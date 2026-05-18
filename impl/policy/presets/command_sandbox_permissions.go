package presets

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/policy"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

const (
	commandSandboxPermissionUseDefault                = "use_default"
	commandSandboxPermissionRequireEscalated          = "require_escalated"
	commandSandboxPermissionWithAdditionalPermissions = "with_additional_permissions"
)

type commandSandboxRequest struct {
	SandboxPermissions         string
	ExplicitSandboxPermissions bool
	Justification              string
	AdditionalPermissions      any
	AdditionalPathRules        []sandbox.PathRule
	AdditionalNetwork          sandbox.Network
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
		req.ExplicitSandboxPermissions = true
	}

	if raw, ok := args["justification"]; ok && raw != nil {
		value, ok := raw.(string)
		if !ok {
			return req, fmt.Errorf("justification must be a string")
		}
		req.Justification = strings.TrimSpace(value)
	}

	if raw, ok := args["additional_permissions"]; ok && raw != nil {
		additional, err := parseCommandAdditionalPermissions(raw, input, args)
		if err != nil {
			return req, err
		}
		req.AdditionalPermissions = raw
		req.AdditionalPathRules = additional.PathRules
		req.AdditionalNetwork = additional.Network
	}

	if req.SandboxPermissions != commandSandboxPermissionWithAdditionalPermissions && req.AdditionalPermissions != nil {
		return req, fmt.Errorf("additional_permissions requires sandbox_permissions=%q", commandSandboxPermissionWithAdditionalPermissions)
	}
	if req.SandboxPermissions == commandSandboxPermissionRequireEscalated && req.ExplicitSandboxPermissions && req.Justification == "" {
		return req, fmt.Errorf("sandbox_permissions=%q requires a non-empty justification", commandSandboxPermissionRequireEscalated)
	}
	if req.SandboxPermissions == commandSandboxPermissionWithAdditionalPermissions && !req.hasAdditionalGrant() {
		return req, fmt.Errorf("sandbox_permissions=%q requires non-empty additional_permissions", commandSandboxPermissionWithAdditionalPermissions)
	}
	return req, nil
}

func normalizeCommandSandboxPermission(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "default", commandSandboxPermissionUseDefault:
		return commandSandboxPermissionUseDefault, nil
	case commandSandboxPermissionRequireEscalated:
		return commandSandboxPermissionRequireEscalated, nil
	case commandSandboxPermissionWithAdditionalPermissions:
		return commandSandboxPermissionWithAdditionalPermissions, nil
	default:
		return "", fmt.Errorf("unknown sandbox_permissions value %q", value)
	}
}

func (r commandSandboxRequest) hasAdditionalGrant() bool {
	return len(r.AdditionalPathRules) > 0 || r.AdditionalNetwork == sandbox.NetworkEnabled
}

func (r commandSandboxRequest) approvalMetadata(reason string) map[string]any {
	out := map[string]any{
		"approval_reason":     strings.TrimSpace(reason),
		"sandbox_permissions": r.SandboxPermissions,
	}
	if r.Justification != "" {
		out["justification"] = r.Justification
	}
	if additional := r.normalizedAdditionalPermissions(); len(additional) > 0 {
		out["additional_permissions"] = additional
	}
	return out
}

func (r commandSandboxRequest) normalizedAdditionalPermissions() map[string]any {
	out := map[string]any{}
	if r.AdditionalNetwork == sandbox.NetworkEnabled {
		out["network"] = map[string]any{"enabled": true}
	}
	if len(r.AdditionalPathRules) > 0 {
		read := []string{}
		write := []string{}
		for _, rule := range r.AdditionalPathRules {
			path := strings.TrimSpace(rule.Path)
			if path == "" {
				continue
			}
			switch rule.Access {
			case sandbox.PathAccessReadWrite:
				write = append(write, path)
			case sandbox.PathAccessReadOnly:
				read = append(read, path)
			}
		}
		fileSystem := map[string]any{}
		if len(read) > 0 {
			fileSystem["read"] = read
		}
		if len(write) > 0 {
			fileSystem["write"] = write
		}
		if len(fileSystem) > 0 {
			out["file_system"] = fileSystem
		}
	}
	return out
}

func applyCommandAdditionalPermissions(base sandbox.Constraints, req commandSandboxRequest) sandbox.Constraints {
	out := sandbox.NormalizeConstraints(base)
	out.Route = sandbox.RouteSandbox
	out.Permission = sandbox.PermissionWorkspaceWrite
	if req.AdditionalNetwork == sandbox.NetworkEnabled {
		out.Network = sandbox.NetworkEnabled
	}
	out.PathRules = mergePathRules(out.PathRules, req.AdditionalPathRules)
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

type parsedCommandAdditionalPermissions struct {
	Network   sandbox.Network
	PathRules []sandbox.PathRule
}

func parseCommandAdditionalPermissions(raw any, input policy.ToolContext, args map[string]any) (parsedCommandAdditionalPermissions, error) {
	obj, ok := raw.(map[string]any)
	if !ok {
		return parsedCommandAdditionalPermissions{}, fmt.Errorf("additional_permissions must be an object")
	}
	out := parsedCommandAdditionalPermissions{}
	for key := range obj {
		switch key {
		case "network", "file_system":
		default:
			return out, fmt.Errorf("additional_permissions.%s is not supported", key)
		}
	}
	if rawNetwork, ok := obj["network"]; ok && rawNetwork != nil {
		network, err := parseCommandAdditionalNetwork(rawNetwork)
		if err != nil {
			return out, err
		}
		out.Network = network
	}
	if rawFileSystem, ok := obj["file_system"]; ok && rawFileSystem != nil {
		pathRules, err := parseCommandAdditionalFileSystem(rawFileSystem, input, args)
		if err != nil {
			return out, err
		}
		out.PathRules = pathRules
	}
	return out, nil
}

func parseCommandAdditionalNetwork(raw any) (sandbox.Network, error) {
	obj, ok := raw.(map[string]any)
	if !ok {
		return "", fmt.Errorf("additional_permissions.network must be an object")
	}
	for key := range obj {
		if key != "enabled" {
			return "", fmt.Errorf("additional_permissions.network.%s is not supported", key)
		}
	}
	rawEnabled, ok := obj["enabled"]
	if !ok || rawEnabled == nil {
		return "", nil
	}
	enabled, ok := rawEnabled.(bool)
	if !ok {
		return "", fmt.Errorf("additional_permissions.network.enabled must be a boolean")
	}
	if enabled {
		return sandbox.NetworkEnabled, nil
	}
	return "", nil
}

func parseCommandAdditionalFileSystem(raw any, input policy.ToolContext, args map[string]any) ([]sandbox.PathRule, error) {
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("additional_permissions.file_system must be an object")
	}
	for key := range obj {
		switch key {
		case "read", "write":
		default:
			return nil, fmt.Errorf("additional_permissions.file_system.%s is not supported", key)
		}
	}
	rules := []sandbox.PathRule{}
	addPaths := func(raw any, label string, access sandbox.PathAccess) error {
		values, err := stringList(raw, label)
		if err != nil {
			return err
		}
		for _, value := range values {
			resolved := resolveAdditionalPermissionPath(value, input, args)
			if resolved == "" {
				return fmt.Errorf("%s contains an empty path", label)
			}
			if access == sandbox.PathAccessReadWrite {
				resolved = shellWritableRoot(resolved)
			}
			rules = mergePathRules(rules, []sandbox.PathRule{{Path: resolved, Access: access}})
		}
		return nil
	}
	if rawRead, ok := obj["read"]; ok && rawRead != nil {
		if err := addPaths(rawRead, "additional_permissions.file_system.read", sandbox.PathAccessReadOnly); err != nil {
			return nil, err
		}
	}
	if rawWrite, ok := obj["write"]; ok && rawWrite != nil {
		if err := addPaths(rawWrite, "additional_permissions.file_system.write", sandbox.PathAccessReadWrite); err != nil {
			return nil, err
		}
	}
	return rules, nil
}

func resolveAdditionalPermissionPath(value string, input policy.ToolContext, args map[string]any) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	base := additionalPermissionBasePath(input, args)
	return filepath.Clean(filepath.Join(base, value))
}

func additionalPermissionBasePath(input policy.ToolContext, args map[string]any) string {
	if raw, ok := args["workdir"]; ok && raw != nil {
		if workdir, ok := raw.(string); ok {
			workdir = strings.TrimSpace(workdir)
			if workdir != "" {
				if filepath.IsAbs(workdir) {
					return filepath.Clean(workdir)
				}
				if root := strings.TrimSpace(input.Options.WorkspaceRoot); root != "" {
					return filepath.Clean(filepath.Join(root, workdir))
				}
				return filepath.Clean(workdir)
			}
		}
	}
	if root := strings.TrimSpace(input.Options.WorkspaceRoot); root != "" {
		return filepath.Clean(root)
	}
	return string(filepath.Separator)
}

func shellWritableRoot(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	cleaned := filepath.Clean(path)
	if info, err := os.Stat(cleaned); err == nil && info.IsDir() {
		return cleaned
	}
	parent := filepath.Dir(cleaned)
	if parent == "." || parent == "" || parent == string(filepath.Separator) {
		return cleaned
	}
	return parent
}

func stringList(raw any, label string) ([]string, error) {
	switch typed := raw.(type) {
	case string:
		if trimmed := strings.TrimSpace(typed); trimmed != "" {
			return []string{trimmed}, nil
		}
		return nil, nil
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out, nil
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s entries must be strings", label)
			}
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%s must be a string array", label)
	}
}
