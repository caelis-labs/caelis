// Package task defines task runner, subagent, delegation, and lifecycle contracts
// for the Agent SDK.
//
// The root package owns task identity, state, snapshots, observers, start and
// control requests, storage, result persistence, and management. Focused
// Delegation, producer output, terminal control, and subagent contracts live in
// focused subpackages. Consumer cursors, replay, retention, and fan-out do not.
package task
