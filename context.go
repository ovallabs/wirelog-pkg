// context.go — notes written on the envelope: context annotations callers
// attach before sending so each record carries business meaning.

package wirelog

import (
	"context"
	"maps"
)

// ctxKey keeps annotation keys unexported so callers can't collide with them.
type ctxKey int

const (
	ctxRef ctxKey = iota
	ctxOperation
	ctxConsumer
	ctxIdemKey
	ctxTags
)

// WithRef attaches an internal reference to outgoing calls under ctx.
func WithRef(ctx context.Context, ref string) context.Context {
	return context.WithValue(ctx, ctxRef, ref)
}

// WithOperation labels outgoing calls under ctx; last write wins.
func WithOperation(ctx context.Context, op Operation) context.Context {
	return context.WithValue(ctx, ctxOperation, op)
}

// WithConsumer overrides the consumer for calls under ctx (highest B10 precedence).
func WithConsumer(ctx context.Context, consumer string) context.Context {
	return context.WithValue(ctx, ctxConsumer, consumer)
}

// WithIdempotencyKey attaches the idempotency key to outgoing calls under ctx.
func WithIdempotencyKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, ctxIdemKey, key)
}

// WithTags MERGES tags into any already on ctx: shallow, last write wins per
// key. The merge copies so sibling contexts never share a mutable map.
func WithTags(ctx context.Context, tags map[string]any) context.Context {
	merged := make(map[string]any, len(tags))
	if prev, ok := ctx.Value(ctxTags).(map[string]any); ok {
		maps.Copy(merged, prev)
	}
	maps.Copy(merged, tags)
	return context.WithValue(ctx, ctxTags, merged)
}

// refFrom returns the internal reference annotated on ctx, or "" when unset.
func refFrom(ctx context.Context) string {
	ref, _ := ctx.Value(ctxRef).(string)
	return ref
}

// operationFrom returns the operation annotated on ctx, or "" when unset.
func operationFrom(ctx context.Context) Operation {
	operation, _ := ctx.Value(ctxOperation).(Operation)
	return operation
}

// consumerFrom returns the consumer annotated on ctx, or "" when unset.
func consumerFrom(ctx context.Context) string {
	consumer, _ := ctx.Value(ctxConsumer).(string)
	return consumer
}

// idempotencyKeyFrom returns the idempotency key annotated on ctx, or "" when unset.
func idempotencyKeyFrom(ctx context.Context) string {
	key, _ := ctx.Value(ctxIdemKey).(string)
	return key
}

// tagsFrom returns the merged tags annotated on ctx, or nil when unset.
func tagsFrom(ctx context.Context) map[string]any {
	tags, _ := ctx.Value(ctxTags).(map[string]any)
	return tags
}

// resolveConsumer applies B10 precedence: WithConsumer(ctx) > Config.Consumer > instance default.
func resolveConsumer(ctx context.Context, cfgConsumer, instanceDefault string) string {
	if c := consumerFrom(ctx); c != "" {
		return c
	}
	if cfgConsumer != "" {
		return cfgConsumer
	}
	return instanceDefault
}
