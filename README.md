# wirelog

`wirelog` captures every outbound provider HTTP call at the
`http.RoundTripper` level, masks sensitive data in-process, and persists a
structured record of each exchange asynchronously to Postgres.

It is built to be invisible to the calls it observes: **no sensitive value ever
exists beyond the transport unmasked**, and **capture can never fail, block, or
slow a provider call** — if the database is down or the queue is full, the
provider request still goes out untouched and the record is simply dropped and
counted.

- **Transport-level.** Works with any `*http.Client` — stdlib, resty, anything
  that uses an `http.RoundTripper`. No SDK hooks, no per-call instrumentation.
- **Safe by default.** Auth headers and a broad set of PII body fields are
  masked out of the box.
- **Non-blocking.** A buffered queue feeds a single background writer that
  batches inserts; the hot path never touches Postgres.
- **Degrades silently.** A failed init leaves a `nil *Wirelog` that still mints
  working (uncaptured) clients, so a wirelog outage never takes your service
  down.
- **Minimal deps.** Only `pgx/v5` and `otelhttp`, plus the standard library.

---

## Table of contents

- [How it works — the mail room](#how-it-works--the-mail-room)
- [Install](#install)
- [Quickstart (local demo)](#quickstart-local-demo)
- [Integration guide](#integration-guide)
  - [1. Create one instance per service](#1-create-one-instance-per-service)
  - [2. Mint a client per provider](#2-mint-a-client-per-provider)
  - [3. Providers with their own transport (proxy, custom TLS)](#3-providers-with-their-own-transport-proxy-custom-tls)
  - [4. Annotate calls with business context](#4-annotate-calls-with-business-context)
  - [5. Shut down cleanly](#5-shut-down-cleanly)
- [What gets masked](#what-gets-masked)
- [Outcome classification](#outcome-classification)
- [Configuration reference](#configuration-reference)
- [Database schema](#database-schema)
- [Querying the logs](#querying-the-logs)
- [Production notes](#production-notes)
- [Dependencies & versioning](#dependencies--versioning)
- [FAQ](#faq)

---

## How it works — the mail room

The whole package fits one picture: a company **mail room** that every outbound
letter (HTTP request) passes through on its way to a provider.

```
  your code                                             provider
     │                                                     ▲
     ▼                                                     │
  request ──►  [ transport / mail-room window ]  ──►  [ otelhttp ]  ──►  [ proxy or DefaultTransport ]
                     │  photocopies the letter
                     │  and its reply, masks the
                     ▼  copy, drops it in the slot
              [ queue / outgoing mail slot ]  ──►  [ writer / mail van ]  ──►  provider_api_logs (Postgres)
```

- **The transport** (`transport.go`) is the mail-room window every letter passes
  through — an `http.RoundTripper` that forwards each request to the wrapped
  transport and photocopies the letter and its reply, building a record from the
  request, response, latency and outcome. The original is never altered: the
  caller always receives the wrapped transport's response and error, with only
  the response body swapped for a reader yielding identical bytes.
- **Masking** (`mask.go`) is the redaction desk — it blacks out sensitive lines
  on the photocopy, never the original. Denied header values and matched JSON
  body fields are replaced *inside* `RoundTrip`, before anything is queued, so no
  unmasked value exists past the transport.
- **The queue** is the outgoing mail slot — a buffered channel between transport
  and writer. Enqueue never blocks: when the slot is full the photocopy is
  discarded and counted via `Dropped()`, while the letter itself always goes out.
- **The writer** (`writer.go`) is the mail van — a single goroutine that leaves
  when full or on a schedule (batch size or flush interval), delivering batches
  to the archive, the `provider_api_logs` table. `Close` drains the slot, makes a
  final delivery, then parks the van.
- **Context helpers** (`context.go`) are notes written on the envelope before
  sending — `WithRef`, `WithOperation`, etc. annotate a `context.Context` so the
  record carries business meaning the transport could never infer on its own.

The full narrative lives in the package overview — `go doc github.com/ovallabs/wirelog`
or [doc.go](doc.go).

---

## Install

```sh
go get github.com/ovallabs/wirelog
```

Requires Go 1.22+.

---

## Quickstart (local demo)

```sh
docker compose up -d          # Postgres on :5439
go run ./example/magma-demo   # or set WIRELOG_DATABASE_URL to point elsewhere
```

The demo starts a stub Magma server, drives traffic through **every outcome**
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

It exits non-zero if any assertion fails, so it doubles as an end-to-end test.
`/health` produces zero rows (ExcludePaths), `/oauth/token` produces a row with
NULL bodies (SkipBodyPaths), and the demo MSISDN never appears in a stored body
(masking). The demo source in [example/magma-demo/main.go](example/magma-demo/main.go)
is a working reference for the integration steps below.

---

## Integration guide

### 1. Create one instance per service

A `*Wirelog` owns a Postgres pool, the record queue, and the background writer.
Create it once at startup:

```go
wl, err := wirelog.New(ctx, os.Getenv(wirelog.EnvDatabaseURL), // "WIRELOG_DATABASE_URL"
    wirelog.WithDefaultConsumer("payments-api"), // stamped on every record
    wirelog.WithLogger(myLogger),                // receives one line per failed insert
    wirelog.WithAutoMigrate(false),              // apply the DDL yourself in prod (default false)
)
if err != nil {
    // capture is best-effort: keep wl nil and carry on — clients still work, just uncaptured
    log.Err(err).Msg("wirelog init failed, continuing without provider API capture")
}
defer wl.Close() // drains the queue and flushes before the pool closes; nil-safe
```

`WithLogger` takes anything with `Printf(format string, args ...any)` — the
stdlib `*log.Logger` satisfies it directly; for zerolog/zap, a one-method
adapter does the job.

### 2. Mint a client per provider

`HTTPClient` returns an `*http.Client` whose transport chain is
`wirelog → otelhttp → http.DefaultTransport`. Use it wherever that provider's
calls are made:

```go
cfg := wirelog.NewConfig("magma",
    wirelog.WithCaptureBodies(true),
    wirelog.WithExtraMaskFields("sender_firstname", "sender_lastname"),
)
client := wl.HTTPClient(cfg)
```

On a **nil** `*Wirelog`, `HTTPClient` returns a plain `otelhttp` client — so the
degraded-init path above needs no special-casing at call sites.

### 3. Providers with their own transport (proxy, custom TLS)

`HTTPClient` roots its chain at `http.DefaultTransport`. When a provider builds
its **own** transport — an egress proxy, custom TLS — that fixed chain would
discard it. Use `WrapTransport` to layer capture *on top* of the provider's
transport instead:

```go
base := otelhttp.NewTransport(providerProxyTransport) // the provider's own transport
providerClient := &http.Client{
    Transport: wl.WrapTransport(cfg, base), // chain: wirelog → otelhttp → proxy
}
```

`WrapTransport` is nil-safe too: on a nil `*Wirelog` it returns `base`
unchanged, so the provider keeps working (without capture) if init failed. A nil
`base` falls back to `otelhttp → http.DefaultTransport`.

> At Ovalfi this wiring is centralized in the `httpx` package (in
> integrations-playground): it builds the one wirelog instance itself from a
> connection-string env var on first use, so services wire nothing and every
> provider is captured. `httpx` is the resty-specific adapter; wirelog stays
> generic.

### 4. Annotate calls with business context

The transport can see the URL, status, latency and bodies, but not *why* a call
was made. Attach that through the request context and it lands in the record:

```go
ctx = wirelog.WithRef(ctx, "PYT-2026-0001")             // your internal reference
ctx = wirelog.WithOperation(ctx, "payout.execute")      // business operation
ctx = wirelog.WithIdempotencyKey(ctx, "idem-0001")
ctx = wirelog.WithConsumer(ctx, "checkout")             // overrides the instance default
ctx = wirelog.WithTags(ctx, map[string]any{"batch": "b-42"}) // merges across calls
req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
resp, err := client.Do(req)
```

Consumer precedence is `WithConsumer(ctx)` > `Config.Consumer` >
`WithDefaultConsumer`. `WithTags` shallow-merges across calls (last write wins
per key); repeated `WithOperation` — last wins.

### 5. Shut down cleanly

`wl.Close()` stops the writer, drains whatever is queued, does one final flush,
then closes the pool. Call it on graceful shutdown so you don't lose the last
batch (up to `WithFlushInterval`, default 2s, of records). It is safe on a nil
receiver and safe to call more than once.

---

## What gets masked

Masking happens **inside `RoundTrip`, before the record is queued** — the
unmasked value never leaves the transport. The masked value is the constant
`•••` unless you supply a custom `Masker`.

**Headers** — always masked, case-insensitively, on every record:

```
authorization   proxy-authorization   cookie   set-cookie
x-api-key        api-key               x-auth-token   x-signature
```

Add provider-specific auth headers with `WithExtraDenyHeaders`:

```go
wirelog.NewConfig("magma", wirelog.WithExtraDenyHeaders("X-User-Secret"))
```

**Body fields** (only when `WithCaptureBodies(true)`) — JSON is walked
recursively (objects and arrays); keys are matched case-insensitively against
the mask set, and on a match the **whole value is replaced** (no recursion into a
matched subtree). Form-encoded bodies are masked by key too. The shared default
mask set:

```
msisdn  phone  phone_number  mobile  account_number  account_name
iban  bvn  nin  pin  otp  cvv  pan  password  secret  token
access_token  refresh_token  api_key  first_name  last_name  address
email  receiver_account  receiver_account_number  sender_phone_number
```

Extend it per provider with `WithExtraMaskFields(...)` (it **appends**, never
replaces). Keys are matched **exactly** (case-insensitively) — `first_name`
does not match `receiver_first_name`, so add provider-specific variants
explicitly. Bodies are truncated to `MaxBodyBytes` (default 16384) **before**
parsing; a body that isn't valid JSON (or was cut mid-token) is stored as
`{"_raw": "…", "_truncated": true}`. Empty bodies become SQL NULL.

> **Always build configs with `wirelog.NewConfig`.** A literal
> `wirelog.Config{...}` opts out of the shared defaults — its `MaskFields` stays
> exactly what you set, which may be nothing.

---

## Outcome classification

Every record carries an `outcome`, derived from the response or error:

| Outcome | When |
|---|---|
| `success` | response with a 2xx status |
| `provider_error` | response with a non-2xx status |
| `timeout` | `context.DeadlineExceeded`, or a `net.Error` with `Timeout() == true` (including when wrapped in `*url.Error`) |
| `network` | any other transport error (connection refused, DNS, reset, …) |

The error string is stored in the `error` column for the failure paths.

> Prefer `context.WithTimeout` on the request over `http.Client.Timeout`. The
> client's timer cancels the request in a way that can reach the transport as a
> bare "request canceled" error, which classifies as `network`; a context
> deadline always arrives as `context.DeadlineExceeded` and classifies as
> `timeout`.

---

## Configuration reference

**Instance options** (`wirelog.New`):

| Option | Default | Effect |
|---|---|---|
| `WithBuffer(n)` | 2048 | enqueue channel capacity; a full buffer drops records (counted in `Dropped`) |
| `WithBatchSize(n)` | 100 | records per multi-row INSERT |
| `WithFlushInterval(d)` | 2s | flush cadence for partial batches |
| `WithLogger(l)` | silent no-op | receives one line per failed batch insert |
| `WithAutoMigrate(b)` | false | apply the embedded DDL during `New` |
| `WithDefaultConsumer(c)` | "" | consumer stamped on every record unless overridden |

**Provider config** (`wirelog.NewConfig(provider, ...)`):

| Option | Default | Effect |
|---|---|---|
| `WithCaptureBodies(b)` | false | store masked request/response bodies |
| `WithExtraMaskFields(f...)` | shared list | **appends** body-field names to mask |
| `WithExtraDenyHeaders(h...)` | built-in list | **appends** header names to mask |
| `WithExtraSkipBodyPaths(p...)` | `/oauth`, `/token`, `/auth` | record metadata and sizes only, never bodies |
| `WithExtraExcludePaths(p...)` | `/health`, `/ping`, `/status` | no record at all (short-circuits before any work) |
| `WithMasker(m)` | `•••` constant | custom masking for matched JSON body fields (headers always get the constant) |

Path options match as **substrings of `req.URL.Path`** only, never the query
string. Substring means `/auth` also matches `/authors` — narrow the needle with
a trailing slash or a more specific fragment when needed. `MaxBodyBytes` defaults
to 16384; a nil `PathNormalizer` falls back to `DefaultNormalizer`, which
collapses UUID, all-numeric, and long-hex path segments to `{id}` (so
`/users/123` files under the endpoint `/users/{id}`).

`EnvDatabaseURL` is the conventional env var name (`WIRELOG_DATABASE_URL`) for
the DSN, so services reference it instead of hardcoding.

---

## Database schema

Records land in `provider_api_logs`. The DDL is embedded in
[migrate.go](migrate.go); apply it with `WithAutoMigrate(true)` in dev, or run it
as a migration in prod (the app's DB role then needs only `INSERT`).

| Column | Notes |
|---|---|
| `id`, `created_at` | identity PK, insert timestamp |
| `provider`, `consumer`, `operation` | who called, on whose behalf, doing what |
| `endpoint`, `path`, `method` | normalized endpoint, raw path, HTTP method |
| `remote_ip` | resolved provider IP; NULL if the connection never established |
| `status_code`, `outcome`, `error` | status (NULL on transport error), classification, error string |
| `latency_ms`, `request_size`, `response_size` | timing and byte sizes (always recorded, even with bodies off) |
| `internal_ref`, `idempotency_key` | from context annotations |
| `request_headers`, `request_body`, `response_headers`, `response_body` | masked `jsonb`; NULL when not captured |
| `tags` | merged context tags as `jsonb` |

Indexes cover provider+time, consumer+time, `internal_ref`, `idempotency_key`, a
partial index on failures, and GIN indexes on both body columns for containment
queries.

---

## Querying the logs

By your internal reference:

```sql
select created_at, method, endpoint, status_code, outcome, latency_ms
from provider_api_logs
where internal_ref = 'PYT-2026-0001'
order by created_at desc;
```

Recent failures for one provider:

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

---

## Production notes

- **Always set `WithLogger` and alert on a rising `Dropped()`.** The default
  logger is a silent no-op — with it, a misconfigured database drops every record
  without a single line of output. `Dropped()` counts both full-buffer enqueue
  drops and failed-insert batches.
- **Grant the app role `INSERT` only.** wirelog's writer only inserts; a
  dedicated append-only role keeps the blast radius small if credentials leak.
  Run the DDL as a migration rather than `WithAutoMigrate` so the app role needs
  no DDL rights.
- **Size the buffer for burst, not average.** If `Dropped()` climbs under load,
  raise `WithBuffer`, lower `WithFlushInterval`, or check writer/DB health.
- **Thread-safe by construction.** One `*http.Client` is shared across
  goroutines; the transport holds no per-request mutable state and `Config` is
  read-only after mint. The race detector is the enforcement.
- **Redirects each produce their own record** — the `http.Client` resolves them
  above the transport, so each hop is a real wire crossing and is logged as such.

---

## Dependencies & versioning

wirelog depends only on `github.com/jackc/pgx/v5` and
`go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp`, plus the
standard library. **No zerolog, no resty, no ORM.**

The `otelhttp` requirement is a **minimum** (`v0.62.0`), not a pin — wirelog only
uses `otelhttp.NewTransport`, so a consumer's minimal-version selection is free to
pick a newer compatible version its other dependencies need.

---

## FAQ

**Does it slow down or risk provider calls?**
No. Masking and enqueue happen in-process with a non-blocking send; a full queue
or a dead database drops the record, never the request. The caller always
receives the wrapped transport's exact response and error.

**Do I need a client per provider?**
Yes — the provider name and its mask/capture policy live in `Config`, and one
client is minted per `Config`. Context annotations then add per-call detail.

**Can I use it with resty / a custom client?**
Yes. wirelog produces an `http.RoundTripper`; point resty at it with
`SetTransport`, or use `WrapTransport` over any base transport. No SDK hooks
needed.

**Why isn't a field being masked?**
Body-field matching is exact (case-insensitive). `first_name` won't match
`receiver_first_name` — add the exact key via `WithExtraMaskFields`. For custom
auth headers, use `WithExtraDenyHeaders`.

**What happens if wirelog fails to start?**
Keep the `nil *Wirelog`. `HTTPClient`, `WrapTransport`, `Close` and `Dropped`
are all nil-safe, so your service boots and runs normally — just without capture.
