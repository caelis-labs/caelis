package runner

import (
	"context"

	"github.com/OnslaughtSnail/caelis/policy"
	"github.com/OnslaughtSnail/caelis/sandbox"
)

// toolContext implements tool.Context for runner-wired tool execution.
type toolContext struct {
	context.Context
	sessionRef    string
	invocationID  string
	agentName     string
	fs            sandbox.FileSystem
	backend       sandbox.Backend
	workspaceRoot string
	constraints   *policy.SandboxConstraints
}

func (c *toolContext) SessionRef() string   { return c.sessionRef }
func (c *toolContext) InvocationID() string { return c.invocationID }
func (c *toolContext) AgentName() string    { return c.agentName }

// FileSystem returns the sandboxed filesystem. If constraints have been
// set by the policy wrapper, they are passed to the backend so it can
// enforce path restrictions.
func (c *toolContext) FileSystem() sandbox.FileSystem {
	if c.fs != nil {
		return c.fs
	}
	return nil
}

// SandboxBackend returns the sandbox backend for command execution.
func (c *toolContext) SandboxBackend() sandbox.Backend { return c.backend }

// SetConstraints updates the constraints on the tool context.
// Called by the policy wrapper before delegating to the inner tool.
// Fails closed: if the backend cannot create a constrained FS,
// the filesystem is set to nil so tools get a clear error.
func (c *toolContext) SetConstraints(constraints *policy.SandboxConstraints) {
	c.constraints = constraints
	if c.backend != nil && constraints != nil {
		sandboxConstraints := toSandboxConstraints(constraints)
		fs, err := c.backend.FileSystem(c.Context, sandboxConstraints)
		if err != nil || fs == nil {
			// Fail closed: clear filesystem so tools cannot bypass constraints.
			c.fs = nil
		} else {
			c.fs = fs
		}
	}
}

// Constraints returns the current policy constraints.
func (c *toolContext) Constraints() *policy.SandboxConstraints {
	return c.constraints
}

// toSandboxConstraints converts policy constraints to sandbox constraints.
func toSandboxConstraints(pc *policy.SandboxConstraints) sandbox.Constraints {
	if pc == nil {
		return sandbox.Constraints{}
	}
	var paths []sandbox.PathRule
	for _, p := range pc.Paths {
		access := p.Access
		if access == "" {
			access = sandbox.PathAccessRead
		}
		paths = append(paths, sandbox.PathRule{
			Path:   p.Path,
			Access: sandbox.PathAccess(access),
		})
	}
	return sandbox.Constraints{
		Permission: sandboxPermission(pc.Permission),
		Paths:      paths,
		Network:    pc.Network,
	}
}

func sandboxPermission(p policy.SandboxPermission) sandbox.Permission {
	switch p {
	case policy.SandboxPermRequireEscalated:
		return sandbox.PermissionEscalated
	default:
		return sandbox.PermissionDefault
	}
}
