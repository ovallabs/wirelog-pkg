package wirelog

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// TestHTTPClientNilReceiverReturnsPlainClient checks a nil *Wirelog
// still mints a working plain otelhttp client.
func TestHTTPClientNilReceiverReturnsPlainClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var wl *Wirelog
	client := wl.HTTPClient(NewConfig("magma"))
	if client == nil {
		t.Fatal("nil receiver must still return a usable client")
	}
	if _, ok := client.Transport.(*otelhttp.Transport); !ok {
		t.Fatalf("nil-receiver transport = %T, want plain *otelhttp.Transport", client.Transport)
	}
	resp, err := client.Get(srv.URL + "/partner/balance")
	if err != nil {
		t.Fatalf("degraded client must still work: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != `{"ok":true}` {
		t.Errorf("body = %q", body)
	}
}

// TestHTTPClientChainOrder verifies by type assertion that wirelog wraps
// otelhttp wraps http.DefaultTransport.
func TestHTTPClientChainOrder(t *testing.T) {
	wl := &Wirelog{ch: make(chan record, 1), opts: defaultOptions()}
	client := wl.HTTPClient(NewConfig("magma"))
	tp, ok := client.Transport.(*transport)
	if !ok {
		t.Fatalf("outermost transport = %T, want wirelog *transport", client.Transport)
	}
	if _, ok := tp.next.(*otelhttp.Transport); !ok {
		t.Fatalf("wrapped transport = %T, want *otelhttp.Transport (chain: wirelog → otelhttp → default)", tp.next)
	}
}

// TestHTTPClientNormalizesLiteralConfigAtMint checks normalization applies when
// a literal Config reaches HTTPClient.
func TestHTTPClientNormalizesLiteralConfigAtMint(t *testing.T) {
	wl := &Wirelog{ch: make(chan record, 1), opts: defaultOptions()}
	client := wl.HTTPClient(Config{Provider: "magma"})
	tp := client.Transport.(*transport)
	if tp.cfg.MaxBodyBytes != 16384 || tp.cfg.PathNormalizer == nil {
		t.Errorf("literal Config not normalized at mint: %+v", tp.cfg)
	}
	if len(tp.fields) != 0 {
		t.Error("literal Config must keep its empty mask list")
	}
}

// TestHTTPClientEnqueueIncrementsDroppedWhenFull checks, through the
// public API, that full-buffer drops count in Dropped and calls never fail.
func TestHTTPClientEnqueueIncrementsDroppedWhenFull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	wl := &Wirelog{ch: make(chan record, 1), opts: defaultOptions()} // no writer draining
	client := wl.HTTPClient(NewConfig("magma"))
	for range 3 {
		resp, err := client.Get(srv.URL + "/x")
		if err != nil {
			t.Fatalf("calls must never fail on a full buffer: %v", err)
		}
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	if got := wl.Dropped(); got != 2 {
		t.Errorf("Dropped() = %d, want 2 (buffer of 1, three records)", got)
	}
}

// TestNilReceiverCloseAndDropped enforces the README's degraded pattern: a
// service holding a nil *Wirelog can defer Close and poll Dropped without
// panicking.
func TestNilReceiverCloseAndDropped(t *testing.T) {
	var wl *Wirelog
	wl.Close() // must not panic
	if got := wl.Dropped(); got != 0 {
		t.Errorf("nil Dropped() = %d, want 0", got)
	}
}

// TestZeroValueCloseDoesNotPanic checks Close also survives a non-nil but
// never-initialized Wirelog, whose writer and pool are nil.
func TestZeroValueCloseDoesNotPanic(t *testing.T) {
	(&Wirelog{}).Close()
}

// TestNewRejectsInvalidURL checks New fails fast on an unparseable dbURL.
func TestNewRejectsInvalidURL(t *testing.T) {
	if _, err := New(context.Background(), "://not-a-url"); err == nil {
		t.Fatal("New must fail on an unparseable database URL")
	}
}

// markerRoundTripper is a base transport that records it was reached and
// returns a canned response, standing in for a provider's own proxy transport.
type markerRoundTripper struct{ called *atomic.Bool }

// RoundTrip marks the transport reached and returns a fixed JSON response,
// populating Request and Status as a real transport would.
func (m markerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	m.called.Store(true)
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		Request:    req,
	}, nil
}

// TestWrapTransportKeepsCustomBaseAndCaptures verifies WrapTransport records
// the exchange while forwarding to the caller's own base transport, so a
// provider's proxy dialer survives instead of being replaced.
func TestWrapTransportKeepsCustomBaseAndCaptures(t *testing.T) {
	var called atomic.Bool
	base := markerRoundTripper{called: &called}
	wl := &Wirelog{ch: make(chan record, 4), opts: defaultOptions()}

	rt := wl.WrapTransport(NewConfig("magma", WithCaptureBodies(true)), base)
	tp, ok := rt.(*transport)
	if !ok {
		t.Fatalf("WrapTransport = %T, want wirelog *transport", rt)
	}
	if _, ok := tp.next.(markerRoundTripper); !ok {
		t.Fatalf("wrapped next = %T, want the supplied base (proxy transport would be lost)", tp.next)
	}

	resp, err := (&http.Client{Transport: rt}).Get("http://magma.example/v1/transfers")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if !called.Load() {
		t.Error("custom base was not reached — capture must forward to it, not replace it")
	}
	select {
	case <-wl.ch:
	default:
		t.Error("no record enqueued — capture did not run over the custom base")
	}
}

// TestWrapTransportNilReceiverReturnsBaseUnchanged checks the degradation
// contract: a nil *Wirelog hands the base back untouched so the provider
// still works without capture.
func TestWrapTransportNilReceiverReturnsBaseUnchanged(t *testing.T) {
	var wl *Wirelog
	base := otelhttp.NewTransport(http.DefaultTransport)
	if got := wl.WrapTransport(NewConfig("magma"), base); got != base {
		t.Fatalf("nil receiver must return the supplied base unchanged, got %T", got)
	}
}

// TestWrapTransportNilBaseFallsBackToOtel checks a nil base defaults to the
// otelhttp → http.DefaultTransport chain.
func TestWrapTransportNilBaseFallsBackToOtel(t *testing.T) {
	wl := &Wirelog{ch: make(chan record, 1), opts: defaultOptions()}
	rt := wl.WrapTransport(NewConfig("magma"), nil)
	tp, ok := rt.(*transport)
	if !ok {
		t.Fatalf("WrapTransport(nil base) = %T, want *transport", rt)
	}
	if _, ok := tp.next.(*otelhttp.Transport); !ok {
		t.Fatalf("nil base fallback next = %T, want *otelhttp.Transport", tp.next)
	}
}
