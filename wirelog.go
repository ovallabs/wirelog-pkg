// wirelog.go — the mail room's front office: New wires the queue, writer and
// pool; HTTPClient mints a capturing client for one provider Config.

package wirelog

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Wirelog owns the record queue, the single writer goroutine, and the pgx pool.
type Wirelog struct {
	pool      *pgxpool.Pool
	ch        chan record
	w         *writer
	opts      options
	dropped   atomic.Int64
	closeOnce sync.Once
}

// New connects to Postgres, optionally applies the embedded DDL, and starts
// the writer goroutine.
func New(ctx context.Context, dbURL string, opts ...Option) (*Wirelog, error) {
	o := defaultOptions()
	for _, opt := range opts {
		opt(&o)
	}
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, err
	}
	if o.autoMigrate {
		if err := migrate(ctx, pool); err != nil {
			pool.Close()
			return nil, err
		}
	}
	wl := &Wirelog{pool: pool, ch: make(chan record, o.buffer), opts: o}
	wl.w = newWriter(wl.ch, &pgInserter{pool: pool}, o.batchSize, o.flushInterval, o.logger, &wl.dropped)
	go wl.w.run()
	return wl, nil
}

// Close drains the queue, performs a final flush, then closes the pool (B13).
// Safe on a nil receiver — matching HTTPClient's degradation contract (B11) —
// and safe to call more than once.
func (wl *Wirelog) Close() {
	if wl == nil {
		return
	}
	wl.closeOnce.Do(func() {
		wl.w.closeAndDrain()
		wl.pool.Close()
	})
}

// Dropped reports records that never reached the database: non-blocking
// enqueue drops plus insert-failure batch drops (B2, Q4 ruling). A nil
// receiver reports 0.
func (wl *Wirelog) Dropped() int64 {
	if wl == nil {
		return 0
	}
	return wl.dropped.Load()
}

// HTTPClient mints a client whose transport chain is wirelog → otelhttp →
// http.DefaultTransport (B12), normalizing cfg at mint. On a nil receiver it
// returns a plain otelhttp client, so services that boot despite a wirelog
// init failure degrade silently (B11).
func (wl *Wirelog) HTTPClient(cfg Config) *http.Client {
	base := otelhttp.NewTransport(http.DefaultTransport)
	if wl == nil {
		return &http.Client{Transport: base}
	}
	return &http.Client{Transport: newTransport(base, newCapture(cfg, wl.opts.consumer), wl.ch, &wl.dropped)}
}
