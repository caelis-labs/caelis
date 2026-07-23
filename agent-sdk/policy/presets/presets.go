package presets

import (
	"context"
	"errors"
	"runtime"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
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
			def := workspaceWriteConstraints(input.Options)
			switch toolName(input) {
			case names.Plan, names.Spawn:
				return allow(def), nil
			case names.Read, names.Grep, names.Glob:
				if err := ensureReadPathsOutsideDefaultHiddenRoots(input); err != nil {
					return policyErrorOrDeny(err)
				}
				return allow(def), nil
			case names.Skill:
				return allow(def), nil
			case names.Write, names.Patch:
				return decideFilesystemWrite(input, def)
			case names.Task:
				return allow(def), nil
			case names.RunCommand:
				return decideCommand(input, def)
			case names.WebSearch, names.WebFetch:
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
	name := toolName(input)
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
	if info, ok := names.LookupExecutable(name); ok {
		return string(info.Kind)
	}
	return string(names.KindOther)
}

func isMCPTool(def tool.Definition) bool {
	if len(def.Metadata) == 0 {
		return false
	}
	kind, _ := def.Metadata[tool.MetadataToolKind].(string)
	return strings.EqualFold(strings.TrimSpace(kind), tool.MetadataToolKindMCP)
}

func approvalTitle(name string, call map[string]any) string {
	return display.SummarizeToolCallTitle(name, call)
}

func workspaceWriteConstraints(opts policy.ModeOptions) sandbox.Constraints {
	devWriteRoots := defaultDeveloperWritableRoots()
	rules := make([]sandbox.PathRule, 0, 2+len(devWriteRoots)+len(opts.ExtraWriteRoots))
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
	return names.ExecutableOrSelf(input.Tool.Name)
}
