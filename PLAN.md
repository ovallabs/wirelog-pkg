# PLAN.md — wirelog Phase 1

Statuses: `[ ]` todo · `[~]` in progress · `[x]` done.
Each task = the file plus its `_test.go`, committed together. "B#" = Behaviors item in wirelog_frd_phase1.md.
Work pauses for human review at the end of every stage.

## Stage 1 — config / context / mask / outcome / normalize

- [x] `go.mod` + `quality.sh` scaffolding — module `github.com/ovallabs/wirelog`, go 1.22+; quality.sh: gofmt · vet · golangci-lint (if present) · test -cover
- [x] `config.go` + `config_test.go` — types only (Config, ConfigOption, Masker, Operation); Config must be safe as read-only shared state after mint (B17)
- [x] `defaults.go` + `defaults_test.go` — the one shared mask list; `WithExtraMaskFields` APPENDS, never replaces; CaptureBodies never defaults true
- [x] `context.go` + `context_test.go` — `WithTags` MERGES across calls (not replace); consumer precedence ctx > Config > instance default (B10)
- [x] `mask.go` + `mask_test.go` — replace a matched subtree's VALUE wholesale, no recursion into it (B6); header masking copies, never mutates the source map (B5); truncate to MaxBodyBytes BEFORE json.Unmarshal, `{"_raw":…,"_truncated":…}` wrap (B4); unmarshalable custom-Masker output remasks with the constant, never falls back to raw bytes (B1)
- [x] `outcome.go` + `outcome_test.go` — DeadlineExceeded wrapped inside `*url.Error` must still classify as timeout (B7)
- [x] `doc.go` + analogy anchor lines on every file — mail-room documentation style per CLAUDE.md amendment (2026-07-16)
- [x] `normalize.go` + `normalize_test.go` — UUID / all-numeric / long-hex segments → `{id}`, everything else untouched (B14) — done before defaults.go, which needs DefaultNormalizer

## Stage 2 — transport / body / record

- [x] `body.go` + `body_test.go` — snapshot via `req.GetBody` ONLY, never consume `req.Body`; caller receives the complete body byte-for-byte while wirelog keeps a truncated copy (B3); landmark analogy fragment on the body swap line; a mid-stream read error is replayed to the caller after the buffered bytes
- [x] `record.go` + `record_test.go` — sizes always recorded even with CaptureBodies=false, response size from actual bytes read w/ Content-Length fallback (B9); consumer precedence resolution (B10); also holds `capture` (minted read-only state, B17) with Q6 normalization at mint
- [x] `transport.go` + `transport_test.go` — return the wrapped transport's response/error bit-for-bit, capture can never fail the call (B2); ExcludePaths short-circuit before ANY work incl. timing (B8); mask before enqueue (B1); no per-request mutable state on the transport (B17); landmark analogy fragment on the non-blocking enqueue line; nil-receiver HTTPClient test deferred to `wirelog_test.go` (Stage 3, where HTTPClient exists)

## Stage 3 — writer / migrate / wirelog

- [ ] `logger.go` (covered via writer tests) — insert failures produce exactly one Logger line and never propagate (B2); default is silent no-op
- [x] `migrate.go` + `migrate_test.go` — FRD DDL embedded verbatim; auto-migrate default false; migrate takes a small unexported execer interface so tests need no database
- [ ] `writer.go` + `writer_test.go` — single goroutine, flush at batch size OR interval, Close drains → final flush → pool close → goroutine exit (B13); NULL mapping for ''/0/nil (B15); numbered placeholders only, 10s insert timeout (B13); insert failure adds len(batch) to Dropped (B2, Q4 ruling); tests use in-package recording fake, no new dependency (Q8 ruling); landmark analogy fragment on the drain-on-close line
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

## Open questions — RESOLVED 2026-07-16, amended into the FRD

All ten ruled on by the user; the FRD is the single source of truth for the outcomes. Summary:

1. **Response-capture timing** — ACCEPTED eager: full response read inside RoundTrip, caller gets io.NopCloser over complete bytes, truncated capture, enqueue before return. SkipBodyPaths is the escape hatch. (B3)
2. **Body "not read"** — ACCEPTED: CaptureBodies=false or SkipBodyPaths match → body never read; sizes fall back to Content-Length / req.ContentLength, 0 when unknown. (B9)
3. **"Bit-for-bit"** — ACCEPTED: same *http.Response with only Body swapped for identical-bytes reader; error always unmodified. (B2)
4. **Dropped() scope** — OVERRULED: counts BOTH enqueue drops and insert-failure batch drops (+len(batch)); total data-loss visibility. (B2/B13)
5. **Header masking vs Masker** — ACCEPTED: denied headers always `"•••"`; Masker is JSON-body-only. (B1/B5)
6. **Zero-value Config** — ACCEPTED: HTTPClient normalizes at mint; empty MaskFields stays empty; README warns literal construction opts out of defaults. (B11)
7. **Path match target** — ACCEPTED: `req.URL.Path` only, never the query. (B8)
8. **writer_test mock** — ACCEPTED: in-package recording fake behind unexported insert interface, no new dependency.
9. **WithTags collisions** — ACCEPTED: shallow merge, last write wins per key.
10. **endpoint column** — ACCEPTED: path = raw, endpoint = normalized; host not stored (provider column serves that role); schema verbatim. (B14)
