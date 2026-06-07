package model

import (
	"context"
	"iter"
)

// LLM is the provider-neutral model contract. Implementations live in
// model/providers/.
type LLM interface {
	// Name returns the model identifier (e.g. "claude-sonnet-4-20250514").
	Name() string

	// Generate streams response events for a model request.
	Generate(context.Context, Request) iter.Seq2[ResponseEvent, error]
}

// Registry resolves model references to concrete LLM instances.
type Registry interface {
	// Resolve returns an LLM and its info for the given reference.
	Resolve(context.Context, Ref) (LLM, ModelInfo, error)

	// List returns all known models.
	List(context.Context) ([]ModelInfo, error)
}
