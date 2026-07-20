package wirelog

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
