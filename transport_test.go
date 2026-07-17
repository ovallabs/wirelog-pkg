package wirelog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestTransport mints a transport over next with a buffered record channel
// and a fresh drop counter for assertions.
func newTestTransport(next http.RoundTripper, cfg Config, instanceConsumer string, buf int) (*transport, chan record, *atomic.Int64) {
	ch := make(chan record, buf)
	var dropped atomic.Int64
	return newTransport(next, newCapture(cfg, instanceConsumer), ch, &dropped), ch, &dropped
}

// recvRecord returns the next enqueued record, failing the test after 1s.
func recvRecord(t *testing.T, ch chan record) record {
	t.Helper()
	select {
	case rec := <-ch:
		return rec
	case <-time.After(time.Second):
		t.Fatal("no record enqueued within 1s")
		return record{}
	}
}

// TestRoundTripFullRecordOnSuccess drives a real httptest call and verifies
// every record field, with masking applied before enqueue (B1) while the
// caller still receives unmasked bytes.
func TestRoundTripFullRecordOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		w.Header().Set("Set-Cookie", "session=1")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"transfer_token":"tk_1","receiver_account":"+237670000001"}`))
	}))
	defer srv.Close()

	tr, ch, _ := newTestTransport(http.DefaultTransport, NewConfig("magma", WithCaptureBodies(true)), "magma-demo", 8)
	client := &http.Client{Transport: tr}

	ctx := WithRef(context.Background(), "ref-1")
	ctx = WithOperation(ctx, "payout.execute")
	ctx = WithIdempotencyKey(ctx, "idem-1")
	reqBody := `{"msisdn":"+237670000001","amount":100}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/transfers/12345", strings.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "+237670000001") {
		t.Error("caller must receive the unmasked response body")
	}

	rec := recvRecord(t, ch)
	if rec.provider != "magma" || rec.consumer != "magma-demo" {
		t.Errorf("provider/consumer = %q/%q", rec.provider, rec.consumer)
	}
	if rec.operation != "payout.execute" || rec.internalRef != "ref-1" || rec.idempotencyKey != "idem-1" {
		t.Errorf("annotations lost: %+v", rec)
	}
	if rec.path != "/v1/transfers/12345" || rec.endpoint != "/v1/transfers/{id}" {
		t.Errorf("path/endpoint = %q/%q", rec.path, rec.endpoint)
	}
	if rec.method != http.MethodPost || rec.statusCode != 200 || rec.outcome != outcomeSuccess {
		t.Errorf("method/status/outcome = %q/%d/%q", rec.method, rec.statusCode, rec.outcome)
	}
	if rec.latencyMS < 10 {
		t.Errorf("latencyMS = %d, want >= 10 (handler sleeps 10ms)", rec.latencyMS)
	}
	if rec.requestSize != int64(len(reqBody)) {
		t.Errorf("requestSize = %d, want %d", rec.requestSize, len(reqBody))
	}
	if rec.responseSize != int64(len(body)) {
		t.Errorf("responseSize = %d, want %d actual bytes", rec.responseSize, len(body))
	}
	if got := rec.requestHeaders["Authorization"]; !reflect.DeepEqual(got, []string{maskedValue}) {
		t.Errorf("Authorization = %v, want masked before enqueue (B1)", got)
	}
	if got := rec.responseHeaders["Set-Cookie"]; !reflect.DeepEqual(got, []string{maskedValue}) {
		t.Errorf("Set-Cookie = %v, want masked", got)
	}
	var rb, sb map[string]any
	if err := json.Unmarshal(rec.requestBody, &rb); err != nil {
		t.Fatalf("requestBody invalid JSON: %v", err)
	}
	if rb["msisdn"] != maskedValue {
		t.Errorf("requestBody msisdn = %v, want masked before enqueue (B1)", rb["msisdn"])
	}
	if err := json.Unmarshal(rec.responseBody, &sb); err != nil {
		t.Fatalf("responseBody invalid JSON: %v", err)
	}
	if sb["receiver_account"] != maskedValue || sb["transfer_token"] != "tk_1" {
		t.Errorf("responseBody masking wrong: %v", sb)
	}
}

// TestRoundTripExcludePathsProduceNoRecord enforces B8: excluded paths pass
// through untouched and enqueue nothing.
func TestRoundTripExcludePathsProduceNoRecord(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	tr, ch, _ := newTestTransport(http.DefaultTransport, NewConfig("magma", WithCaptureBodies(true)), "", 8)
	resp, err := (&http.Client{Transport: tr}).Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "ok" {
		t.Errorf("caller body = %q, want ok", body)
	}
	select {
	case rec := <-ch:
		t.Fatalf("excluded path produced a record: %+v", rec)
	default:
	}
}

// TestRoundTripSkipBodyPathsRecordMetadataOnly checks skip-body paths record
// metadata, sizes, and masked headers but never bodies (B8/B9).
func TestRoundTripSkipBodyPathsRecordMetadataOnly(t *testing.T) {
	respPayload := `{"access_token":"secret-token"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respPayload))
	}))
	defer srv.Close()

	tr, ch, _ := newTestTransport(http.DefaultTransport, NewConfig("magma", WithCaptureBodies(true)), "", 8)
	reqBody := `{"client_secret":"abc"}`
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/oauth/token", strings.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Basic xyz")
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != respPayload {
		t.Errorf("caller body = %q, want passthrough %q", body, respPayload)
	}

	rec := recvRecord(t, ch)
	if rec.requestBody != nil || rec.responseBody != nil {
		t.Errorf("bodies = %s/%s, want nil on skip-body path", rec.requestBody, rec.responseBody)
	}
	if rec.requestSize != int64(len(reqBody)) {
		t.Errorf("requestSize = %d, want ContentLength %d", rec.requestSize, len(reqBody))
	}
	if rec.responseSize != int64(len(respPayload)) {
		t.Errorf("responseSize = %d, want Content-Length %d", rec.responseSize, len(respPayload))
	}
	if got := rec.requestHeaders["Authorization"]; !reflect.DeepEqual(got, []string{maskedValue}) {
		t.Errorf("Authorization = %v, want masked headers even on skip-body path", got)
	}
	if rec.statusCode != 200 || rec.outcome != outcomeSuccess {
		t.Errorf("status/outcome = %d/%q", rec.statusCode, rec.outcome)
	}
}

// TestRoundTripSizesWithCaptureBodiesFalse checks sizes still record from
// length headers when bodies are never read (B9).
func TestRoundTripSizesWithCaptureBodiesFalse(t *testing.T) {
	respPayload := `{"balance":100}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(respPayload))
	}))
	defer srv.Close()

	tr, ch, _ := newTestTransport(http.DefaultTransport, NewConfig("magma"), "", 8)
	reqBody := `{"q":1}`
	resp, err := (&http.Client{Transport: tr}).Post(srv.URL+"/partner/balance", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	rec := recvRecord(t, ch)
	if rec.requestBody != nil || rec.responseBody != nil {
		t.Error("bodies must be nil with CaptureBodies=false")
	}
	if rec.requestSize != int64(len(reqBody)) || rec.responseSize != int64(len(respPayload)) {
		t.Errorf("sizes = %d/%d, want %d/%d from length headers (B9)",
			rec.requestSize, rec.responseSize, len(reqBody), len(respPayload))
	}
}

// TestRoundTripBodyIntactBeyondMaxBodyBytes enforces B3 over the wire: a
// response larger than MaxBodyBytes reaches the caller byte-for-byte while
// the stored copy truncates.
func TestRoundTripBodyIntactBeyondMaxBodyBytes(t *testing.T) {
	big := []byte(`{"data":"` + strings.Repeat("x", 100_000) + `"}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(big)
	}))
	defer srv.Close()

	cfg := NewConfig("magma", WithCaptureBodies(true))
	cfg.MaxBodyBytes = 1024
	tr, ch, _ := newTestTransport(http.DefaultTransport, cfg, "", 8)
	resp, err := (&http.Client{Transport: tr}).Get(srv.URL + "/big")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, big) {
		t.Fatalf("caller received %d bytes, want the original %d byte-for-byte (B3)", len(body), len(big))
	}

	rec := recvRecord(t, ch)
	var v map[string]any
	if err := json.Unmarshal(rec.responseBody, &v); err != nil {
		t.Fatalf("stored body invalid JSON: %v", err)
	}
	if v["_truncated"] != true {
		t.Errorf("stored body _truncated = %v, want true", v["_truncated"])
	}
	if rec.responseSize != int64(len(big)) {
		t.Errorf("responseSize = %d, want full %d despite truncated capture", rec.responseSize, len(big))
	}
}

// staticRoundTripper returns fixed values so identity can be asserted.
type staticRoundTripper struct {
	resp *http.Response
	err  error
}

// RoundTrip returns the canned response and error unchanged.
func (s *staticRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return s.resp, s.err
}

// TestRoundTripReturnsWrappedResponseIdentity enforces B2: the caller gets
// the wrapped transport's *http.Response pointer with only Body swapped.
func TestRoundTripReturnsWrappedResponseIdentity(t *testing.T) {
	inner := &http.Response{
		StatusCode: 422,
		Header:     http.Header{"X-Reason": {"validation"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":"bad"}`)),
	}
	tr, ch, _ := newTestTransport(&staticRoundTripper{resp: inner}, NewConfig("magma", WithCaptureBodies(true)), "", 8)
	req, err := http.NewRequest(http.MethodPost, "http://magma/v1/transfers", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if resp != inner {
		t.Error("must return the wrapped transport's *http.Response, only Body swapped (B2)")
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"error":"bad"}` {
		t.Errorf("caller body = %q, want identical bytes", body)
	}
	rec := recvRecord(t, ch)
	if rec.outcome != outcomeProviderError || rec.statusCode != 422 {
		t.Errorf("outcome/status = %q/%d, want provider_error/422", rec.outcome, rec.statusCode)
	}
}

// TestRoundTripReturnsWrappedErrorIdentity enforces B2 on the error path:
// the wrapped transport's error is returned by identity, and the failure is
// still recorded.
func TestRoundTripReturnsWrappedErrorIdentity(t *testing.T) {
	sentinel := errors.New("dial tcp: connection refused")
	tr, ch, _ := newTestTransport(&staticRoundTripper{err: sentinel}, NewConfig("magma", WithCaptureBodies(true)), "", 8)
	req, err := http.NewRequest(http.MethodGet, "http://magma/partner/balance", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, gotErr := tr.RoundTrip(req)
	if resp != nil {
		t.Errorf("resp = %v, want nil", resp)
	}
	if gotErr != sentinel { //nolint:errorlint // identity is the contract (B2)
		t.Errorf("err = %v, want the wrapped transport's error unmodified", gotErr)
	}
	rec := recvRecord(t, ch)
	if rec.outcome != outcomeNetwork || rec.callErr != sentinel.Error() || rec.statusCode != 0 {
		t.Errorf("record = %+v, want network outcome with error text", rec)
	}
}

// TestRoundTripFullBufferDropsAndCounts enforces B2's non-blocking enqueue:
// a full buffer drops and counts the record while the call still succeeds.
func TestRoundTripFullBufferDropsAndCounts(t *testing.T) {
	tr, _, dropped := newTestTransport(
		&staticRoundTripper{resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}},
		NewConfig("magma"), "", 0) // unbuffered channel, no reader: enqueue must drop
	req, err := http.NewRequest(http.MethodGet, "http://magma/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, rtErr := tr.RoundTrip(req)
	if rtErr != nil || resp == nil || resp.StatusCode != 200 {
		t.Fatalf("call must succeed when the buffer is full: resp=%v err=%v", resp, rtErr)
	}
	if got := dropped.Load(); got != 1 {
		t.Errorf("dropped = %d, want 1", got)
	}
}

// TestTransportConcurrentUse hammers one transport from many goroutines; the
// race detector run enforces B17.
func TestTransportConcurrentUse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	const goroutines, perG = 20, 10
	tr, ch, dropped := newTestTransport(http.DefaultTransport, NewConfig("magma", WithCaptureBodies(true)), "", goroutines*perG)
	client := &http.Client{Transport: tr}
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range perG {
				resp, err := client.Get(srv.URL + "/v1/things/42")
				if err != nil {
					t.Error(err)
					return
				}
				_, _ = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
			}
		})
	}
	wg.Wait()
	if got := len(ch) + int(dropped.Load()); got != goroutines*perG {
		t.Errorf("records+dropped = %d, want %d", got, goroutines*perG)
	}
}
