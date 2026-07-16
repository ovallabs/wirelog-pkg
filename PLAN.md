# PLAN.md — wirelog Phase 1

Statuses: `[ ]` todo · `[~]` in progress · `[x]` done.
Each task = the file plus its `_test.go`, committed together. "B#" = Behaviors item in wirelog_frd_phase1.md.
Work pauses for human review at the end of every stage.

## Stage 1 — config / context / mask / outcome / normalize

- [ ] `go.mod` + `quality.sh` scaffolding — module `github.com/ovallabs/wirelog`, go 1.22+; quality.sh: gofmt · vet · golangci-lint (if present) · test -cover
- [ ] `config.go` + `config_test.go` — types only (Config, ConfigOption, Masker, Operation); Config must be safe as read-only shared state after mint (B17)
- [ ] `defaults.go` + `defaults_test.go` — the one shared mask list; `WithExtraMaskFields` APPENDS, never replaces; CaptureBodies never defaults true
- [ ] `context.go` + `context_test.go` — `WithTags` MERGES across calls (not replace); consumer precedence ctx > Config > instance default (B10)
- [ ] `mask.go` + `mask_test.go` — replace a matched subtree's VALUE wholesale, no recursion into it (B6); header masking copies, never mutates the source map (B5); truncate to MaxBodyBytes BEFORE json.Unmarshal, `{"_raw":…,"_truncated":…}` wrap (B4)
- [ ] `outcome.go` + `outcome_test.go` — DeadlineExceeded wrapped inside `*url.Error` must still classify as timeout (B7)
- [ ] `normalize.go` + `normalize_test.go` — UUID / all-numeric / long-hex segments → `{id}`, everything else untouched (B14)

## Stage 2 — transport / body / record

- [ ] `body.go` + `body_test.go` — snapshot via `req.GetBody` ONLY, never consume `req.Body`; caller receives the complete body byte-for-byte while wirelog keeps a truncated copy (B3)
- [ ] `record.go` + `record_test.go` — sizes always recorded even with CaptureBodies=false, response size from actual bytes read w/ Content-Length fallback (B9); consumer precedence resolution (B10)
- [ ] `transport.go` + `transport_test.go` — return the wrapped transport's response/error bit-for-bit, capture can never fail the call (B2); ExcludePaths short-circuit before ANY work incl. timing (B8); mask before enqueue (B1); no per-request mutable state on the transport (B17)

## Stage 3 — writer / migrate / wirelog

- [ ] `logger.go` (covered via writer tests) — insert failures produce exactly one Logger line and never propagate (B2); default is silent no-op
- [ ] `migrate.go` + `migrate_test.go` — FRD DDL embedded verbatim; auto-migrate default false
- [ ] `writer.go` + `writer_test.go` — single goroutine, flush at batch size OR interval, Close drains → final flush → pool close → goroutine exit (B13); NULL mapping for ''/0/nil (B15); numbered placeholders only, 10s insert timeout (B13)
- [ ] `options.go` + `options_test.go` — instance option defaults: buffer 2048, batch 100, flush 2s, no-op logger, auto-migrate off
- [ ] `wirelog.go` + `wirelog_test.go` — HTTPClient nil-receiver-safe, degrades to plain otelhttp client (B11); chain order wirelog → otelhttp → http.DefaultTransport (B12); non-blocking enqueue increments Dropped (B2)
- [ ] optional `//go:build integration` writer test against real Postgres (only test allowed to need Docker)

## Stage 4 — example / docker-compose / README

- [ ] `docker-compose.yml` — postgres:16, port 5439, user/pass/db all `wirelog`
- [ ] `example/magma-demo/main.go` — stub Magma server; drives success, provider_error, timeout, network, excluded `/health` (zero rows), skip-body `/oauth/token` (NULL bodies); post-run report queries Postgres; MSISDN "+237670000001" must appear ZERO times in stored bodies; exit non-zero on any failed assertion
- [ ] `README.md` — quickstart (`docker compose up -d` → `go run ./example/magma-demo` → expected output), integration guide, config reference, psql examples (by ref, failures only, `@>` containment)

## Stage 5 — verification

- [ ] `docker compose up -d` + `go run ./example/magma-demo` passes all assertions (row count per outcome, zero unmasked MSISDN)
- [ ] `./quality.sh` clean
- [ ] `go test -race ./...` clean (B17 enforcement)
- [ ] `go vet ./...` clean

## Open questions

Ambiguities/conflicts found before writing code. Blocking ones marked ⛔; the rest have a proposed default I'll apply unless overruled.

1. ⛔ **Response-capture timing: B1 vs B9/B3.** B1 requires masking + enqueue to happen *inside RoundTrip*, but the response body hasn't been read yet when RoundTrip returns. B9's "actual bytes read during the copy, falling back to Content-Length when the body isn't read" implies a lazy tee (enqueue at body EOF/Close), while body_test's "copyBody returns full bytes to caller + truncated capture" implies an eager full read inside RoundTrip. These are different designs. **Proposed:** eager — read the full response body inside RoundTrip, hand the caller a reconstructed `io.NopCloser(bytes.Reader)` over the complete bytes, capture the truncated copy, enqueue before returning. Simple, satisfies B1/B3/B4 exactly; cost is buffering whole responses in memory. Then B9's Content-Length fallback applies only when we deliberately don't read (see Q2). Confirm eager buffering is acceptable for provider-API-sized responses.
2. **When is the body "not read" (B9)?** With eager capture the body is always read when CaptureBodies=true. **Proposed:** with CaptureBodies=false or a SkipBodyPaths match we do not read the body at all; response_size falls back to Content-Length (and 0 when Content-Length is -1/chunked). request_size likewise from req.ContentLength, 0 when unknown.
3. **B2 "bit-for-bit" vs body replacement.** Returning the response truly untouched is impossible if we must read its body (Q1). **Proposed interpretation:** same *http.Response (status, headers, trailer, error identity) with only resp.Body swapped for a reader that yields identical bytes; the error return is always the wrapped transport's error unmodified.
4. **Does `Dropped()` count insert-failure batches?** B2 defines the counter for full-buffer enqueue drops; insert failures also "drop the batch". **Proposed:** Dropped() counts only enqueue drops (its stated definition); insert-failure drops are visible via the Logger line only.
5. **Header masking vs custom Masker.** Does a custom Masker apply to denied header values, or are headers always replaced with the `"•••"` constant? mask_test's "built-in + custom header masking" could mean DenyHeaders, not Masker. **Proposed:** denied headers always become the mask constant; the Masker applies to JSON body fields only.
6. **Zero-value Config.** `HTTPClient(cfg)` accepts a Config that may be a literal, not from NewConfig: MaxBodyBytes 0, nil PathNormalizer, empty MaskFields. **Proposed:** HTTPClient normalizes at mint — MaxBodyBytes<=0 → 16384, nil PathNormalizer → DefaultNormalizer; empty MaskFields stays empty (literal construction opts out of defaults deliberately).
7. **Substring match target for ExcludePaths/SkipBodyPaths.** `req.URL.Path` only, or the full URL including query? `/token` in a query string would match the latter. **Proposed:** match against `req.URL.Path` only.
8. **writer_test mock.** FRD's dependency rule allows nothing beyond pgx/otelhttp/stdlib, but the test list mentions "mock/pgxmock or a recording fake". **Proposed:** an in-package recording fake behind a small unexported insert interface — no new dependency, per the FRD's default-no rule.
9. **WithTags collision semantics.** MERGE across calls — on duplicate keys, later call wins? **Proposed:** yes, last write wins per key (shallow merge).
10. **`endpoint` column contents.** B14 says "both raw path and normalized endpoint stored". **Proposed:** `path` = raw URL path, `endpoint` = normalized path only (no host/method — those have their own columns... method does; host does not). Sub-question: should host:port be stored anywhere? Currently nothing in the schema holds it. Proposed: no, schema is verbatim.
