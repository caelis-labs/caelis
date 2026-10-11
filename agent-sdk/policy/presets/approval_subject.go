package presets

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/web"
)

// The policy owner uses the same interpreted arguments and constraints it
// authorizes. No title, prose or arbitrary extension input establishes facts.
func approvalSubject(input policy.ToolContext, constraints sandbox.Constraints, args map[string]any) *session.ApprovalSubject {
	out := &session.ApprovalSubject{
		Kind: "opaque", Tool: session.KnownApprovalField(input.Tool.Name),
		Operation: session.ApprovalField{State: session.ApprovalFieldOpaque},
		Targets:   []session.ApprovalField{{State: session.ApprovalFieldOpaque}},
		Effect:    session.ApprovalField{State: session.ApprovalFieldOpaque},
		Scope:     session.ApprovalField{State: session.ApprovalFieldOpaque},
	}
	field := func(name, text string) session.ApprovalField {
		// Use the interpreted argument's actual shape as well as its property
		// annotation: a path list may mark individual array items writeOnly.
		_, marked := approval.RedactInputForDisplay(map[string]any{name: args[name]}, input.Tool.InputSchema)
		if marked || approval.InputFieldSensitive(input.Tool.InputSchema, name) {
			return session.ApprovalField{State: session.ApprovalFieldOpaque, Sensitive: true}
		}
		return session.KnownApprovalField(text)
	}
	switch policyClass(input) {
	case builtinPolicyCommand:
		command, _ := args["command"].(string)
		cwd, _ := args["workdir"].(string)
		if strings.TrimSpace(cwd) == "" {
			cwd = input.Session.CWD
		}
		out.Kind = "command"
		out.Operation = session.KnownApprovalField("execute")
		out.Targets = []session.ApprovalField{field("command", command), field("workdir", cwd)}
		out.Effect = session.KnownApprovalField("run the supplied command")
		out.Scope = executionApprovalScope(constraints)
	case builtinPolicyReadPath, builtinPolicySearchPath, builtinPolicyGlobPath, builtinPolicyWritePath:
		paths, err := candidatePaths(input)
		if err != nil {
			break
		}
		out.Kind = "file"
		operation, source := "read", "path"
		switch policyClass(input) {
		case builtinPolicyWritePath:
			operation = "write"
		case builtinPolicySearchPath:
			operation = "search"
		case builtinPolicyGlobPath:
			operation = "match"
			if len(stringValues(args["path"])) == 0 {
				source = "pattern"
			}
		}
		out.Operation = session.KnownApprovalField(operation)
		out.Effect = session.KnownApprovalField(operation + " the exact target paths")
		out.Targets = nil
		for _, path := range paths {
			out.Targets = append(out.Targets, field(source, path))
		}
		out.Scope = session.KnownApprovalField("exact target paths for this invocation")
	default:
		if input.Tool.Name == web.FetchToolName {
			url, _ := args["url"].(string)
			out.Kind = "network"
			out.Operation = session.KnownApprovalField("fetch")
			out.Targets = []session.ApprovalField{field("url", url)}
			out.Effect = session.KnownApprovalField("fetch the supplied URL")
			out.Scope = executionApprovalScope(constraints)
		}
	}
	out.Grants = []session.ApprovalGrant{
		{OptionID: "allow_once", Scope: session.KnownApprovalField("this invocation")},
		{OptionID: "reject_once", Scope: session.KnownApprovalField("no grant")},
	}
	return session.CloneApprovalSubject(out)
}

func executionApprovalScope(constraints sandbox.Constraints) session.ApprovalField {
	constraints = sandbox.NormalizeConstraints(constraints)
	if constraints.Route == "" || constraints.Permission == "" {
		return session.ApprovalField{State: session.ApprovalFieldUnavailable}
	}
	return session.KnownApprovalField("route: " + string(constraints.Route) + "; permission: " + string(constraints.Permission) + "; network: " + string(constraints.Network))
}

func mcpApprovalSubject(input policy.ToolContext) *session.ApprovalSubject {
	metadata := func(key string) string {
		value, _ := input.Tool.Metadata[key].(string)
		return strings.TrimSpace(value)
	}
	server, remote := metadata(tool.MetadataMCPServer), metadata(tool.MetadataMCPTool)
	target := session.ApprovalField{State: session.ApprovalFieldUnavailable}
	if server != "" && remote != "" {
		target = session.KnownApprovalField(strings.Trim(strings.Join([]string{metadata(tool.MetadataPluginID), server, remote}, "/"), "/"))
	}
	effect := session.ApprovalField{State: session.ApprovalFieldUnavailable}
	switch input.Tool.EffectClass {
	case tool.EffectReadOnly, tool.EffectIdempotent, tool.EffectNonIdempotent:
		effect = session.KnownApprovalField(string(input.Tool.EffectClass))
	}
	return session.CloneApprovalSubject(&session.ApprovalSubject{
		Kind: "app_access", Tool: session.KnownApprovalField(input.Tool.Name),
		Operation: session.KnownApprovalField("call app tool"),
		Targets:   []session.ApprovalField{target, session.KnownApprovalField(input.Session.CWD)},
		Effect:    effect,
		Scope:     session.KnownApprovalField("this ready MCP tool, workspace and configuration"),
		Grants: []session.ApprovalGrant{
			{OptionID: "allow_once", Scope: session.KnownApprovalField("this invocation")},
			{OptionID: "allow_session", Scope: session.KnownApprovalField("this session")},
			{OptionID: "allow_always", Scope: session.KnownApprovalField("this workspace and configuration (persistent)")},
			{OptionID: "cancel", Scope: session.KnownApprovalField("no grant")},
		},
	})
}
