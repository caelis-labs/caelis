package sandbox

import (
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

// Capability identifies one executor feature.
type Capability string

const (
	CapabilityFileSystem     Capability = "file_system"
	CapabilityCommandExec    Capability = "command_exec"
	CapabilityAsyncSessions  Capability = "async_sessions"
	CapabilityTTY            Capability = "tty"
	CapabilityNetworkControl Capability = "network_control"
	CapabilityPathPolicy     Capability = "path_policy"
	CapabilityEnvPolicy      Capability = "env_policy"
)

// CapabilityError reports an executor feature required by assembly but not
// declared by its Descriptor.
type CapabilityError struct {
	Backend    Backend
	Capability Capability
}

func (e *CapabilityError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("sandbox: backend %q does not declare required capability %q", e.Backend, e.Capability)
}

func (e *CapabilityError) ErrorCode() errorcode.Code { return errorcode.Unsupported }

// ValidateCapabilities checks executor requirements in deterministic order.
func ValidateCapabilities(descriptor Descriptor, required CapabilitySet) error {
	actual := descriptor.Capabilities
	checks := []struct {
		capability Capability
		actual     bool
		required   bool
	}{
		{CapabilityFileSystem, actual.FileSystem, required.FileSystem},
		{CapabilityCommandExec, actual.CommandExec, required.CommandExec},
		{CapabilityAsyncSessions, actual.AsyncSessions, required.AsyncSessions},
		{CapabilityTTY, actual.TTY, required.TTY},
		{CapabilityNetworkControl, actual.NetworkControl, required.NetworkControl},
		{CapabilityPathPolicy, actual.PathPolicy, required.PathPolicy},
		{CapabilityEnvPolicy, actual.EnvPolicy, required.EnvPolicy},
	}
	for _, check := range checks {
		if check.required && !check.actual {
			return &CapabilityError{Backend: descriptor.Backend, Capability: check.capability}
		}
	}
	return nil
}
