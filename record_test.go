package wirelog

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func TestNewCaptureNormalizesLiteralConfig(t *testing.T) {
	c := newCapture(Config{Provider: "magma"}, "")
	if c.cfg.MaxBodyBytes != 16384 {
		t.Errorf("MaxBodyBytes = %d, want normalized 16384", c.cfg.MaxBodyBytes)
	}
	if c.cfg.PathNormalizer == nil {
		t.Fatal("PathNormalizer = nil, want DefaultNormalizer")
	}
	if got := c.cfg.PathNormalizer("/users/123"); got != "/users/{id}" {
		t.Errorf("PathNormalizer(/users/123) = %q, want /users/{id}", got)
	}
	if len(c.fields) != 0 {
		t.Errorf("mask fields = %v, want empty (literal Config opts out of defaults)", c.fields)
	}
	if _, ok := c.deny["authorization"]; !ok {
		t.Error("built-in header denylist missing from minted capture")
	}
}

func TestNewCaptureDoesNotMutateCallerConfig(t *testing.T) {
	cfg := Config{Provider: "magma"}
	_ = newCapture(cfg, "")
	if cfg.MaxBodyBytes != 0 || cfg.PathNormalizer != nil {
		t.Error("newCapture mutated the caller's Config")
	}
}

func TestBuildRecordSuccessFields(t *testing.T) {
	c := newCapture(NewConfig("magma", WithCaptureBodies(true)), "inst")
	ctx := WithRef(context.Background(), "ref-9")
	ctx = WithOperation(ctx, "payout.execute")
	ctx = WithIdempotencyKey(ctx, "idem-9")
	ctx = WithTags(ctx, map[string]any{"batch": "b1"})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://magma/v1/transfers/12345?verbose=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")

	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Set-Cookie": {"s=1"}, "Content-Type": {"application/json"}},
	}
	reqBody := []byte(`{"msisdn":"+237670000001","amount":100}`)
	respBody := []byte(`{"transfer_token":"tk_1","receiver_account":"+237670000001"}`)

	rec := c.buildRecord(exchange{
		req: req, resp: resp, latency: 1500 * time.Millisecond,
		reqBody: reqBody, respBody: respBody,
	})

	if rec.provider != "magma" || rec.consumer != "inst" {
		t.Errorf("provider/consumer = %q/%q, want magma/inst", rec.provider, rec.consumer)
	}
	if rec.operation != "payout.execute" || rec.internalRef != "ref-9" || rec.idempotencyKey != "idem-9" {
		t.Errorf("ctx annotations lost: op=%q ref=%q idem=%q", rec.operation, rec.internalRef, rec.idempotencyKey)
	}
	if !reflect.DeepEqual(rec.tags, map[string]any{"batch": "b1"}) {
		t.Errorf("tags = %v, want {batch: b1}", rec.tags)
	}
	if rec.path != "/v1/transfers/12345" {
		t.Errorf("path = %q, want raw /v1/transfers/12345", rec.path)
	}
	if rec.endpoint != "/v1/transfers/{id}" {
		t.Errorf("endpoint = %q, want normalized /v1/transfers/{id}", rec.endpoint)
	}
	if rec.method != http.MethodPost || rec.statusCode != 200 || rec.outcome != outcomeSuccess {
		t.Errorf("method/status/outcome = %q/%d/%q", rec.method, rec.statusCode, rec.outcome)
	}
	if rec.latencyMS != 1500 {
		t.Errorf("latencyMS = %d, want 1500", rec.latencyMS)
	}
	if rec.requestSize != int64(len(reqBody)) || rec.responseSize != int64(len(respBody)) {
		t.Errorf("sizes = %d/%d, want actual byte counts %d/%d", rec.requestSize, rec.responseSize, len(reqBody), len(respBody))
	}
	if got := rec.requestHeaders["Authorization"]; !reflect.DeepEqual(got, []string{maskedValue}) {
		t.Errorf("Authorization = %v, want masked", got)
	}
	if got := rec.responseHeaders["Set-Cookie"]; !reflect.DeepEqual(got, []string{maskedValue}) {
		t.Errorf("Set-Cookie = %v, want masked", got)
	}
	var rb map[string]any
	if err := json.Unmarshal(rec.requestBody, &rb); err != nil {
		t.Fatalf("requestBody not valid JSON: %v", err)
	}
	if rb["msisdn"] != maskedValue || rb["amount"] != float64(100) {
		t.Errorf("requestBody masking wrong: %v", rb)
	}
	var sb map[string]any
	if err := json.Unmarshal(rec.responseBody, &sb); err != nil {
		t.Fatalf("responseBody not valid JSON: %v", err)
	}
	if sb["receiver_account"] != maskedValue || sb["transfer_token"] != "tk_1" {
		t.Errorf("responseBody masking wrong: %v", sb)
	}
	if rec.callErr != "" {
		t.Errorf("callErr = %q, want empty on success", rec.callErr)
	}
}

func TestBuildRecordErrorPath(t *testing.T) {
	c := newCapture(NewConfig("magma"), "")
	req, err := http.NewRequest(http.MethodGet, "http://magma/partner/balance", nil)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("dial tcp: connection refused")
	rec := c.buildRecord(exchange{req: req, err: sentinel, latency: 20 * time.Millisecond})
	if rec.outcome != outcomeNetwork {
		t.Errorf("outcome = %q, want network", rec.outcome)
	}
	if rec.statusCode != 0 {
		t.Errorf("statusCode = %d, want 0 with no response", rec.statusCode)
	}
	if rec.callErr != sentinel.Error() {
		t.Errorf("callErr = %q, want %q", rec.callErr, sentinel.Error())
	}
	if rec.responseHeaders != nil || rec.responseBody != nil {
		t.Error("response fields must stay nil with no response")
	}
}

func TestBuildRecordConsumerPrecedence(t *testing.T) {
	req := func(ctx context.Context) *http.Request {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://magma/x", nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	tests := []struct {
		name string
		ctx  context.Context
		cfg  string
		inst string
		want string
	}{
		{"ctx wins", WithConsumer(context.Background(), "ctx-c"), "cfg-c", "inst-c", "ctx-c"},
		{"config wins over instance", context.Background(), "cfg-c", "inst-c", "cfg-c"},
		{"instance default", context.Background(), "", "inst-c", "inst-c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newCapture(NewConfig("magma", func(cfg *Config) { cfg.Consumer = tt.cfg }), tt.inst)
			rec := c.buildRecord(exchange{req: req(tt.ctx)})
			if rec.consumer != tt.want {
				t.Fatalf("consumer = %q, want %q", rec.consumer, tt.want)
			}
		})
	}
}

func TestRequestSizeFallbacks(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "http://x", nil)
	req.ContentLength = 42
	if got := requestSize(req, []byte("abcde")); got != 5 {
		t.Errorf("with snapshot = %d, want 5 (actual bytes beat ContentLength)", got)
	}
	if got := requestSize(req, nil); got != 42 {
		t.Errorf("no snapshot = %d, want ContentLength 42", got)
	}
	req.ContentLength = -1
	if got := requestSize(req, nil); got != 0 {
		t.Errorf("unknown length = %d, want 0", got)
	}
}

func TestResponseSizeFallbacks(t *testing.T) {
	resp := &http.Response{ContentLength: 99}
	if got := responseSize(resp, []byte("abc")); got != 3 {
		t.Errorf("with body = %d, want 3 (actual bytes beat Content-Length)", got)
	}
	if got := responseSize(resp, nil); got != 99 {
		t.Errorf("no body = %d, want Content-Length 99", got)
	}
	resp.ContentLength = -1
	if got := responseSize(resp, nil); got != 0 {
		t.Errorf("chunked = %d, want 0", got)
	}
	if got := responseSize(nil, nil); got != 0 {
		t.Errorf("nil response = %d, want 0", got)
	}
}
