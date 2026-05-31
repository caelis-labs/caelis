package projector

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

func TestProjectToolResultTerminalMetadata(t *testing.T) {
	updates, err := (Projector{}).ProjectEvent(session.Event{
		SessionID: "s1",
		Type:      session.EventToolResult,
		Tool: &session.ToolEvent{
			ID:     "call-1",
			Name:   "run_command",
			Status: session.ToolCompleted,
			Input:  map[string]any{"command": "printf ok", "cwd": "/tmp/work"},
			Output: map[string]any{
				"task_id":     "host-1",
				"terminal_id": "host-1",
				"stdout":      "ok\n",
				"exit_code":   0,
			},
			Meta: map[string]any{
				"caelis": map[string]any{
					"runtime": map[string]any{
						"task": map[string]any{
							"task_id":          "host-1",
							"terminal_id":      "host-1",
							"state":            "completed",
							"stdout_cursor":    3,
							"output_truncated": true,
						},
					},
				},
			},
		},
	})
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
	if len(update.Content) != 1 || update.Content[0].Type != "terminal" || update.Content[0].TerminalID != "host-1" {
		t.Fatalf("content = %#v, want terminal marker", update.Content)
	}
	info, ok := update.Meta["terminal_info"].(map[string]any)
	if !ok || info["terminal_id"] != "host-1" || info["command"] != "printf ok" || info["cwd"] != "/tmp/work" {
		t.Fatalf("terminal_info = %#v", update.Meta["terminal_info"])
	}
	output, ok := update.Meta["terminal_output"].(map[string]any)
	if !ok || output["terminal_id"] != "host-1" || output["data"] != "ok\n" {
		t.Fatalf("terminal_output = %#v", update.Meta["terminal_output"])
	}
	if output["stdout_cursor"] != 3 || output["output_truncated"] != true {
		t.Fatalf("terminal_output = %#v, want runtime task output hints", output)
	}
	exit, ok := update.Meta["terminal_exit"].(map[string]any)
	if !ok || exit["terminal_id"] != "host-1" || exit["exit_code"] != 0 {
		t.Fatalf("terminal_exit = %#v", update.Meta["terminal_exit"])
	}
}

func TestProjectToolResultTerminalOutputFromRuntimeTaskPreview(t *testing.T) {
	updates, err := (Projector{}).ProjectEvent(session.Event{
		SessionID: "s1",
		Type:      session.EventToolResult,
		Tool: &session.ToolEvent{
			ID:     "call-1",
			Name:   "task",
			Status: session.ToolRunning,
			Output: map[string]any{
				"task_id": "host-1",
				"state":   "running",
			},
			Meta: map[string]any{
				"caelis": map[string]any{
					"runtime": map[string]any{
						"task": map[string]any{
							"task_id":        "host-1",
							"terminal_id":    "term-1",
							"output_preview": "still running\n",
							"stdout_cursor":  14,
						},
					},
				},
			},
		},
	})
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
	if len(update.Content) != 1 || update.Content[0].TerminalID != "term-1" {
		t.Fatalf("content = %#v, want terminal marker from runtime meta", update.Content)
	}
	output, ok := update.Meta["terminal_output"].(map[string]any)
	if !ok || output["data"] != "still running\n" || output["stdout_cursor"] != 14 {
		t.Fatalf("terminal_output = %#v, want preview-backed output", update.Meta["terminal_output"])
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
