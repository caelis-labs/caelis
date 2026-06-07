// Package builtin contains built-in tool implementations.
//
// Sub-packages:
//   - filesystem/ — READ, WRITE, PATCH, LIST, GLOB, SEARCH
//   - shell/      — RUN_COMMAND
//   - task/       — TASK
//   - plan/       — PLAN
//   - spawn/      — SPAWN
//
// Built-in tools implement tool.Tool and must not import session/, runner/,
// gateway/, acp/, tui/, or app/. They receive sandbox and policy
// capabilities through narrow interfaces in tool/.
//
// Phase 1: skeleton only. No behavior.
package builtin
