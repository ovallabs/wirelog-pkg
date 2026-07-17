// record.go — the photocopy: the persisted record and buildRecord, which
// assembles one masked copy from an observed exchange.

package wirelog

import (
	"net/http"
	"time"
)

// record mirrors one provider_api_logs row; jsonb columns hold masked copies.
type record struct {
	provider        string
	consumer        string
	operation       string
	endpoint        string
	path            string
	method          string
	remoteIP        string
	statusCode      int
	outcome         string
	latencyMS       int64
	requestSize     int64
	responseSize    int64
	internalRef     string
	idempotencyKey  string
	requestHeaders  map[string][]string
	requestBody     []byte
	responseHeaders map[string][]string
	responseBody    []byte
	callErr         string
	tags            map[string]any
}

// capture is the minted, read-only state shared by every request.
type capture struct {
	cfg      Config
	deny     map[string]struct{}
	fields   map[string]struct{}
	consumer string // instance default, lowest consumer precedence
}

// newCapture normalizes cfg at mint: non-positive MaxBodyBytes and nil
// PathNormalizer get defaults; empty MaskFields deliberately stays empty
// because literal Config construction opts out of the shared defaults.
func newCapture(cfg Config, instanceConsumer string) *capture {
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = defaultMaxBodyBytes
	}
	if cfg.PathNormalizer == nil {
		cfg.PathNormalizer = DefaultNormalizer
	}
	return &capture{
		cfg:      cfg,
		deny:     denyHeaderSet(cfg.DenyHeaders),
		fields:   maskFieldSet(cfg.MaskFields),
		consumer: instanceConsumer,
	}
}

// exchange carries everything RoundTrip observed for one call.
type exchange struct {
	req      *http.Request
	resp     *http.Response // nil on the error path
	err      error
	latency  time.Duration
	reqBody  []byte // raw request snapshot; nil when bodies are not captured
	respBody []byte // full response bytes; nil when bodies are not captured
	remoteIP string // resolved provider IP; "" when no connection was made
}

// buildRecord assembles the persisted record; masking happens here, inside
// RoundTrip and before enqueue.
func (c *capture) buildRecord(x exchange) record {
	ctx := x.req.Context()
	rec := record{
		provider:       c.cfg.Provider,
		consumer:       resolveConsumer(ctx, c.cfg.Consumer, c.consumer),
		operation:      string(operationFrom(ctx)),
		endpoint:       c.cfg.PathNormalizer(x.req.URL.Path),
		path:           x.req.URL.Path,
		method:         x.req.Method,
		remoteIP:       x.remoteIP,
		outcome:        classify(x.resp, x.err),
		latencyMS:      x.latency.Milliseconds(),
		requestSize:    requestSize(x.req, x.reqBody),
		responseSize:   responseSize(x.resp, x.respBody),
		internalRef:    refFrom(ctx),
		idempotencyKey: idempotencyKeyFrom(ctx),
		requestHeaders: maskHeaders(x.req.Header, c.deny),
		requestBody:    maskBody(x.reqBody, c.cfg.MaxBodyBytes, c.fields, c.cfg.Masker, x.req.Header.Get("Content-Type")),
		tags:           tagsFrom(ctx),
	}
	if x.resp != nil {
		rec.statusCode = x.resp.StatusCode
		rec.responseHeaders = maskHeaders(x.resp.Header, c.deny)
		rec.responseBody = maskBody(x.respBody, c.cfg.MaxBodyBytes, c.fields, c.cfg.Masker, x.resp.Header.Get("Content-Type"))
	}
	if x.err != nil {
		rec.callErr = x.err.Error()
	}
	return rec
}

// requestSize prefers actual snapshot bytes and falls back to
// req.ContentLength when the body was not read; 0 when unknown.
func requestSize(req *http.Request, snapshot []byte) int64 {
	if snapshot != nil {
		return int64(len(snapshot))
	}
	if req.ContentLength > 0 {
		return req.ContentLength
	}
	return 0
}

// responseSize prefers actual bytes read and falls back to Content-Length
// when the body was not read; 0 when unknown or chunked.
func responseSize(resp *http.Response, body []byte) int64 {
	if body != nil {
		return int64(len(body))
	}
	if resp != nil && resp.ContentLength > 0 {
		return resp.ContentLength
	}
	return 0
}
