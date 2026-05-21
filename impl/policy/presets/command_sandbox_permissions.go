package presets

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/policy"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

const (
	commandSandboxPermissionUseDefault       = "use_default"
	commandSandboxPermissionRequireEscalated = "require_escalated"
)

type commandSandboxRequest struct {
	SandboxPermissions string
	Justification      string
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
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", commandSandboxPermissionUseDefault:
		return commandSandboxPermissionUseDefault, nil
	case commandSandboxPermissionRequireEscalated:
		return commandSandboxPermissionRequireEscalated, nil
	case "with_additional_permissions":
		return commandSandboxPermissionUseDefault, nil
	default:
		return "", fmt.Errorf("unknown sandbox_permissions value %q", value)
	}
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
