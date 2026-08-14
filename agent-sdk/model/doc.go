// Package model defines provider-neutral LLM and model-selection contracts for
// the Agent SDK.
//
// Slice 6a moved ports/model public contracts and implementation here. The
// SDK-owned ports/model compatibility path has been removed; callers should use
// this package directly.
//
// Concrete provider implementations live in agent-sdk/model/providers.
package model
