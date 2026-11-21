package presets

import (
	"context"
	"errors"
	"runtime"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

const (
	ModeWorkspaceWrite = policy.ProfileWorkspaceWrite
	// ModeDangerFullAccess is an explicitly registered escape policy for hosts
	// that cannot provide a supported sandbox. It is intentionally omitted from
	// NewRegistry so ordinary policy configuration cannot enable it.
	ModeDangerFullAccess = "danger-full-access"

	// ModeDefault is the built-in fallback policy for omitted, legacy, or
	// otherwise unresolved local policy configuration.
	ModeDefault = ModeWorkspaceWrite

	riskClassMachine        = "machine"
	riskClassVCSDestructive = "vcs_destructive"
	riskClassPathEscape     = "path_escape"
	riskClassHostExec       = "host_exec"
	riskClassRemoteScript   = "remote_script"
)

func NormalizeModeName(mode string) string {
	normalized := policy.NormalizeProfileName(mode)
	if strings.TrimSpace(normalized) == "" {
		return ModeDefault
	}
	return normalized
}

// NewRegistry registers the default workspace-write policy under its canonical name.
func NewRegistry() (*policy.MemoryRegistry, error) {
	return policy.NewMemory(WorkspaceWriteMode())
}

// WorkspaceWriteMode applies the built-in tool rules and default workspace
// sandbox constraints. Embedders exposing commands under other names must
// apply command-specific restrictions in their policy.
func WorkspaceWriteMode() policy.Mode {
	return policy.NamedMode{
		ID: ModeWorkspaceWrite,
		Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
			def := workspaceWriteConstraints(input.Options)
			if input.ReadPathArgument != "" {
				if err := ensureReadPathsOutsideDefaultHiddenRoots(input); err != nil {
					return policyErrorOrDeny(err)
				}
			}
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

// MCPApprovalDecision requests one of the four executable MCP scopes. ACP
// carries Session under allow_once kind and Cancel under reject_once kind;
// the stable option IDs preserve the distinct choices through every Surface.
func MCPApprovalDecision(input policy.ToolContext, constraints sandbox.Constraints) (policy.Decision, error) {
	// ACP uses "other"; the MCP identity lives in trusted definition metadata.
	decision, err := askApproval("MCP tool call requires approval", constraints, input, "other", "")
	if err != nil {
		return policy.Decision{}, err
	}
	plugin, _ := input.Tool.Metadata[mcp.MetadataPluginID].(string)
	server, _ := input.Tool.Metadata[mcp.MetadataServer].(string)
	remote, _ := input.Tool.Metadata[mcp.MetadataTool].(string)
	decision.Approval.ToolCall.Title = "MCP " + strings.TrimSpace(plugin+"/"+server+"/"+remote)
	decision.Approval.Options = []session.ProtocolApprovalOption{
		{ID: "allow_once", Name: "Allow Once", Kind: "allow_once"},
		{ID: "allow_session", Name: "Allow this session", Kind: "allow_once"},
		{ID: "allow_always", Name: "Allow Always", Kind: "allow_always"},
		{ID: "cancel", Name: "Cancel", Kind: "reject_once"},
	}
	decision.Approval.Subject = mcpApprovalSubject(input)
	return decision, nil
}

// DangerFullAccessMode allows assembled tools on the Host without this preset's
// approval or command filtering. An embedding may add source requirements inside its final Policy. Callers
// must register this exceptional mode explicitly; it is not a substitute for
// sandbox isolation.
func DangerFullAccessMode() policy.Mode {
	return policy.NamedMode{
		ID: ModeDangerFullAccess,
		Decide: func(_ context.Context, _ policy.ToolContext) (policy.Decision, error) {
			return allow(hostExecutionConstraints()), nil
		},
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

func askApproval(reason string, constraints sandbox.Constraints, input policy.ToolContext, kind, titleArgument string) (policy.Decision, error) {
	name := toolName(input)
	call, err := policy.CallArgs(input.Call)
	if err != nil {
		return policy.Decision{}, err
	}
	title := name
	if titleArgument != "" {
		detail, _ := call[titleArgument].(string)
		if detail = strings.TrimSpace(detail); detail != "" {
			title += " " + detail
		}
	}
	decision := policy.Decision{
		Action:      policy.ActionAskApproval,
		Reason:      strings.TrimSpace(reason),
		Constraints: constraints,
		Approval: &session.ProtocolApproval{
			Subject: approvalSubject(input, constraints, call),
			ToolCall: session.ProtocolToolCall{
				ID:       strings.TrimSpace(input.Call.ID),
				Name:     name,
				Kind:     kind,
				Title:    title,
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

func workspaceWriteConstraints(opts policy.ModeOptions) sandbox.Constraints {
	devWriteRoots := defaultDeveloperWritableRoots()
	rules := make([]sandbox.PathRule, 0, 1+len(devWriteRoots)+len(opts.WritableRoots))
	appendRule := func(path string, access sandbox.PathAccess) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		rules = append(rules, sandbox.PathRule{Path: path, Access: access})
	}
	if runtime.GOOS != "windows" {
		appendRule(opts.TempRoot, sandbox.PathAccessReadWrite)
	}
	for _, path := range devWriteRoots {
		appendRule(path, sandbox.PathAccessReadWrite)
	}
	for _, path := range opts.WritableRoots {
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

func policyClass(input policy.ToolContext) builtinPolicyClass {
	switch toolName(input) {
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
