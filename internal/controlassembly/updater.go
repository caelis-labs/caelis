package controlassembly

// AgentConfigUpdater replaces the runtime-visible ACP agent list without
// reconstructing controller or subagent runner instances. Product-host
// composition owns the concrete registry and wires one updater into the
// local runtime.
type AgentConfigUpdater interface {
	UpdateAgents(agents []AgentConfig) error
}
