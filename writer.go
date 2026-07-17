// writer.go — the mail van: a single goroutine that batches records and
// delivers them to Postgres, flushing at batch size or interval.

package wirelog

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// insertTimeout bounds each batch INSERT (B13).
const insertTimeout = 10 * time.Second

// inserter is the single seam between the writer and Postgres; tests fake it
// in-package, so no mock dependency is needed.
type inserter interface {
	insertBatch(ctx context.Context, recs []record) error
}

// writer owns the only goroutine that touches the database (B13).
type writer struct {
	ch       <-chan record
	ins      inserter
	batch    int
	interval time.Duration
	log      Logger
	dropped  *atomic.Int64
	stop     chan struct{}
	done     chan struct{}
}

// newWriter builds a writer over the record channel; the caller starts run in
// its own goroutine.
func newWriter(ch <-chan record, ins inserter, batch int, interval time.Duration, log Logger, dropped *atomic.Int64) *writer {
	return &writer{
		ch: ch, ins: ins, batch: batch, interval: interval, log: log, dropped: dropped,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
}

// run loops until closeAndDrain is called, flushing at batch size or on the
// interval ticker.
func (w *writer) run() {
	defer close(w.done)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	buf := make([]record, 0, w.batch)
	for {
		select {
		case rec := <-w.ch:
			buf = append(buf, rec)
			if len(buf) >= w.batch {
				w.flush(&buf)
			}
		case <-ticker.C:
			w.flush(&buf)
		case <-w.stop:
			// Drain what is already queued, flush once more, then exit — the
			// van finishes its route before the depot shuts (B13).
			for {
				select {
				case rec := <-w.ch:
					buf = append(buf, rec)
					if len(buf) >= w.batch {
						w.flush(&buf)
					}
				default:
					w.flush(&buf)
					return
				}
			}
		}
	}
}

// flush delivers buf as one batch INSERT and resets it. A failed batch is
// dropped after exactly one log line, adding its length to the drop counter
// (B2, Q4 ruling); errors never propagate.
func (w *writer) flush(buf *[]record) {
	if len(*buf) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), insertTimeout)
	err := w.ins.insertBatch(ctx, *buf)
	cancel()
	if err != nil {
		w.log.Printf("wirelog: batch insert failed, dropping %d records: %v", len(*buf), err)
		w.dropped.Add(int64(len(*buf)))
	}
	*buf = (*buf)[:0]
}

// closeAndDrain signals run to drain and waits for the goroutine to exit.
// The record channel is never closed, so a late enqueue can only drop, never
// panic.
func (w *writer) closeAndDrain() {
	close(w.stop)
	<-w.done
}

// pgInserter delivers batches to Postgres with one multi-row INSERT.
type pgInserter struct{ pool *pgxpool.Pool }

// insertBatch executes the rendered multi-row INSERT for one batch.
func (p *pgInserter) insertBatch(ctx context.Context, records []record) error {
	insertSQL, args := buildInsert(records)
	_, err := p.pool.Exec(ctx, insertSQL, args...)
	return err
}

// insertColumns lists the target columns in record-field order.
const insertColumns = "provider, consumer, operation, endpoint, path, method, " +
	"remote_ip, status_code, outcome, latency_ms, request_size, response_size, " +
	"internal_ref, idempotency_key, request_headers, request_body, " +
	"response_headers, response_body, error, tags"

const colCount = 20

// maxBatchSize keeps one multi-row INSERT under Postgres's 65535
// bind-parameter statement limit (65535 / colCount ≈ 3276) with headroom.
const maxBatchSize = 3000

// jsonbCols marks 1-based placeholder positions cast with ::jsonb so the
// driver never has to guess the parameter type.
var jsonbCols = map[int]bool{15: true, 16: true, 17: true, 18: true, 20: true}

// buildInsert renders one multi-row INSERT using numbered placeholders only;
// record values are never interpolated into the SQL (B13).
func buildInsert(records []record) (string, []any) {
	var query strings.Builder
	query.WriteString("insert into provider_api_logs (" + insertColumns + ") values ")
	args := make([]any, 0, len(records)*colCount)
	for i, rec := range records {
		if i > 0 {
			query.WriteByte(',')
		}
		query.WriteByte('(')
		for j := 1; j <= colCount; j++ {
			if j > 1 {
				query.WriteByte(',')
			}
			fmt.Fprintf(&query, "$%d", i*colCount+j)
			if jsonbCols[j] {
				query.WriteString("::jsonb")
			}
		}
		query.WriteByte(')')
		args = append(args,
			rec.provider, rec.consumer, rec.operation, rec.endpoint, rec.path, rec.method,
			nullText(rec.remoteIP),
			nullInt(rec.statusCode), rec.outcome, rec.latencyMS, rec.requestSize, rec.responseSize,
			nullText(rec.internalRef), nullText(rec.idempotencyKey),
			jsonHeaders(rec.requestHeaders), jsonBody(rec.requestBody),
			jsonHeaders(rec.responseHeaders), jsonBody(rec.responseBody),
			nullText(rec.callErr), jsonTags(rec.tags),
		)
	}
	return query.String(), args
}

// nullText maps the empty string to SQL NULL for nullable text columns (B15).
func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullInt maps 0 to SQL NULL for status_code (B15).
func nullInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

// jsonBody passes already-masked JSON bytes as a jsonb string param; empty
// means NULL (B15).
func jsonBody(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// jsonHeaders marshals a header map for its jsonb param; nil means NULL (B15).
func jsonHeaders(headers map[string][]string) any {
	if headers == nil {
		return nil
	}
	encoded, err := json.Marshal(headers)
	if err != nil {
		return nil
	}
	return string(encoded)
}

// jsonTags marshals tags for their jsonb param; nil maps and unmarshalable
// values (a jsonb column must receive valid JSON or NULL, B4) become NULL.
func jsonTags(tags map[string]any) any {
	if tags == nil {
		return nil
	}
	encoded, err := json.Marshal(tags)
	if err != nil {
		return nil
	}
	return string(encoded)
}
