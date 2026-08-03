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
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
)

const (
	ModeWorkspaceWrite = policy.ProfileWorkspaceWrite
	// ModeDangerFullAccess is an explicitly registered escape policy for hosts
	// that cannot provide a supported sandbox. It is intentionally omitted from
	// NewRegistry so ordinary policy configuration cannot enable it.
	ModeDangerFullAccess = "danger-full-access"

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
			name := toolName(input)
			if info, ok := names.LookupExecutable(name); ok {
				switch info.ResultStyle {
				case names.ResultRead, names.ResultSearch, names.ResultGlob:
					if err := ensureReadPathsOutsideDefaultHiddenRoots(input); err != nil {
						return policyErrorOrDeny(err)
					}
					return allow(def), nil
				}
			}
			switch name {
			case names.Write, names.Patch:
				return decideFilesystemWrite(input, def)
			case names.RunCommand:
				return decideCommand(input, def)
			default:
				// Tool assembly is the capability-admission boundary. This
				// preset classifies calls that need path, command, or sandbox
				// restrictions; it is not a second tool-name allowlist.
				return allow(def), nil
			}
		},
	}
}

// DangerFullAccessMode allows tools to execute directly on the Host while
// retaining the small machine-level RUN_COMMAND denylist. Callers must
// register this mode explicitly; it is not a substitute for sandbox isolation.
func DangerFullAccessMode() policy.Mode {
	return policy.NamedMode{
		ID: ModeDangerFullAccess,
		Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
			if toolName(input) == names.RunCommand {
				command, err := commandArg(input)
				if err != nil {
					return policy.Decision{}, err
				}
				if class := scanCommandTree(command, input.Options, classifyMachineHardDeny); class.Action == policy.ActionDeny {
					return deny(class.Reason), nil
				}
			}
			return allow(hostExecutionConstraints()), nil
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
