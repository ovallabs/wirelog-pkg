// Package wirelog captures outbound provider HTTP calls at the
// http.RoundTripper level, masks sensitive data in-process, and persists
// records asynchronously to Postgres.
//
// The whole package fits one picture: a company mail room that every
// outbound letter (HTTP request) passes through on its way to a provider.
//
// Context helpers (context.go) are notes written on the envelope before
// sending: WithRef, WithOperation, WithConsumer, WithIdempotencyKey and
// WithTags annotate a context.Context so the eventual record carries
// business meaning the transport could never infer on its own.
//
// The transport (transport.go) is the mail-room window every letter passes
// through: an http.RoundTripper that forwards each request to the wrapped
// transport and photocopies the letter and its reply — building a record
// from the request, response, latency and outcome. The original is never
// altered: the caller always receives the wrapped transport's response and
// error, with only the response body swapped for a reader yielding
// identical bytes. HTTPClient mints this over the default chain (wirelog →
// otelhttp → http.DefaultTransport); WrapTransport mints it over a
// provider's own transport, so an egress proxy or custom TLS survives.
//
// Masking (mask.go) is the redaction desk: it blacks out sensitive lines on
// the photocopy, never the original. Denied header values and matched JSON
// body fields are replaced in the record inside RoundTrip, before anything
// is queued — no unmasked value exists beyond the transport.
//
// The queue is the outgoing mail slot: a buffered channel between transport
// and writer. Enqueue never blocks — when the slot is full the photocopy is
// discarded and counted via Dropped, while the letter itself always goes
// out; capture can never fail or slow a provider call.
//
// The writer (writer.go) is the mail van: a single goroutine that leaves
// when full or on schedule — flushing at batch size or flush interval — and
// delivers batches of records to the archive, the Postgres table
// provider_api_logs. Close drains the slot, makes one final delivery, then
// parks the van: the goroutine exits and the connection pool closes.
// Dropped also counts records lost when a delivery fails (a failed batch
// insert).
//
// Around that core: Config (config.go) is a correspondent's standing
// instruction sheet — which letters to photocopy (CaptureBodies), which
// lines to redact (MaskFields, DenyHeaders), which mail to log by envelope
// only (SkipBodyPaths) or not at all (ExcludePaths). NewConfig
// (defaults.go) supplies the house rules: the shared mask defaults every
// instruction sheet starts from. DefaultNormalizer (normalize.go) writes
// the filing label, collapsing per-entity paths such as /users/123 into
// the endpoint /users/{id}. classify (outcome.go) applies the delivery
// stamp: success, provider_error, timeout or network.
//
// The analogy annotates the design; every guarantee above is stated in
// technical terms and holds independently of it.
package wirelog
