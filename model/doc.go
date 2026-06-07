// Package model owns the provider-neutral LLM contract, message types,
// stream events, tool specs, output specs, and the model registry.
//
// This is a Layer 4 (Infrastructure / Agent Core) leaf package. It imports
// only stdlib and helper deps. It must not import session/, tool/, agent/,
// runner/, gateway/, acp/, tui/, or app/.
//
// Sub-packages:
//   - model/catalog/ — model directory and capability overrides
//   - model/providers/ — concrete provider adapters (OpenAI, Anthropic, …)
//
// Phase 1: types and interfaces only. No behavior.
package model
