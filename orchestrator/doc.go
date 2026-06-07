// Package orchestrator owns multi-agent orchestration for Layer 4.
//
// It provides:
//   - Agent registry and resolution (internal and external ACP agents)
//   - ACP child handle lifecycle (spawn, wait, continue, cancel)
//   - SPAWN delegation implementing agent.SpawnDelegator
//   - ACP loopback adapter for internal/self agents
//   - Context visibility builders (main, sidecar, delegated, parent-summary)
//   - Permission bridging (child ACP → parent approval → response)
//   - Stream merge from child events to parent tool updates
//
// The orchestrator imports: acp/, agent/, runner/, session/, tool/, policy/.
// It must not import: gateway/, app/, tui/, headless/, cmd/.
package orchestrator
