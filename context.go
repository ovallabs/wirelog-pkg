// context.go — notes written on the envelope: context annotations callers
// attach before sending so each record carries business meaning.

package wirelog

import "context"

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
		for k, v := range prev {
			merged[k] = v
		}
	}
	for k, v := range tags {
		merged[k] = v
	}
	return context.WithValue(ctx, ctxTags, merged)
}

func refFrom(ctx context.Context) string {
	s, _ := ctx.Value(ctxRef).(string)
	return s
}

func operationFrom(ctx context.Context) Operation {
	op, _ := ctx.Value(ctxOperation).(Operation)
	return op
}

func consumerFrom(ctx context.Context) string {
	s, _ := ctx.Value(ctxConsumer).(string)
	return s
}

func idempotencyKeyFrom(ctx context.Context) string {
	s, _ := ctx.Value(ctxIdemKey).(string)
	return s
}

func tagsFrom(ctx context.Context) map[string]any {
	m, _ := ctx.Value(ctxTags).(map[string]any)
	return m
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
