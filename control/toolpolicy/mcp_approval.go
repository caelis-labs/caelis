package toolpolicy

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

func mcpApproval(input policy.ToolContext) (*session.ProtocolApproval, error) {
	args, err := policy.CallArgs(input.Call)
	if err != nil {
		return nil, err
	}
	plugin, _ := input.Tool.Metadata[mcp.MetadataPluginID].(string)
	server, _ := input.Tool.Metadata[mcp.MetadataServer].(string)
	remote, _ := input.Tool.Metadata[mcp.MetadataTool].(string)
	target := session.ApprovalField{State: session.ApprovalFieldUnavailable}
	if strings.TrimSpace(server) != "" && strings.TrimSpace(remote) != "" {
		path := strings.Join([]string{strings.TrimSpace(plugin), strings.TrimSpace(server), strings.TrimSpace(remote)}, "/")
		target = session.KnownApprovalField(strings.Trim(path, "/"))
	}
	return &session.ProtocolApproval{
		ToolCall: session.ProtocolToolCall{
			ID:       strings.TrimSpace(input.Call.ID),
			Name:     input.Tool.Name,
			Kind:     "other",
			Title:    "MCP " + strings.TrimSpace(plugin+"/"+server+"/"+remote),
			Status:   "pending",
			RawInput: args,
		},
		// ACP uses allow_once for Session and reject_once for Cancel. Their IDs
		// preserve the distinct choices through approval settlement.
		Options: []session.ProtocolApprovalOption{
			{ID: "allow_once", Name: "Allow Once", Kind: "allow_once"},
			{ID: "allow_session", Name: "Allow this session", Kind: "allow_once"},
			{ID: "allow_always", Name: "Allow Always", Kind: "allow_always"},
			{ID: "cancel", Name: "Cancel", Kind: "reject_once"},
		},
		Subject: &session.ApprovalSubject{
			Kind:      "app_access",
			Tool:      session.KnownApprovalField(input.Tool.Name),
			Operation: session.KnownApprovalField("call app tool"),
			Targets:   []session.ApprovalField{target, session.KnownApprovalField(input.Session.CWD)},
			Effect:    session.ApprovalField{State: session.ApprovalFieldUnavailable},
			Scope:     session.KnownApprovalField("this ready MCP tool, workspace and configuration"),
			Grants: []session.ApprovalGrant{
				{OptionID: "allow_once", Scope: session.KnownApprovalField("this invocation")},
				{OptionID: "allow_session", Scope: session.KnownApprovalField("this session")},
				{OptionID: "allow_always", Scope: session.KnownApprovalField("this workspace and configuration (persistent)")},
				{OptionID: "cancel", Scope: session.KnownApprovalField("no grant")},
			},
		},
	}, nil
}
