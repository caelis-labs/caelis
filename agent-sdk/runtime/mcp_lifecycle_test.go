package runtime

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestMCPFixedUnknownRecoveryKeepsOuterCallWithoutReplay(t *testing.T) {
	service, active := newToolFactTestSession(t, "fixed-unknown")
	original := model.ToolCall{ID: "provider-operation", Name: tool.MCPCallToolName, Args: `{"tool_name":"docs__read","arguments":{"number":9007199254740993}}`}
	input := map[string]any{"number": json.Number("9007199254740993")}
	message := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{original}, "")
	fact := &session.Event{ID: "original-call", Type: session.EventTypeToolCall, Scope: &session.EventScope{TurnID: "original-turn"}, Message: &message, Tool: &session.EventTool{ID: original.ID, Name: "docs__read", Input: input, ModelCall: &original, Binding: &tool.Binding{Name: "docs__read", Identity: "identity", Version: "v1"}, Execution: tool.InvocationContext{SessionID: active.SessionID, TurnID: "original-turn", ItemID: "original-occurrence"}}}
	if _, err := service.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: fact}); err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{sessions: service, clock: time.Now}
	fence, err := service.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "new-owner"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := session.ContextWithRuntimeFence(t.Context(), fence)
	effects := 0
	if err := rt.repairToolPairs(ctx, active.SessionRef); err != nil {
		t.Fatal(err)
	}
	events, err := service.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if effects != 0 || last.Tool.Outcome != "unknown_outcome" || last.Tool.Name != "docs__read" || last.Tool.ModelCall == nil || last.Message.ToolResults()[0].Name != tool.MCPCallToolName || fmt.Sprint(last.Tool.Input["number"]) != "9007199254740993" {
		t.Fatalf("unknown recovery changed identity or replayed: effects=%d event=%+v", effects, last.Tool)
	}
}
