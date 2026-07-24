// wirelog.go — the mail room's front office: New wires the queue, writer and
// pool; WrapTransport and HTTPClient mint capturing transports for one
// provider Config.

package wirelog

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// EnvDatabaseURL is the conventional environment variable holding the wirelog Postgres DSN.
const EnvDatabaseURL = "WIRELOG_DATABASE_URL"

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

// Close drains the queue, performs a final flush, then closes the pool.
// Safe on a nil receiver — matching HTTPClient's degradation contract —
// and safe to call more than once.
func (wl *Wirelog) Close() {
	if wl == nil {
		return
	}
	wl.closeOnce.Do(func() {
		// guard each field: a zero-value Wirelog never started a writer or pool
		if wl.w != nil {
			wl.w.closeAndDrain()
		}
		if wl.pool != nil {
			wl.pool.Close()
		}
	})
}

// Dropped reports records that never reached the database: non-blocking
// enqueue drops plus insert-failure batch drops. A nil
// receiver reports 0.
func (wl *Wirelog) Dropped() int64 {
	if wl == nil {
		return 0
	}
	return wl.dropped.Load()
}

// WrapTransport wraps base with wirelog capture for one provider Config,
// normalizing cfg at mint. Use it when a provider builds its own transport —
// a proxy dialer or custom TLS — that HTTPClient's fixed chain would discard:
// the returned RoundTripper records every exchange, then forwards to base
// untouched. A nil base falls back to otelhttp → http.DefaultTransport. On a
// nil receiver it returns base unchanged, so a provider whose client is built
// despite a wirelog init failure keeps working without capture.
func (wl *Wirelog) WrapTransport(cfg Config, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = otelhttp.NewTransport(http.DefaultTransport)
	}
	if wl == nil {
		return base
	}
	return newTransport(base, newCapture(cfg, wl.opts.consumer), wl.ch, &wl.dropped)
}

// HTTPClient mints a client whose transport chain is wirelog → otelhttp →
// http.DefaultTransport, normalizing cfg at mint. On a nil receiver it
// returns a plain otelhttp client, so services that boot despite a wirelog
// init failure degrade silently. It is WrapTransport over the default chain;
// a provider with its own transport should call WrapTransport instead.
func (wl *Wirelog) HTTPClient(cfg Config) *http.Client {
	return &http.Client{Transport: wl.WrapTransport(cfg, otelhttp.NewTransport(http.DefaultTransport))}
}
