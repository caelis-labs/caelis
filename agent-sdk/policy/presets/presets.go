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
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/sendmessage"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	skilltool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/skill"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/web"
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
			switch policyClass(input) {
			case builtinPolicyReadPath, builtinPolicySearchPath, builtinPolicyGlobPath:
				if err := ensureReadPathsOutsideDefaultHiddenRoots(input); err != nil {
					return policyErrorOrDeny(err)
				}
				return allow(def), nil
			case builtinPolicyWritePath:
				return decideFilesystemWrite(input, def)
			case builtinPolicyCommand:
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
// retaining the small machine-level command denylist. Callers must
// register this mode explicitly; it is not a substitute for sandbox isolation.
func DangerFullAccessMode() policy.Mode {
	return policy.NamedMode{
		ID: ModeDangerFullAccess,
		Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
			if policyClass(input) == builtinPolicyCommand {
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
	decision := policy.Decision{
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
	}
	if !sandbox.PolicySnapshotEmpty(input.SandboxPolicy) {
		decision.Metadata = map[string]any{
			policy.MetadataSandboxPolicy: sandbox.ClonePolicySnapshot(input.SandboxPolicy),
		}
	}
	return decision, nil
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
	switch name {
	case filesystem.ReadToolName, filesystem.ViewImageToolName, skilltool.ToolName:
		return "read"
	case filesystem.WriteToolName, filesystem.PatchToolName:
		return "edit"
	case filesystem.GlobToolName, filesystem.SearchToolName, web.SearchToolName, web.FetchToolName:
		return "search"
	case shell.RunCommandToolName, tasktool.ToolName, spawn.ToolName, sendmessage.ToolName:
		return "execute"
	default:
		return "other"
	}
}

func approvalTitle(name string, call map[string]any) string {
	switch name {
	case filesystem.ReadToolName, filesystem.ViewImageToolName, filesystem.WriteToolName, filesystem.PatchToolName, filesystem.GlobToolName, filesystem.SearchToolName:
		if path := strings.TrimSpace(display.MapString(call, "path")); path != "" {
			return name + " " + path
		}
	case skilltool.ToolName:
		if skillName := strings.TrimSpace(display.MapString(call, "name")); skillName != "" {
			return name + " " + skillName
		}
	case web.SearchToolName:
		if query := strings.TrimSpace(display.MapString(call, "query")); query != "" {
			return name + " " + query
		}
	case web.FetchToolName:
		if url := strings.TrimSpace(display.MapString(call, "url")); url != "" {
			return name + " " + url
		}
	case shell.RunCommandToolName, tasktool.ToolName:
		if command := strings.TrimSpace(display.MapString(call, "command")); command != "" {
			return name + " " + command
		}
		if action := strings.TrimSpace(display.MapString(call, "action")); action != "" {
			if handle := strings.TrimSpace(display.MapString(call, "handle")); handle != "" {
				return name + " " + action + " " + handle
			}
			return name + " " + action
		}
	case spawn.ToolName:
		if args := strings.TrimSpace(display.SpawnFullDisplayArgs(call)); args != "" {
			return name + " " + args
		}
	case sendmessage.ToolName:
		if args := strings.TrimSpace(display.AgentMessageFullDisplayArgs(call)); args != "" {
			return "Send message " + args
		}
	}
	return name
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
	return input.Tool.Name
}

type builtinPolicyClass uint8

const (
	builtinPolicyUnknown builtinPolicyClass = iota
	builtinPolicyReadPath
	builtinPolicySearchPath
	builtinPolicyGlobPath
	builtinPolicyWritePath
	builtinPolicyCommand
)

func builtinPolicyClassForName(name string) builtinPolicyClass {
	switch name {
	case filesystem.ReadToolName, filesystem.ViewImageToolName:
		return builtinPolicyReadPath
	case filesystem.SearchToolName:
		return builtinPolicySearchPath
	case filesystem.GlobToolName:
		return builtinPolicyGlobPath
	case filesystem.WriteToolName, filesystem.PatchToolName:
		return builtinPolicyWritePath
	case shell.RunCommandToolName:
		return builtinPolicyCommand
	default:
		return builtinPolicyUnknown
	}
}

func policyClass(input policy.ToolContext) builtinPolicyClass {
	if class := builtinPolicyClassForName(toolName(input)); class != builtinPolicyUnknown {
		return class
	}
	if input.Tool.ExecutionRequirements != nil && input.Tool.ExecutionRequirements.Sandbox.CommandExec {
		return builtinPolicyCommand
	}
	return builtinPolicyUnknown
}
