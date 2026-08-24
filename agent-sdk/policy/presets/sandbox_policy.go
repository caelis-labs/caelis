package presets

import (
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	backendpolicy "github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/policy"
)

// EffectiveSandboxPolicy projects the built-in policy profile and selected
// Runtime backend into the facts agents and approval reviewers need.
func EffectiveSandboxPolicy(
	profile string,
	cfg sandbox.Config,
	descriptor sandbox.Descriptor,
	options policy.ModeOptions,
) sandbox.PolicySnapshot {
	constraints := workspaceWriteConstraints(options)
	if NormalizeModeName(profile) == ModeDangerFullAccess || sandbox.DescriptorImpliesHostExecution(descriptor) {
		constraints = hostExecutionConstraints()
	}
	effective := backendpolicy.Default(cfg, constraints)
	snapshot := sandbox.PolicySnapshot{
		Route:            constraints.Route,
		Backend:          descriptor.Backend,
		Permission:       constraints.Permission,
		Isolation:        descriptor.Isolation,
		Network:          constraints.Network,
		WritableRoots:    effective.WritableRoots,
		ReadOnlySubpaths: effective.ReadOnlySubpaths,
	}
	if snapshot.Backend == "" {
		snapshot.Backend = constraints.Backend
	}
	if snapshot.Isolation == "" {
		snapshot.Isolation = constraints.Isolation
	}
	if snapshot.Route == sandbox.RouteHost {
		snapshot.Backend = sandbox.BackendHost
		snapshot.Isolation = sandbox.IsolationHost
		snapshot.Network = sandbox.NetworkInherit
	}
	return sandbox.ClonePolicySnapshot(snapshot)
}
