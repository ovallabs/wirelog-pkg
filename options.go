// options.go — the depot's standing orders: instance-level options applied
// once in New.

package wirelog

import "time"

// Option customises a Wirelog instance at construction.
type Option func(*options)

// options holds instance settings; see defaultOptions for the FRD defaults.
type options struct {
	buffer        int
	batchSize     int
	flushInterval time.Duration
	logger        Logger
	autoMigrate   bool
	consumer      string
}

func defaultOptions() options {
	return options{
		buffer:        2048,
		batchSize:     100,
		flushInterval: 2 * time.Second,
		logger:        nopLogger{},
	}
}

// WithBuffer sets the enqueue channel capacity (default 2048); non-positive
// values keep the default.
func WithBuffer(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.buffer = n
		}
	}
}

// WithBatchSize sets the writer flush batch size (default 100); non-positive
// values keep the default.
func WithBatchSize(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.batchSize = n
		}
	}
}

// WithFlushInterval sets the writer ticker interval (default 2s);
// non-positive values keep the default.
func WithFlushInterval(d time.Duration) Option {
	return func(o *options) {
		if d > 0 {
			o.flushInterval = d
		}
	}
}

// WithLogger sets the insert-failure logger (default silent no-op); nil
// keeps the default.
func WithLogger(l Logger) Option {
	return func(o *options) {
		if l != nil {
			o.logger = l
		}
	}
}

// WithAutoMigrate toggles applying the embedded DDL in New (default false).
func WithAutoMigrate(b bool) Option {
	return func(o *options) { o.autoMigrate = b }
}

// WithDefaultConsumer stamps every record unless overridden — the lowest
// rung of the B10 precedence chain.
func WithDefaultConsumer(c string) Option {
	return func(o *options) { o.consumer = c }
}
