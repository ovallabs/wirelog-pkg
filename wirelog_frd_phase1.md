# FRD — wirelog core package (Phase 1)

Implementation spec for Claude Code. Act as a senior Go engineer: idiomatic, minimal dependencies, table-driven tests, no speculative abstraction.

## Mission

Build the core library of `wirelog` — a Go package that captures every outbound provider HTTP call at the `http.RoundTripper` level, masks sensitive data in-process, and persists records asynchronously to Postgres.

Prove it works end-to-end locally: a demo program simulating Magma payout traffic against a stub Magma server, writing real rows into a local Postgres started by docker-compose.

## Hard scope boundary

IN SCOPE (this phase):

* The flat library package: capture, masking, outcome classification, async writer, schema migration, context helpers, config defaults.
* Unit tests for every behavior listed under Acceptance criteria.
* `docker-compose.yml` for local Postgres.
* `example/magma-demo/main.go`: runnable local demo (details below).
* `README.md`: quickstart, integration guide, config reference.
* `quality.sh`: gofmt check · go vet · golangci-lint (if available) · go test -cover.

OUT OF SCOPE (do NOT build, do NOT scaffold):

* Any HTTP server, read API, metrics endpoints, or UI (`server/`, `ui/`, `cmd/` do not exist in this phase).
* resty hooks of any kind. Capture is transport-level ONLY.
* Prometheus/OTel metric export, log shipping, insert retries, alerting.
* Any dependency beyond: `github.com/jackc/pgx/v5` (pool), stdlib, and `go.opentelemetry.io/contrib/.../otelhttp` (transport chaining only). Justify in the PR description if anything else seems necessary — default answer is no. No zerolog, no logrus, no ORM.

## Repository layout (create exactly this)

```
wirelog/
├── go.mod                    # module github.com/ovallabs/wirelog (root, go 1.22+)
├── README.md
├── quality.sh
├── docker-compose.yml        # postgres:16, port 5439, user/pass/db: wirelog
├── wirelog.go                # Wirelog struct, New, Close, Dropped, HTTPClient
├── options.go                # functional options (see API surface)
├── config.go                 # Config, ConfigOption, Masker, Operation types
├── defaults.go               # NewConfig + shared mask defaults + config options
├── context.go                # WithRef/WithOperation/WithConsumer/WithIdempotencyKey/WithTags
├── transport.go              # the RoundTripper (capture)
├── body.go                   # snapshotRequestBody, copyBody
├── record.go                 # record struct + buildRecord
├── mask.go                   # header + recursive JSON masking
├── outcome.go                # classify()
├── normalize.go              # DefaultNormalizer
├── writer.go                 # buffered channel → batched INSERTs
├── migrate.go                # embedded DDL + auto-migrate
├── logger.go                 # Logger interface + no-op default
├── *_test.go                 # alongside every file above
└── example/magma-demo/main.go

```

Flat package `wirelog` for all root files. No internal/ packages in this phase.

## Public API surface (implement exactly; unexported everything else)

```go
// instance
func New(ctx context.Context, dbURL string, opts ...Option) (*Wirelog, error)
func (wl *Wirelog) Close()                       // drain queue, final flush, close pool
func (wl *Wirelog) Dropped() int64               // atomic drop counter
func (wl *Wirelog) HTTPClient(cfg Config) *http.Client  // NIL-SAFE (see behaviors)

// instance options
func WithBuffer(n int) Option                    // default 2048
func WithBatchSize(n int) Option                 // default 100
func WithFlushInterval(d time.Duration) Option   // default 2s
func WithLogger(l Logger) Option                 // default silent no-op
func WithAutoMigrate(b bool) Option              // default false
func WithDefaultConsumer(c string) Option        // stamps every record unless overridden

// minimal logger contract — any logger adapts in one line
type Logger interface{ Printf(format string, args ...any) }

// provider config
type Operation string
type Masker func(field string, value any) any    // field arrives lowercased
type Config struct {
    Provider       string
    Consumer       string
    CaptureBodies  bool
    MaxBodyBytes   int      // default 16384
    MaskFields     []string
    DenyHeaders    []string
    Masker         Masker
    SkipBodyPaths  []string // substring match: metadata+sizes only, never bodies
    ExcludePaths   []string // substring match: NO record at all
    PathNormalizer func(string) string // default DefaultNormalizer
}
func NewConfig(provider string, opts ...ConfigOption) Config
func WithExtraMaskFields(f ...string) ConfigOption   // APPEND-only
func WithCaptureBodies(b bool) ConfigOption
func WithExtraExcludePaths(p ...string) ConfigOption
func WithExtraSkipBodyPaths(p ...string) ConfigOption
func WithMasker(m Masker) ConfigOption
func DefaultNormalizer(path string) string

// context annotations (unexported key types)
func WithRef(ctx context.Context, ref string) context.Context
func WithOperation(ctx context.Context, op Operation) context.Context
func WithConsumer(ctx context.Context, consumer string) context.Context
func WithIdempotencyKey(ctx context.Context, key string) context.Context
func WithTags(ctx context.Context, tags map[string]any) context.Context  // MERGES across calls

```

`NewConfig` shared mask defaults (maintain as one list; CaptureBodies never defaults true): `msisdn, phone, phone_number, mobile, account_number, account_name, iban, bvn, nin, pin, otp, cvv, pan, password, secret, token, access_token, refresh_token, api_key, first_name, last_name, address, email, receiver_account, receiver_account_number, sender_phone_number`

Default SkipBodyPaths: `/oauth, /token, /auth`. Default ExcludePaths: `/health, /ping, /status`.

## Database schema (use verbatim; embed in migrate.go)

```sql
create table if not exists provider_api_logs (
    id               bigint generated always as identity primary key,
    created_at       timestamptz not null default now(),
    provider         text        not null,
    consumer         text        not null default '',
    operation        text        not null default '',
    endpoint         text        not null default '',
    path             text        not null default '',
    method           text        not null default '',
    status_code      int,
    outcome          text        not null,
    latency_ms       bigint      not null default 0,
    request_size     bigint      not null default 0,
    response_size    bigint      not null default 0,
    internal_ref     text,
    idempotency_key  text,
    request_headers  jsonb,
    request_body     jsonb,
    response_headers jsonb,
    response_body    jsonb,
    error            text,
    tags             jsonb
);
create index if not exists idx_pal_provider_time on provider_api_logs (provider, created_at desc);
create index if not exists idx_pal_consumer_time on provider_api_logs (consumer, created_at desc);
create index if not exists idx_pal_internal_ref  on provider_api_logs (internal_ref) where internal_ref is not null;
create index if not exists idx_pal_idem_key      on provider_api_logs (idempotency_key) where idempotency_key is not null;
create index if not exists idx_pal_failures      on provider_api_logs (created_at desc) where outcome <> 'success';
create index if not exists idx_pal_req_body_gin  on provider_api_logs using gin (request_body  jsonb_path_ops);
create index if not exists idx_pal_resp_body_gin on provider_api_logs using gin (response_body jsonb_path_ops);

```

## Behaviors (non-negotiable)

1. Mask before persist. No sensitive value may exist beyond the transport unmasked. Masking runs in RoundTrip before enqueue. Masked value constant: `"•••"` unless a custom Masker is set.
2. Logging never blocks or fails a provider call. Enqueue is `select { case ch <- rec: default: dropped++ }`. Insert failures: one Logger line, drop the batch, never propagate. RoundTrip must return the response/error from the wrapped transport bit-for-bit.
3. Body stream integrity. The caller ALWAYS receives the complete response body even when wirelog logs a truncated copy. Request bodies are snapshotted via `req.GetBody` only — never consume `req.Body`. If `GetBody` is nil, record size (from ContentLength) and skip content.
4. Truncate before parse. Body bytes are cut to MaxBodyBytes BEFORE json.Unmarshal. Non-JSON or broken-by-truncation bodies wrap as `{"_raw": "<string>", "_truncated": true?}`. Empty body → SQL NULL. jsonb columns receive valid JSON or NULL, always.
5. Header masking copies, never mutates. Built-in denylist (always, case-insensitive): authorization, proxy-authorization, cookie, set-cookie, x-api-key, api-key, x-auth-token, x-signature. Plus Config.DenyHeaders.
6. Recursive body masking. Walk decoded JSON (objects + arrays); keys matched case-insensitively against the mask set; on match replace the VALUE entirely (do not recurse into a matched subtree).
7. Outcome classification. Response present: 2xx → success, else provider_error. Error path: errors.Is(err, context.DeadlineExceeded) OR errors.As net.Error with Timeout() → timeout; anything else → network. Store err.Error() in the error column.
8. ExcludePaths short-circuit before ANY work (no timing, no snapshot). SkipBodyPaths still record metadata, sizes, masked headers.
9. Sizes always recorded (request_size, response_size) even with CaptureBodies=false. Response size = actual bytes read during the copy, falling back to Content-Length when the body isn't read.
10. Consumer precedence: WithConsumer(ctx) > Config.Consumer > instance WithDefaultConsumer.
11. Nil-safe HTTPClient: called on a nil *Wirelog it returns a plain client with `otelhttp.NewTransport(http.DefaultTransport)` — services that boot despite wirelog init failure degrade silently.
12. Transport chain order: wirelog wraps otelhttp wraps http.DefaultTransport.
13. Writer: single goroutine; flush at batch size OR flush interval; multi-row INSERT with numbered placeholders only (never interpolate values); 10s timeout per insert; Close() drains, final-flushes, then closes the pool; goroutine must terminate.
14. Path normalization: DefaultNormalizer replaces UUID segments, all-numeric segments, and long hex segments with `{id}`. Both raw path and normalized endpoint stored.
15. NULL mapping in the writer: empty-string internal_ref, idempotency_key, and error → SQL NULL (not ''); status_code 0 → NULL; nil header/body/tags maps → NULL. Non-nullable text columns (consumer, operation, endpoint, path, method) keep '' defaults. Header maps and tags are json.Marshal-ed for their jsonb params.
16. Redirects: http.Client resolves redirects ABOVE the transport, so each hop is its own RoundTrip and produces its own record. This is accepted and correct (each hop truly crossed the wire); do not add redirect deduplication.
17. Thread safety: one HTTPClient is shared by many goroutines. The transport must hold no per-request mutable state; Config is read-only after mint. The race detector run is the enforcement.
18. Deliberately deferred (do NOT implement): an IdempotencyHeader fallback (reading the key from a request header when the ctx annotation is absent) is planned for a later phase. Phase 1 is context-only.

## Local verification (Kehinde's requirement — build this to be runnable)

`example/magma-demo/main.go` must:

1. Start an in-process stub Magma server (`net/http/httptest`) exposing:
   * `GET /partner/balance` → 200 JSON balance response (~120ms delay)
   * `POST /v1/transfers` → 200 JSON with transfer_token; request echoes a realistic payload containing receiver_account (MSISDN), amounts, names
   * `POST /v1/transfers` with header `X-Demo-Fail: provider` → 422 JSON error
   * `GET /health` → 200
   * a `/slow` endpoint sleeping 3s (for timeout demo)
2. `wirelog.New(ctx, "postgres://wirelog:wirelog@localhost:5439/wirelog?sslmode=disable", WithDefaultConsumer("magma-demo"), WithAutoMigrate(true), WithLogger(stdout adapter))`.
3. Mint a client with `NewConfig("magma", WithCaptureBodies(true), WithExtraMaskFields("sender_first_name","sender_last_name","sender_address"))`.
4. Drive traffic that exercises every outcome: balance check + successful transfer (with WithRef/WithOperation("payout.execute")/WithIdempotencyKey), a provider_error transfer, a timeout (client timeout 500ms against /slow), a network error (call a closed port), a /health call (must produce NO row), an /oauth/token call (row with NULL bodies).
5. `wl.Close()`, then print a verification report by querying Postgres: row count per outcome, and a check that scans request_body/response_body text for the demo MSISDN "+237670000001" — MUST report zero occurrences. Exit non-zero if any assertion fails, so the demo doubles as an e2e test.

README must document: `docker compose up -d` → `go run ./example/magma-demo` → expected output → example psql queries (by ref, failures only, body containment via `@>`).

## Acceptance criteria / required tests

Table-driven where sensible; no test may require Docker except an optional `//go:build integration` file for the writer against real Postgres.

* mask_test: built-in + custom header masking; source header map unmutated; nested object/array masking; case-insensitivity; matched-subtree replacement; custom Masker; non-JSON wrap; truncation marker; empty→nil.
* outcome_test: 200/201/404/500/0; naked DeadlineExceeded; DeadlineExceeded wrapped in *url.Error; net.Error timeout; connection-refused → network.
* context_test: all five helpers; tag MERGE across calls; consumer precedence chain (all three levels); operation overwrite (last wins).
* transport_test (httptest): full record fields on success; ExcludePaths → zero records; SkipBodyPaths → metadata + sizes, nil bodies; sizes recorded with CaptureBodies=false; response body delivered INTACT to caller when larger than MaxBodyBytes (byte-for-byte compare); response/error returned identical to wrapped transport's; nil-receiver HTTPClient returns working plain client.
* body_test: GetBody snapshot leaves original request body readable; GetBody==nil → size only; copyBody returns full bytes to caller + truncated capture.
* writer_test: N records → ceil(N/batch) inserts (mock/pgxmock or a recording fake); flush on interval with partial batch; non-blocking enqueue with full buffer increments Dropped; Close drains and flushes remainder; insert error → batch dropped + one log line, no panic.
* normalize_test: UUIDs, numerics, long hex, mixed paths, already-clean paths.
* defaults_test: NewConfig list intact; WithExtraMaskFields appends (never replaces); CaptureBodies defaults false.
* `go vet ./...` clean; `quality.sh` passes; race detector clean (`go test -race ./...`).

## Working method (for Claude Code)

* Commit in reviewable stages: (1) config/context/mask/outcome/normalize + tests → (2) transport/body/record + tests → (3) writer/migrate/wirelog + tests → (4) example + docker-compose + README. Conventional Commits.
* Every exported identifier gets a doc comment. Explain WHY on the non-obvious lines (truncate-before-parse, copy-not-mutate, select/default).
* If any requirement here conflicts with itself or with Go reality, STOP and surface the conflict in a comment block at the top of the affected file instead of silently choosing.
