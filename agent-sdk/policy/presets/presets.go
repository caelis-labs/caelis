package presets

import (
	"context"
	"errors"
	"runtime"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const (
	ModeWorkspaceWrite = policy.ProfileWorkspaceWrite

	// ModeAutoReview and ModeManual are legacy policy names kept for callers
	// that still import them. They normalize to ModeWorkspaceWrite.
	ModeAutoReview = "auto-review"
	ModeManual     = "manual"

	// ModeDefault is the built-in fallback policy for omitted, legacy, or
	// otherwise unresolved local policy configuration.
	ModeDefault = ModeWorkspaceWrite

	riskClassMachine        = "machine"
	riskClassVCSDestructive = "vcs_destructive"
	riskClassPathEscape     = "path_escape"
	riskClassHostExec       = "host_exec"
	riskClassVCSSandbox     = "vcs_sandbox"
)

func NormalizeModeName(mode string) string {
	normalized := policy.NormalizeProfileName(mode)
	if strings.TrimSpace(normalized) == "" {
		return ModeDefault
	}
	return normalized
}

func NewRegistry() (*policy.MemoryRegistry, error) {
	return policy.NewMemory(
		WorkspaceWriteMode(),
		workspaceWriteAliasMode(ModeAutoReview),
		workspaceWriteAliasMode(ModeManual),
	)
}

func WorkspaceWriteMode() policy.Mode {
	return policy.NamedMode{
		ID: ModeWorkspaceWrite,
		Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
			def := baseStrictConstraints(input.Options)
			switch toolName(input) {
			case "PLAN", "SPAWN":
				return allow(def), nil
			case "READ", "SEARCH", "LIST", "GLOB":
				if err := ensureReadPathsOutsideDefaultHiddenRoots(input); err != nil {
					return policyErrorOrDeny(err)
				}
				return allow(filesystemReadToolConstraints(def)), nil
			case "SKILL":
				return allow(filesystemReadToolConstraints(def)), nil
			case "WRITE", "PATCH":
				return decideFilesystemWrite(input, def)
			case "TASK":
				return allow(def), nil
			case "RUN_COMMAND":
				return decideCommand(input, def)
			case "WEB_SEARCH", "WEB_FETCH":
				return allow(def), nil
			default:
				if isMCPTool(input.Tool) || tool.IsToolSearchDefinition(input.Tool) {
					return allow(def), nil
				}
				return deny("tool is not allowed by workspace-write policy"), nil
			}
		},
	}
}

func AutoReviewMode() policy.Mode {
	return WorkspaceWriteMode()
}

func ManualMode() policy.Mode {
	return WorkspaceWriteMode()
}

func workspaceWriteAliasMode(id string) policy.Mode {
	base := WorkspaceWriteMode()
	return policy.NamedMode{
		ID:     strings.TrimSpace(id),
		Decide: base.DecideTool,
	}
}

func allow(constraints sandbox.Constraints) policy.Decision {
	return policy.Decision{
		Action:      policy.ActionAllow,
		Constraints: constraints,
	}
}

func deny(reason string) policy.Decision {
	return policy.Decision{
		Action: policy.ActionDeny,
		Reason: strings.TrimSpace(reason),
	}
}

func policyErrorOrDeny(err error) (policy.Decision, error) {
	var decodeErr *policy.ToolInputDecodeError
	if errors.As(err, &decodeErr) {
		return policy.Decision{}, err
	}
	return deny(err.Error()), nil
}

func askApproval(reason string, constraints sandbox.Constraints, input policy.ToolContext) (policy.Decision, error) {
	name := strings.TrimSpace(strings.ToUpper(input.Tool.Name))
	call, err := policy.CallArgs(input.Call)
	if err != nil {
		return policy.Decision{}, err
	}
	return policy.Decision{
		Action:      policy.ActionAskApproval,
		Reason:      strings.TrimSpace(reason),
		Constraints: constraints,
		Approval: &session.ProtocolApproval{
			ToolCall: session.ProtocolToolCall{
				ID:       strings.TrimSpace(input.Call.ID),
				Name:     name,
				Kind:     toolKind(name),
				Title:    approvalTitle(name, call),
				Status:   "pending",
				RawInput: call,
			},
			Options: []session.ProtocolApprovalOption{
				{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
				{ID: "reject_once", Name: "Reject once", Kind: "reject_once"},
			},
		},
	}, nil
}

func hostExecutionConstraints() sandbox.Constraints {
	return sandbox.Constraints{
		Route:      sandbox.RouteHost,
		Backend:    sandbox.BackendHost,
		Permission: sandbox.PermissionFullAccess,
		Isolation:  sandbox.IsolationHost,
		Network:    sandbox.NetworkInherit,
	}
}

func toolKind(name string) string {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "READ":
		return "read"
	case "WRITE", "PATCH":
		return "edit"
	case "SEARCH", "GLOB", "LIST":
		return "search"
	case "RUN_COMMAND", "TASK", "SPAWN":
		return "execute"
	default:
		return "other"
	}
}

func isMCPTool(def tool.Definition) bool {
	if len(def.Metadata) == 0 {
		return false
	}
	kind, _ := def.Metadata[tool.MetadataToolKind].(string)
	return strings.EqualFold(strings.TrimSpace(kind), tool.MetadataToolKindMCP)
}

func approvalTitle(name string, call map[string]any) string {
	name = strings.ToUpper(strings.TrimSpace(name))
	switch name {
	case "READ", "WRITE", "PATCH", "SEARCH", "LIST", "GLOB":
		if path, _ := call["path"].(string); strings.TrimSpace(path) != "" {
			return strings.TrimSpace(name + " " + path)
		}
	case "RUN_COMMAND":
		if command, _ := call["command"].(string); strings.TrimSpace(command) != "" {
			return strings.TrimSpace(name + " " + command)
		}
	case "TASK":
		if action, _ := call["action"].(string); strings.TrimSpace(action) != "" {
			if taskID, _ := call["task_id"].(string); strings.TrimSpace(taskID) != "" {
				return strings.TrimSpace(name + " " + action + " " + taskID)
			}
			return strings.TrimSpace(name + " " + action)
		}
	case "SPAWN":
		if agent, _ := call["agent"].(string); strings.TrimSpace(agent) != "" {
			return strings.TrimSpace(name + " " + agent)
		}
	}
	return name
}

func baseStrictConstraints(opts policy.ModeOptions) sandbox.Constraints {
	devWriteRoots := defaultDeveloperWritableRoots()
	rules := make([]sandbox.PathRule, 0, 2+len(devWriteRoots)+len(opts.ExtraWriteRoots)+len(opts.ExtraReadRoots))
	appendRule := func(path string, access sandbox.PathAccess) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		rules = append(rules, sandbox.PathRule{Path: path, Access: access})
	}
	appendRule(opts.WorkspaceRoot, sandbox.PathAccessReadWrite)
	if runtime.GOOS != "windows" {
		appendRule(opts.TempRoot, sandbox.PathAccessReadWrite)
	}
	for _, path := range devWriteRoots {
		appendRule(path, sandbox.PathAccessReadWrite)
	}
	for _, path := range opts.ExtraWriteRoots {
		appendRule(path, sandbox.PathAccessReadWrite)
	}
	for _, path := range opts.ExtraReadRoots {
		appendRule(path, sandbox.PathAccessReadOnly)
	}
	return sandbox.Constraints{
		Route:      sandbox.RouteSandbox,
		Permission: sandbox.PermissionWorkspaceWrite,
		Isolation:  sandbox.IsolationContainer,
		Network:    defaultNetworkPolicy(opts),
		PathRules:  rules,
	}
}

func defaultNetworkPolicy(opts policy.ModeOptions) sandbox.Network {
	if opts.NetworkEnabled != nil && !*opts.NetworkEnabled {
		return sandbox.NetworkDisabled
	}
	return sandbox.NetworkEnabled
}

func toolName(input policy.ToolContext) string {
	return strings.ToUpper(strings.TrimSpace(input.Tool.Name))
}
