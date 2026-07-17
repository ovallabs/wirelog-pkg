// transport.go — the mail-room window: the http.RoundTripper every outbound
// request passes through, recorded without altering what the caller sends or
// receives.

package wirelog

import (
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// transport captures each exchange and forwards it untouched (B2). All its
// fields are set at mint and never written afterwards, so one instance is
// safe for concurrent use (B17).
type transport struct {
	next http.RoundTripper
	*capture
	ch      chan<- record
	dropped *atomic.Int64
}

// newTransport wraps next with capture state, the record queue, and the drop counter.
func newTransport(next http.RoundTripper, c *capture, ch chan<- record, dropped *atomic.Int64) *transport {
	return &transport{next: next, capture: c, ch: ch, dropped: dropped}
}

// RoundTrip implements http.RoundTripper. It returns the wrapped transport's
// response and error unmodified, with only resp.Body swapped for a reader
// yielding identical bytes (B2/B3).
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	path := req.URL.Path
	// B8: excluded paths short-circuit before ANY work, including timing.
	if matchAny(path, t.cfg.ExcludePaths) {
		return t.next.RoundTrip(req)
	}
	captureBodies := t.cfg.CaptureBodies && !matchAny(path, t.cfg.SkipBodyPaths)

	var reqBody []byte
	if captureBodies {
		reqBody = snapshotRequestBody(req)
	}

	start := time.Now()
	resp, err := t.next.RoundTrip(req)
	latency := time.Since(start)

	var respBody []byte
	if captureBodies && err == nil && resp != nil && resp.Body != nil {
		full, readErr := copyBody(resp.Body)
		swapBody(resp, full, readErr)
		respBody = full
	}

	t.enqueueRecord(exchange{
		req: req, resp: resp, err: err, latency: latency,
		reqBody: reqBody, respBody: respBody,
	})
	return resp, err
}

// enqueueRecord builds and enqueues the record for one exchange. It recovers
// from panicking user callbacks (Masker, PathNormalizer) so capture can never
// fail the provider call (B2); the abandoned record is counted as dropped.
func (t *transport) enqueueRecord(x exchange) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.dropped.Add(1)
		}
	}()
	rec := t.buildRecord(x)
	// B2: enqueue never blocks — a full buffer drops and counts the record;
	// the letter itself always goes out.
	select {
	case t.ch <- rec:
	default:
		t.dropped.Add(1)
	}
}

// matchAny reports whether any non-empty needle is a substring of path;
// matching never considers the query string (B8).
func matchAny(path string, needles []string) bool {
	for _, n := range needles {
		if n != "" && strings.Contains(path, n) {
			return true
		}
	}
	return false
}
