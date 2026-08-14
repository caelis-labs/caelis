// Package agentsdk is the root of the Caelis Agent SDK.
//
// The root package carries cross-domain public contracts and common value types
// such as agent specs, turn requests, runtime events, capabilities, approvals,
// handoff values, usage, and stable errors. Domain-specific contracts live in
// child packages such as approval, model, tool, session, sandbox, runtime, and
// task.
//
// Phase 2 Slice 5a moved source/forwarder root contracts here. Slice 5b moved
// the remaining root runtime contracts: context, submission, run state, agent
// specs, approval bridge values, run requests/results, session control-plane
// requests, and stream provider capability. Slice 6h moved approval review
// contracts into agent-sdk/approval. SDK-owned ports and impl compatibility
// paths have been removed; product-host contracts remain outside the SDK.
package agentsdk
