package wirelog

import (
	"context"
	"reflect"
	"testing"
)

func TestContextHelpers(t *testing.T) {
	ctx := context.Background()
	if refFrom(ctx) != "" || operationFrom(ctx) != "" || consumerFrom(ctx) != "" ||
		idempotencyKeyFrom(ctx) != "" || tagsFrom(ctx) != nil {
		t.Fatal("annotations on a bare context must be zero values")
	}

	ctx = WithRef(ctx, "ref-1")
	ctx = WithOperation(ctx, Operation("payout.execute"))
	ctx = WithConsumer(ctx, "magma-demo")
	ctx = WithIdempotencyKey(ctx, "idem-1")
	ctx = WithTags(ctx, map[string]any{"batch": "b1"})

	if got := refFrom(ctx); got != "ref-1" {
		t.Errorf("refFrom = %q, want ref-1", got)
	}
	if got := operationFrom(ctx); got != "payout.execute" {
		t.Errorf("operationFrom = %q, want payout.execute", got)
	}
	if got := consumerFrom(ctx); got != "magma-demo" {
		t.Errorf("consumerFrom = %q, want magma-demo", got)
	}
	if got := idempotencyKeyFrom(ctx); got != "idem-1" {
		t.Errorf("idempotencyKeyFrom = %q, want idem-1", got)
	}
	if got := tagsFrom(ctx); !reflect.DeepEqual(got, map[string]any{"batch": "b1"}) {
		t.Errorf("tagsFrom = %v, want {batch: b1}", got)
	}
}

func TestWithTagsMergesAcrossCalls(t *testing.T) {
	ctx := WithTags(context.Background(), map[string]any{"a": 1, "b": 1})
	ctx = WithTags(ctx, map[string]any{"b": 2, "c": 3})
	want := map[string]any{"a": 1, "b": 2, "c": 3}
	if got := tagsFrom(ctx); !reflect.DeepEqual(got, want) {
		t.Errorf("merged tags = %v, want %v (last write wins per key)", got, want)
	}
}

func TestWithTagsDoesNotMutateParent(t *testing.T) {
	parent := WithTags(context.Background(), map[string]any{"a": 1})
	_ = WithTags(parent, map[string]any{"a": 2, "b": 2})
	sibling := WithTags(parent, map[string]any{"c": 3})
	if got := tagsFrom(parent); !reflect.DeepEqual(got, map[string]any{"a": 1}) {
		t.Errorf("parent tags mutated by child merge: %v", got)
	}
	want := map[string]any{"a": 1, "c": 3}
	if got := tagsFrom(sibling); !reflect.DeepEqual(got, want) {
		t.Errorf("sibling tags = %v, want %v", got, want)
	}
}

func TestOperationOverwriteLastWins(t *testing.T) {
	ctx := WithOperation(context.Background(), "payout.init")
	ctx = WithOperation(ctx, "payout.execute")
	if got := operationFrom(ctx); got != "payout.execute" {
		t.Errorf("operationFrom = %q, want payout.execute", got)
	}
}

func TestResolveConsumerPrecedence(t *testing.T) {
	ctxWith := WithConsumer(context.Background(), "ctx-consumer")
	tests := []struct {
		name            string
		ctx             context.Context
		cfgConsumer     string
		instanceDefault string
		want            string
	}{
		{"ctx beats config and instance", ctxWith, "cfg-consumer", "inst-consumer", "ctx-consumer"},
		{"config beats instance", context.Background(), "cfg-consumer", "inst-consumer", "cfg-consumer"},
		{"instance default last", context.Background(), "", "inst-consumer", "inst-consumer"},
		{"all empty", context.Background(), "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveConsumer(tt.ctx, tt.cfgConsumer, tt.instanceDefault); got != tt.want {
				t.Fatalf("resolveConsumer = %q, want %q", got, tt.want)
			}
		})
	}
}
