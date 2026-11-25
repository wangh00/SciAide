package model

import "context"

// RequestTraceContext is host-only correlation/configuration, never wire data
// or a token measurement. The snapshotter validates fields before publishing.
type RequestTraceContext struct {
	RunID             string
	TurnIndex         int
	Kind              string
	WindowTokens      int
	EffectiveTokens   int
	AutoCompactTokens int
	WindowSource      string
}

type requestTraceContextKey struct{}

func WithRequestTraceContext(ctx context.Context, value RequestTraceContext) context.Context {
	return context.WithValue(ctx, requestTraceContextKey{}, value)
}

func RequestTraceContextFrom(ctx context.Context) RequestTraceContext {
	value, _ := ctx.Value(requestTraceContextKey{}).(RequestTraceContext)
	return value
}
