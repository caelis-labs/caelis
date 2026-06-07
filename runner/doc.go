// Package runner owns one invocation execution against a session: session
// loading, context preparation, agent dispatch, tool resolution, policy
// and approval wrappers, compaction recovery, task/subagent execution,
// event persistence, and run state.
//
// This is a Layer 4 (Infrastructure / Agent Core) package. It imports
// agent/, model/, session/, tool/, sandbox/, policy/, skill/, and stdlib.
// It must not import tool/builtin/*, gateway/, acp/, tui/, or app/.
//
// Phase 1: types and interfaces only. No behavior.
package runner
