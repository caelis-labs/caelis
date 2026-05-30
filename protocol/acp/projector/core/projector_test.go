package core

import (
	"encoding/json"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestProjectAssistantReasoningAndText(t *testing.T) {
	event := session.Event{
		SessionID: "s1",
		Type:      session.EventAssistant,
		Message: &model.Message{
			Role: model.RoleAssistant,
			Parts: []model.Part{
				model.NewReasoningPart("thinking", model.ReasoningVisible),
				model.NewTextPart("answer"),
			},
		},
	}
	updates, err := (Projector{}).ProjectEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 2 {
		t.Fatalf("updates = %d, want 2", len(updates))
	}
	thought, ok := updates[0].(schema.ContentChunk)
	if !ok || thought.SessionUpdate != schema.UpdateAgentThought {
		t.Fatalf("first update = %#v, want agent thought", updates[0])
	}
	message, ok := updates[1].(schema.ContentChunk)
	if !ok || message.SessionUpdate != schema.UpdateAgentMessage {
		t.Fatalf("second update = %#v, want agent message", updates[1])
	}
}

func TestProjectToolCallAnchor(t *testing.T) {
	updates, err := (Projector{}).ProjectEvent(session.Event{
		SessionID: "s1",
		Type:      session.EventToolCall,
		Tool: &session.ToolEvent{
			ID:     "call-1",
			Name:   "READ",
			Status: session.ToolStarted,
			Input:  rawInputMap(json.RawMessage(`{"path":"a.txt"}`)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(updates))
	}
	call, ok := updates[0].(schema.ToolCall)
	if !ok {
		t.Fatalf("update = %#v, want tool call", updates[0])
	}
	if call.ToolCallID != "call-1" || call.Kind != schema.ToolKindRead || call.Status != schema.ToolStatusPending {
		t.Fatalf("tool call = %#v", call)
	}
	rawInput, ok := call.RawInput.(map[string]any)
	if !ok || rawInput["path"] != "a.txt" {
		t.Fatalf("raw input = %#v, want path", call.RawInput)
	}
}

func TestProjectToolResultUpdate(t *testing.T) {
	event := session.Event{
		SessionID: "s1",
		Type:      session.EventToolResult,
		Tool: &session.ToolEvent{
			ID:     "call-1",
			Name:   "shell",
			Status: session.ToolCompleted,
			Output: map[string]any{"exit_code": 0},
			Meta: map[string]any{
				"caelis": map[string]any{
					"runtime": map[string]any{
						"tool": map[string]any{"path": "demo.txt"},
					},
				},
			},
			Content: []session.ToolContent{{
				Type: "text",
				Text: "ok",
			}},
		},
	}
	updates, err := (Projector{}).ProjectEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(updates))
	}
	update, ok := updates[0].(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %#v, want ToolCallUpdate", updates[0])
	}
	if update.ToolCallID != "call-1" || update.Status == nil || *update.Status != schema.ToolStatusCompleted {
		t.Fatalf("tool update = %#v", update)
	}
	if len(update.Content) != 1 || update.Content[0].Content != "ok" {
		t.Fatalf("content = %#v, want ok", update.Content)
	}
	rawOutput, ok := update.RawOutput.(map[string]any)
	if !ok || rawOutput["exit_code"] != 0 {
		t.Fatalf("raw output = %#v, want exit_code", update.RawOutput)
	}
	if update.Meta["caelis"] == nil {
		t.Fatalf("meta = %#v, want caelis runtime metadata", update.Meta)
	}
}

func TestProjectPlanAndApproval(t *testing.T) {
	planUpdates, err := (Projector{}).ProjectEvent(session.Event{
		SessionID: "s1",
		Type:      session.EventPlan,
		Plan: []session.PlanEntry{{
			Content: "do it",
			Status:  "in_progress",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planUpdates) != 1 {
		t.Fatalf("plan updates = %d, want 1", len(planUpdates))
	}
	plan, ok := planUpdates[0].(schema.PlanUpdate)
	if !ok || len(plan.Entries) != 1 || plan.Entries[0].Content != "do it" {
		t.Fatalf("plan update = %#v", planUpdates[0])
	}

	permission, ok, err := (Projector{}).ProjectPermissionRequest(session.Event{
		SessionID: "s1",
		Type:      session.EventApproval,
		Approval: &session.ApprovalEvent{
			Status: session.ApprovalPending,
			Tool: &session.ToolEvent{
				ID:     "call-1",
				Name:   "shell",
				Status: session.ToolWaitingApproval,
				Input:  map[string]any{"cmd": "go test ./..."},
			},
			Options: []session.ApprovalOption{{
				ID:   "allow",
				Name: "Allow",
				Kind: schema.PermAllowOnce,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("permission request not projected")
	}
	if permission.SessionID != "s1" || permission.ToolCall.ToolCallID != "call-1" {
		t.Fatalf("permission = %#v", permission)
	}
	if permission.ToolCall.Status == nil || *permission.ToolCall.Status != schema.ToolStatusInProgress {
		t.Fatalf("permission tool status = %#v", permission.ToolCall.Status)
	}
	if len(permission.Options) != 1 || permission.Options[0].OptionID != "allow" {
		t.Fatalf("permission options = %#v", permission.Options)
	}
}
