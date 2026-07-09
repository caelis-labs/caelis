package presets

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const (
	commandSandboxPermissionUseDefault       = tool.CommandSandboxPermissionUseDefault
	commandSandboxPermissionRequireEscalated = tool.CommandSandboxPermissionRequireEscalated
)

type commandSandboxRequest struct {
	SandboxPermissions string
	Justification      string
}

func (r commandSandboxRequest) withEscalation() commandSandboxRequest {
	r.SandboxPermissions = commandSandboxPermissionRequireEscalated
	return r
}

// explicitEscalationDenyReason returns a deny reason when the agent requested
// require_escalated without a usable justification. Host-default backends that
// force approval without require_escalated are not covered here.
func (r commandSandboxRequest) explicitEscalationDenyReason() string {
	if r.SandboxPermissions != commandSandboxPermissionRequireEscalated {
		return ""
	}
	if r.Justification != "" {
		return ""
	}
	return "require_escalated requires justification: state the command intent, why sandbox/default permissions are insufficient, and how it serves the user task"
}

func parseCommandSandboxRequest(input policy.ToolContext) (commandSandboxRequest, error) {
	args, err := policy.CallArgs(input.Call)
	if err != nil {
		return commandSandboxRequest{}, err
	}
	req := commandSandboxRequest{SandboxPermissions: commandSandboxPermissionUseDefault}

	if raw, ok := args["sandbox_permissions"]; ok && raw != nil {
		value, ok := raw.(string)
		if !ok {
			return req, fmt.Errorf("sandbox_permissions must be a string")
		}
		permission, err := normalizeCommandSandboxPermission(value)
		if err != nil {
			return req, err
		}
		req.SandboxPermissions = permission
	}

	if raw, ok := args["justification"]; ok && raw != nil {
		value, ok := raw.(string)
		if !ok {
			return req, fmt.Errorf("justification must be a string")
		}
		req.Justification = strings.TrimSpace(value)
	}

	return req, nil
}

func normalizeCommandSandboxPermission(value string) (string, error) {
	return tool.NormalizeCommandSandboxPermission(value, true)
}

func (r commandSandboxRequest) approvalMetadata(reason string) map[string]any {
	out := map[string]any{
		"approval_reason":     strings.TrimSpace(reason),
		"sandbox_permissions": r.SandboxPermissions,
	}
	if r.Justification != "" {
		out["justification"] = r.Justification
	}
	return out
}

func mergePathRules(base []sandbox.PathRule, extra []sandbox.PathRule) []sandbox.PathRule {
	out := sandbox.ClonePathRules(base)
	for _, rule := range extra {
		path := filepath.Clean(strings.TrimSpace(rule.Path))
		if path == "." || path == "" {
			continue
		}
		access := rule.Access
		if access == "" {
			access = sandbox.PathAccessReadOnly
		}
		upgraded := false
		for i := range out {
			if filepath.Clean(strings.TrimSpace(out[i].Path)) != path {
				continue
			}
			if out[i].Access == sandbox.PathAccessReadOnly && access == sandbox.PathAccessReadWrite {
				out[i].Access = sandbox.PathAccessReadWrite
			}
			upgraded = true
			break
		}
		if !upgraded {
			out = append(out, sandbox.PathRule{Path: path, Access: access})
		}
	}
	return out
}
