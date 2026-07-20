# wirelog

`wirelog` captures every outbound provider HTTP call at the
`http.RoundTripper` level, masks sensitive data in-process, and persists
records asynchronously to Postgres. No sensitive value ever exists beyond the
transport unmasked, and capture can never fail or slow a provider call.

For the design narrative — how the transport, masking, queue, and writer fit
together — see the package overview in [doc.go](doc.go) or `go doc
github.com/ovallabs/wirelog`.

## Quickstart

```sh
docker compose up -d
go run ./example/magma-demo
```

The demo starts a stub Magma server, drives traffic through every outcome
(success, provider_error, timeout, network), and verifies the captured rows.
Expected output ends with:

```
rows per outcome:
  network         1
  provider_error  1
  success         3
  timeout         1
stored bodies containing +237670000001: 0
magma-demo: all assertions passed
```

The demo exits non-zero if any assertion fails, so it doubles as an
end-to-end test. `/health` produces zero rows (ExcludePaths), `/oauth/token`
produces a row with NULL bodies (SkipBodyPaths), and the demo MSISDN never
appears in stored bodies (masking).

## Integration guide

Create one `Wirelog` instance per service, then mint one `http.Client` per
provider:

```go
wl, err := wirelog.New(ctx,
    "postgres://wirelog:wirelog@localhost:5439/wirelog?sslmode=disable",
    wirelog.WithDefaultConsumer("payments-api"),
    wirelog.WithAutoMigrate(true),
    wirelog.WithLogger(log.Default()),
)
// If wirelog must never block service boot, keep wl nil on error:
// HTTPClient on a nil *Wirelog returns a plain otelhttp client.
defer wl.Close() // drains the queue and flushes before the pool closes

cfg := wirelog.NewConfig("magma",
    wirelog.WithCaptureBodies(true),
    wirelog.WithExtraMaskFields("sender_first_name", "sender_last_name"),
)
client := wl.HTTPClient(cfg) // transport chain: wirelog → otelhttp → http.DefaultTransport
```

Annotate calls through the request context so records carry business meaning:

```go
ctx = wirelog.WithRef(ctx, "PYT-2026-0001")            // your internal reference
ctx = wirelog.WithOperation(ctx, "payout.execute")     // business operation
ctx = wirelog.WithIdempotencyKey(ctx, "idem-0001")
ctx = wirelog.WithConsumer(ctx, "checkout")            // overrides the instance default
ctx = wirelog.WithTags(ctx, map[string]any{"batch": "b-42"}) // merges across calls
req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
resp, err := client.Do(req)
```

`wl.Dropped()` reports records that never reached the database — non-blocking
enqueue drops plus failed-insert batches. Any logger with
`Printf(format string, args ...any)` satisfies `wirelog.Logger`. Both `Close`
and `Dropped` are safe on a nil `*Wirelog`, so the degraded pattern above
needs no guards.

In production, always set `WithLogger` and alert on a growing `Dropped()`:
the default logger is a silent no-op, so with it a misconfigured database
drops every record without a single line of output.

Prefer `context.WithTimeout` on the request over `http.Client.Timeout` for
per-call deadlines. The client's timer cancels the request in a way that can
reach the transport as a bare "request canceled" error, which classifies as
`network`; a context deadline always reaches wirelog as
`context.DeadlineExceeded` and classifies as `timeout`.

> **Warning:** always build configs with `wirelog.NewConfig`. A literal
> `wirelog.Config{...}` opts out of the shared mask defaults — its
> `MaskFields` stays exactly what you set, which may be nothing.

## Configuration reference

Instance options (`wirelog.New`):

| Option | Default | Effect |
|---|---|---|
| `WithBuffer(n)` | 2048 | enqueue channel capacity; full buffer drops records |
| `WithBatchSize(n)` | 100 | records per batch INSERT |
| `WithFlushInterval(d)` | 2s | flush cadence for partial batches |
| `WithLogger(l)` | silent no-op | receives one line per failed batch insert |
| `WithAutoMigrate(b)` | false | apply the embedded DDL in `New` |
| `WithDefaultConsumer(c)` | "" | consumer stamped on every record unless overridden |

Provider config (`wirelog.NewConfig(provider, ...)`):

| Option | Default | Effect |
|---|---|---|
| `WithCaptureBodies(b)` | false | store masked request/response bodies |
| `WithExtraMaskFields(f...)` | shared list | APPENDS to the default mask list, never replaces |
| `WithExtraSkipBodyPaths(p...)` | `/oauth`, `/token`, `/auth` | record metadata and sizes only, never bodies |
| `WithExtraExcludePaths(p...)` | `/health`, `/ping`, `/status` | no record at all |
| `WithMasker(m)` | `"•••"` constant | custom masking for matched JSON body fields (headers always get the constant) |

Path options match as substrings of `req.URL.Path` only, never the query
string. Substring means `/auth` also matches `/authors` — choose needles
accordingly (a trailing slash or a more specific fragment narrows the match). Bodies are truncated to `MaxBodyBytes` (default 16384) before
parsing; non-JSON bodies are stored as `{"_raw": "...", "_truncated": true}`.
The default mask list covers MSISDNs, account numbers, names, tokens, and
similar fields — see `defaultMaskFields` in [defaults.go](defaults.go).

## Querying the logs

By your internal reference:

```sql
select created_at, method, endpoint, status_code, outcome, latency_ms
from provider_api_logs
where internal_ref = 'PYT-2026-0001'
order by created_at desc;
```

Recent failures for one provider (`remote_ip` is the resolved provider IP,
NULL when the connection was never established):

```sql
select created_at, endpoint, outcome, status_code, remote_ip, error
from provider_api_logs
where provider = 'magma' and outcome <> 'success'
order by created_at desc
limit 50;
```

Body containment via `@>` (uses the GIN indexes):

```sql
select created_at, endpoint, response_body
from provider_api_logs
where response_body @> '{"status": "accepted"}';
```
