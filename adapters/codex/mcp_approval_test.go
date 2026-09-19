package codex

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/adapters/codex/internal/appserver"
)

type mcpPermissionClient struct {
	recordingACPClient
	request *acp.RequestPermissionRequest
	outcome acp.RequestPermissionOutcome
}

func (c *mcpPermissionClient) RequestPermission(_ context.Context, request acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	c.request = &request
	return acp.RequestPermissionResponse{Outcome: c.outcome}, nil
}

func TestMCPToolApprovalUsesACPDecision(t *testing.T) {
	for _, tc := range []struct{ name, meta, mode, schema, decision, want string }{
		{"current allow", `"codex_approval_kind":"mcp_tool_call","codex_request_type":"approval_request","tool_name":"StartThread"`, "form", `{"type":"object","properties":{}}`, "allow_once", "accept"},
		{"0.153.4 allow", `"codex_approval_kind":"mcp_tool_call"`, "form", `{"type":"object","properties":{}}`, "allow_once", "accept"},
		{"reject", `"codex_approval_kind":"mcp_tool_call"`, "form", `{"type":"object"}`, "decline", "decline"},
		{"cancel", `"codex_approval_kind":"mcp_tool_call"`, "form", `{"type":"object"}`, "", "cancel"},
		{"unknown form", `"tool_name":"StartThread"`, "form", `{"type":"object"}`, "", "cancel"},
		{"requires input", `"codex_approval_kind":"mcp_tool_call"`, "form", `{"type":"object","properties":{"secret":{"type":"string"}}}`, "", "cancel"},
		{"url", `"codex_approval_kind":"mcp_tool_call"`, "url", `{"type":"object"}`, "", "cancel"},
		{"suggestion", `"codex_approval_kind":"tool_suggestion"`, "form", `{"type":"object"}`, "", "cancel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			toAgent, fromClient := io.Pipe()
			toClient, fromAgent := io.Pipe()
			defer toAgent.Close()
			defer fromClient.Close()
			defer toClient.Close()
			defer fromAgent.Close()
			recorder := &mcpPermissionClient{outcome: acp.NewRequestPermissionOutcomeCancelled()}
			if tc.decision != "" {
				recorder.outcome = acp.NewRequestPermissionOutcomeSelected(acp.PermissionOptionId(tc.decision))
			}
			a := &agent{}
			a.connection = acp.NewAgentSideConnection(a, fromAgent, toAgent)
			defer a.connection.Close()
			client := acp.NewClientSideConnection(recorder, fromClient, toClient)
			defer client.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			route := &sessionRoute{agent: a, state: &sessionState{threadID: "thread"}}
			raw := `{"serverName":"caelis-collaboration","message":"Create a collaborator","mode":"` + tc.mode + `","requestedSchema":` + tc.schema + `,"_meta":{` + tc.meta + `,"tool_params":{"agent":"breeze"}}}`
			result, err := route.handleRequest(ctx, appserver.Request{ID: json.RawMessage(`7`), Method: "mcpServer/elicitation/request", Params: json.RawMessage(raw)})
			if err != nil || result.(map[string]any)["action"] != tc.want {
				t.Fatalf("approval = %#v, %v", result, err)
			}
			if tc.decision != "" {
				if recorder.request == nil || recorder.request.SessionId != "thread" || len(recorder.request.Options) != 2 {
					t.Fatalf("missing scoped ACP decision: %#v", recorder.request)
				}
				input := recorder.request.ToolCall.RawInput.(map[string]any)
				if input["mcp_server"] != "caelis-collaboration" || input["arguments"].(map[string]any)["agent"] != "breeze" {
					t.Fatalf("approval lost invocation: %#v", input)
				}
			} else if tc.name != "cancel" && recorder.request != nil {
				t.Fatal("arbitrary elicitation became a tool approval")
			}
		})
	}
}
