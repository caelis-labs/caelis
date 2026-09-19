package codex

import (
	"context"
	"encoding/json"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/adapters/codex/internal/appserver"
)

// approveMCPTool translates Codex's explicit, empty-form tool approval into ACP
// permission. Arbitrary forms, URLs and tool suggestions still require input
// this adapter cannot supply, so they retain the cancellation fallback.
func (r *sessionRoute) approveMCPTool(ctx context.Context, request appserver.Request) (any, error) {
	var p struct {
		Mode    string `json:"mode"`
		Message string `json:"message"`
		Server  string `json:"serverName"`
		Meta    struct {
			Kind        string         `json:"codex_approval_kind"`
			RequestType string         `json:"codex_request_type"`
			Tool        string         `json:"tool_name"`
			Arguments   map[string]any `json:"tool_params"`
		} `json:"_meta"`
		Schema struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		} `json:"requestedSchema"`
	}
	if json.Unmarshal(request.Params, &p) != nil || p.Mode != "form" || p.Meta.Kind != "mcp_tool_call" || (p.Meta.RequestType != "" && p.Meta.RequestType != "approval_request") || p.Server == "" || p.Schema.Type != "object" || len(p.Schema.Properties) > 0 || len(p.Schema.Required) > 0 {
		return appServerFallbackResponse(request.Method)
	}
	// Codex 0.153.4 supplies the invocation in Message, without tool_name or
	// codex_request_type. Keep that presentation until the minimum supported
	// app-server version guarantees structured tool names; never infer authority
	// from this text. Approval still requires a user decision and the MCP grant.
	title := p.Message
	if title == "" {
		title = p.Server + " / " + firstNonEmpty(p.Meta.Tool, "MCP tool")
	}
	kind := acp.ToolKindOther
	response, err := r.agent.connection.RequestPermission(ctx, acp.RequestPermissionRequest{
		SessionId: acp.SessionId(r.state.threadID),
		Options:   []acp.PermissionOption{{OptionId: "allow_once", Name: "Allow this call", Kind: acp.PermissionOptionKindAllowOnce}, {OptionId: "decline", Name: "Reject this call", Kind: acp.PermissionOptionKindRejectOnce}},
		ToolCall:  acp.ToolCallUpdate{ToolCallId: acp.ToolCallId(stableID(r.state.threadID, string(request.ID), "mcp-approval")), Title: &title, Kind: &kind, RawInput: map[string]any{"mcp_server": p.Server, "mcp_tool": p.Meta.Tool, "arguments": p.Meta.Arguments}},
	})
	if err != nil || response.Outcome.Selected == nil {
		return appServerFallbackResponse(request.Method)
	}
	if response.Outcome.Selected.OptionId == "allow_once" {
		return map[string]any{"action": "accept", "content": map[string]any{}}, nil
	}
	return map[string]any{"action": "decline", "content": nil}, nil
}
