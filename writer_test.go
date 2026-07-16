package wirelog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// pgInserter must satisfy the seam the writer runs against.
var _ inserter = (*pgInserter)(nil)

// recordingInserter is the in-package fake behind the inserter seam (Q8).
type recordingInserter struct {
	mu      sync.Mutex
	batches [][]record
	err     error
}

func (r *recordingInserter) insertBatch(_ context.Context, recs []record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches = append(r.batches, append([]record(nil), recs...))
	return r.err
}

func (r *recordingInserter) batchSizes() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	sizes := make([]int, len(r.batches))
	for i, b := range r.batches {
		sizes[i] = len(b)
	}
	return sizes
}

type recordingLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *recordingLogger) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func startWriter(ins inserter, batch int, interval time.Duration, log Logger) (chan record, *writer, *atomic.Int64) {
	ch := make(chan record, 256)
	var dropped atomic.Int64
	w := newWriter(ch, ins, batch, interval, log, &dropped)
	go w.run()
	return ch, w, &dropped
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}

func TestWriterFlushesAtBatchSize(t *testing.T) {
	ins := &recordingInserter{}
	ch, w, _ := startWriter(ins, 10, time.Hour, nopLogger{})
	for i := range 25 {
		ch <- record{provider: "magma", outcome: outcomeSuccess, latencyMS: int64(i)}
	}
	w.closeAndDrain()
	sizes := ins.batchSizes()
	if len(sizes) != 3 || sizes[0] != 10 || sizes[1] != 10 || sizes[2] != 5 {
		t.Fatalf("batch sizes = %v, want [10 10 5] (ceil(25/10) inserts)", sizes)
	}
}

func TestWriterFlushesPartialBatchOnInterval(t *testing.T) {
	ins := &recordingInserter{}
	ch, w, _ := startWriter(ins, 100, 20*time.Millisecond, nopLogger{})
	defer w.closeAndDrain()
	for range 3 {
		ch <- record{provider: "magma", outcome: outcomeSuccess}
	}
	waitFor(t, func() bool { return len(ins.batchSizes()) >= 1 }, "interval flush never happened")
	if sizes := ins.batchSizes(); sizes[0] != 3 {
		t.Fatalf("interval flush size = %d, want 3", sizes[0])
	}
}

func TestWriterCloseDrainsAndFlushesRemainder(t *testing.T) {
	ins := &recordingInserter{}
	ch, w, _ := startWriter(ins, 100, time.Hour, nopLogger{})
	for range 7 {
		ch <- record{provider: "magma", outcome: outcomeSuccess}
	}
	w.closeAndDrain() // returning proves the goroutine exited (B13)
	if sizes := ins.batchSizes(); len(sizes) != 1 || sizes[0] != 7 {
		t.Fatalf("batches after close = %v, want [7]", sizes)
	}
	select {
	case <-w.done:
	default:
		t.Fatal("writer goroutine still running after closeAndDrain")
	}
}

func TestWriterInsertFailureDropsBatchOnce(t *testing.T) {
	ins := &recordingInserter{err: errors.New("connection lost")}
	log := &recordingLogger{}
	ch, w, dropped := startWriter(ins, 4, time.Hour, log)
	for range 4 {
		ch <- record{provider: "magma", outcome: outcomeSuccess}
	}
	waitFor(t, func() bool { return dropped.Load() == 4 }, "insert failure never counted in Dropped (Q4)")
	w.closeAndDrain()
	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.lines) != 1 {
		t.Fatalf("log lines = %d (%v), want exactly one per failed batch", len(log.lines), log.lines)
	}
	if !strings.Contains(log.lines[0], "dropping 4 records") || !strings.Contains(log.lines[0], "connection lost") {
		t.Errorf("log line = %q, want batch size and cause", log.lines[0])
	}
}

func TestBuildInsertPlaceholdersAndNullMapping(t *testing.T) {
	full := record{
		provider: "magma", consumer: "demo", operation: "payout.execute",
		endpoint: "/v1/transfers/{id}", path: "/v1/transfers/123", method: "POST",
		statusCode: 200, outcome: outcomeSuccess, latencyMS: 42, requestSize: 10, responseSize: 20,
		internalRef: "ref-1", idempotencyKey: "idem-1",
		requestHeaders:  map[string][]string{"Content-Type": {"application/json"}},
		requestBody:     []byte(`{"a":1}`),
		responseHeaders: map[string][]string{"Content-Type": {"application/json"}},
		responseBody:    []byte(`{"b":2}`),
		callErr:         "boom",
		tags:            map[string]any{"k": "v"},
	}
	empty := record{provider: "magma", outcome: outcomeNetwork}

	sql, args := buildInsert([]record{full, empty})

	if got := strings.Count(sql, "$"); got != 2*colCount {
		t.Errorf("placeholder count = %d, want %d", got, 2*colCount)
	}
	if !strings.Contains(sql, "$38") || strings.Contains(sql, "$39") {
		t.Error("placeholders must be numbered continuously across rows")
	}
	if got := strings.Count(sql, "::jsonb"); got != 10 {
		t.Errorf("::jsonb casts = %d, want 5 per row", got)
	}
	for _, leak := range []string{"magma", "payout", "boom"} {
		if strings.Contains(sql, leak) {
			t.Errorf("SQL contains interpolated value %q; placeholders only (B13)", leak)
		}
	}
	if len(args) != 2*colCount {
		t.Fatalf("args = %d, want %d", len(args), 2*colCount)
	}

	if args[6] != 200 || args[11] != "ref-1" || args[12] != "idem-1" || args[17] != "boom" {
		t.Errorf("full row nullable args wrong: %v %v %v %v", args[6], args[11], args[12], args[17])
	}
	if args[13] != `{"Content-Type":["application/json"]}` || args[14] != `{"a":1}` || args[18] != `{"k":"v"}` {
		t.Errorf("full row jsonb args wrong: %v %v %v", args[13], args[14], args[18])
	}

	e := args[colCount:]
	if e[1] != "" || e[2] != "" || e[3] != "" || e[4] != "" || e[5] != "" {
		t.Errorf("non-nullable text columns must keep '' defaults, got %v", e[1:6])
	}
	for _, idx := range []int{6, 11, 12, 13, 14, 15, 16, 17, 18} {
		if e[idx] != nil {
			t.Errorf("empty row arg %d = %v, want SQL NULL (B15)", idx, e[idx])
		}
	}
	if e[8] != int64(0) || e[9] != int64(0) || e[10] != int64(0) {
		t.Errorf("latency/sizes must stay 0, not NULL: %v %v %v", e[8], e[9], e[10])
	}
}

func TestJSONTagsUnmarshalableBecomesNull(t *testing.T) {
	if got := jsonTags(map[string]any{"bad": make(chan int)}); got != nil {
		t.Errorf("jsonTags(chan) = %v, want nil (jsonb gets valid JSON or NULL)", got)
	}
	if got := jsonTags(nil); got != nil {
		t.Errorf("jsonTags(nil) = %v, want nil", got)
	}
}
