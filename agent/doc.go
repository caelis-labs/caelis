// Package agent owns the Agent interface, invocation context hierarchy,
// callback types, agent configuration, and agent tree navigation.
//
// This is a Layer 4 (Infrastructure / Agent Core) package. It imports
// model/, session/, tool/, and stdlib. It must not import runner/,
// gateway/, acp/, tui/, or app/.
//
// Sub-packages:
//   - agent/llmagent/ — LLM-backed agent (model/tool loop)
//   - agent/workflow/  — deferred: LoopAgent, ParallelAgent, SequentialAgent
//
// Phase 1: types and interfaces only. No behavior.
package agent
