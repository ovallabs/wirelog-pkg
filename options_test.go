package wirelog

import (
	"testing"
	"time"
)

func TestDefaultOptions(t *testing.T) {
	o := defaultOptions()
	if o.buffer != 2048 {
		t.Errorf("buffer = %d, want 2048", o.buffer)
	}
	if o.batchSize != 100 {
		t.Errorf("batchSize = %d, want 100", o.batchSize)
	}
	if o.flushInterval != 2*time.Second {
		t.Errorf("flushInterval = %v, want 2s", o.flushInterval)
	}
	if _, ok := o.logger.(nopLogger); !ok {
		t.Errorf("logger = %T, want silent no-op", o.logger)
	}
	if o.autoMigrate {
		t.Error("autoMigrate must default false")
	}
	if o.consumer != "" {
		t.Errorf("consumer = %q, want empty", o.consumer)
	}
}

func TestOptionsApply(t *testing.T) {
	log := &recordingLogger{}
	o := defaultOptions()
	for _, opt := range []Option{
		WithBuffer(64),
		WithBatchSize(5),
		WithFlushInterval(50 * time.Millisecond),
		WithLogger(log),
		WithAutoMigrate(true),
		WithDefaultConsumer("magma-demo"),
	} {
		opt(&o)
	}
	if o.buffer != 64 || o.batchSize != 5 || o.flushInterval != 50*time.Millisecond {
		t.Errorf("numeric options not applied: %+v", o)
	}
	if o.logger != log || !o.autoMigrate || o.consumer != "magma-demo" {
		t.Errorf("logger/migrate/consumer options not applied: %+v", o)
	}
}

func TestOptionsRejectInvalidValues(t *testing.T) {
	o := defaultOptions()
	for _, opt := range []Option{WithBuffer(0), WithBatchSize(-1), WithFlushInterval(0), WithLogger(nil)} {
		opt(&o)
	}
	d := defaultOptions()
	if o.buffer != d.buffer || o.batchSize != d.batchSize || o.flushInterval != d.flushInterval {
		t.Errorf("invalid values must keep defaults: %+v", o)
	}
	if _, ok := o.logger.(nopLogger); !ok {
		t.Errorf("nil logger must keep the no-op default, got %T", o.logger)
	}
}
