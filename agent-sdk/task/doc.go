// Package task defines task runner, subagent, delegation, and stream contracts
// for the Agent SDK.
//
// The root package owns public task contracts migrated from ports/task: Kind,
// State, Ref, Snapshot, Observer, CommandStartRequest, SubagentStartRequest,
// ControlRequest, Entry, Store, ResultPersistenceMode, Manager, and persistence
// helpers. Delegation, stream, and subagent contracts live in
// agent-sdk/task/delegation, agent-sdk/task/stream, and agent-sdk/task/subagent.
// The SDK-owned ports/task compatibility path has been removed.
//
// Slice 6g migrated ports/task root contracts into this package.
package task
