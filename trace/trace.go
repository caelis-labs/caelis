// Package trace defines a minimal SDK tracing contract.
package trace

import "context"

// Tracer starts spans for runner and tool lifecycle operations.
type Tracer interface {
	Start(context.Context, SpanStart) (context.Context, Span)
}

// Span is one active tracing operation.
type Span interface {
	End(SpanEnd)
}

// SpanStart describes a span at creation time.
type SpanStart struct {
	Name       string
	Attributes map[string]any
}

// SpanEnd describes a span at completion time.
type SpanEnd struct {
	Error      error
	Attributes map[string]any
}

// NoopTracer is a disabled tracer implementation.
type NoopTracer struct{}

func (NoopTracer) Start(ctx context.Context, _ SpanStart) (context.Context, Span) {
	return ctx, noopSpan{}
}

type noopSpan struct{}

func (noopSpan) End(SpanEnd) {}
