package wirelog

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
)

// closeTracker wraps a reader and records whether Close was called.
type closeTracker struct {
	io.Reader
	closed bool
}

// Close records the call so tests can assert the body was closed.
func (c *closeTracker) Close() error {
	c.closed = true
	return nil
}

// TestSnapshotRequestBodyLeavesOriginalReadable enforces B3: snapshots come
// from GetBody only, leaving req.Body untouched and GetBody reusable.
func TestSnapshotRequestBodyLeavesOriginalReadable(t *testing.T) {
	payload := `{"msisdn":"+237670000001","amount":100}`
	req, err := http.NewRequest(http.MethodPost, "http://magma/v1/transfers", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	snap := snapshotRequestBody(req)
	if string(snap) != payload {
		t.Errorf("snapshot = %q, want %q", snap, payload)
	}
	got, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Errorf("req.Body after snapshot = %q, want untouched %q", got, payload)
	}
	if again := snapshotRequestBody(req); string(again) != payload {
		t.Errorf("second snapshot = %q, want %q (GetBody must stay reusable)", again, payload)
	}
}

// TestSnapshotRequestBodyNilGetBody checks a nil GetBody yields no snapshot
// and req.Body is never consumed.
func TestSnapshotRequestBodyNilGetBody(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://magma/v1/transfers", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Body = io.NopCloser(strings.NewReader("data"))
	req.GetBody = nil
	req.ContentLength = 4
	if snap := snapshotRequestBody(req); snap != nil {
		t.Errorf("snapshot = %q, want nil when GetBody is nil", snap)
	}
	got, _ := io.ReadAll(req.Body)
	if string(got) != "data" {
		t.Errorf("req.Body was consumed: %q, want data", got)
	}
}

// TestSnapshotRequestBodyNoBody checks a bodyless request snapshots to nil.
func TestSnapshotRequestBodyNoBody(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://magma/partner/balance", nil)
	if err != nil {
		t.Fatal(err)
	}
	if snap := snapshotRequestBody(req); snap != nil {
		t.Errorf("snapshot = %q, want nil for bodyless request", snap)
	}
}

// TestCopyBodyReadsAllAndCloses checks the eager copy returns every byte and
// closes the original body.
func TestCopyBodyReadsAllAndCloses(t *testing.T) {
	src := &closeTracker{Reader: strings.NewReader("response payload")}
	full, err := copyBody(src)
	if err != nil {
		t.Fatalf("copyBody error: %v", err)
	}
	if string(full) != "response payload" {
		t.Errorf("full = %q, want response payload", full)
	}
	if !src.closed {
		t.Error("copyBody must close the original body")
	}
}

// TestCopyBodyReadErrorReplayedToCaller checks a mid-stream read error is
// replayed to the caller after the partial bytes, preserving what the caller
// would have observed reading the wire directly.
func TestCopyBodyReadErrorReplayedToCaller(t *testing.T) {
	sentinel := errors.New("connection reset")
	src := &closeTracker{Reader: io.MultiReader(strings.NewReader("partial"), iotest.ErrReader(sentinel))}
	full, err := copyBody(src)
	if !errors.Is(err, sentinel) {
		t.Fatalf("copyBody error = %v, want sentinel", err)
	}
	if string(full) != "partial" {
		t.Errorf("full = %q, want partial bytes", full)
	}
	if !src.closed {
		t.Error("copyBody must close the original body even on error")
	}

	resp := &http.Response{}
	swapBody(resp, full, err)
	got, readErr := io.ReadAll(resp.Body)
	if string(got) != "partial" {
		t.Errorf("caller read %q, want partial", got)
	}
	if !errors.Is(readErr, sentinel) {
		t.Errorf("caller read error = %v, want the original sentinel", readErr)
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("replay body Close = %v, want nil", err)
	}
}

// TestSwapBodyCallerGetsIdenticalBytesWhileCaptureTruncates enforces B3
// end-to-end: the caller reads the full body byte-for-byte while the stored
// copy is truncated and marked.
func TestSwapBodyCallerGetsIdenticalBytesWhileCaptureTruncates(t *testing.T) {
	big := []byte(`{"data":"` + strings.Repeat("x", 100_000) + `"}`)
	full, err := copyBody(io.NopCloser(bytes.NewReader(big)))
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{}
	swapBody(resp, full, nil)
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, big) {
		t.Fatalf("caller received %d bytes, want the original %d byte-for-byte", len(got), len(big))
	}

	stored := maskBody(full, 1024, maskFieldSet(nil), nil)
	var v map[string]any
	if err := json.Unmarshal(stored, &v); err != nil {
		t.Fatalf("stored copy is not valid JSON: %v", err)
	}
	if v["_truncated"] != true {
		t.Errorf("stored copy _truncated = %v, want true", v["_truncated"])
	}
}
