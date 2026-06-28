package eventstream

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestReplayCursorPrefersProjectionID(t *testing.T) {
	t.Parallel()

	env := Envelope{Cursor: "live-1", ProjectionID: "acp-projection:abc:0"}
	if got := ReplayCursor(env); got != "acp-projection:abc:0" {
		t.Fatalf("ReplayCursor() = %q, want projection id", got)
	}
	if got := SSEEventID(env); got != "live-1" {
		t.Fatalf("SSEEventID() = %q, want live cursor", got)
	}
}

func TestNormalizeRunStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		status          string
		waitingApproval bool
		hasActiveTurn   bool
		wantStatus      string
		wantWaiting     bool
	}{
		{
			name:          "empty active",
			hasActiveTurn: true,
			wantStatus:    RunStateRunning,
		},
		{
			name:       "empty inactive",
			wantStatus: RunStateIdle,
		},
		{
			name:            "waiting flag wins",
			status:          RunStateRunning,
			waitingApproval: true,
			hasActiveTurn:   true,
			wantStatus:      RunStateWaitingApproval,
			wantWaiting:     true,
		},
		{
			name:        "waiting status",
			status:      " WAITING_APPROVAL ",
			wantStatus:  RunStateWaitingApproval,
			wantWaiting: true,
		},
		{
			name:       "terminal status lowercased",
			status:     "FAILED",
			wantStatus: RunStateFailed,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			status, waiting := NormalizeRunStatus(tt.status, tt.waitingApproval, tt.hasActiveTurn)
			if status != tt.wantStatus || waiting != tt.wantWaiting {
				t.Fatalf("NormalizeRunStatus() = %q, %v; want %q, %v", status, waiting, tt.wantStatus, tt.wantWaiting)
			}
		})
	}
}

func TestToolCallFromEnvelopeReturnsACPToolCall(t *testing.T) {
	t.Parallel()

	env := Envelope{
		Kind: KindSessionUpdate,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "call-1",
			Title:         "Run tests",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"command": "go test ./..."},
			Content: []schema.ToolCallContent{{
				Type:       "terminal",
				TerminalID: "term-1",
			}},
			Meta: map[string]any{"caelis": map[string]any{"runtime": map[string]any{"task_id": "task-1"}}},
		},
	}

	tool, ok := ToolCallFromEnvelope(env)
	if !ok {
		t.Fatal("ToolCallFromEnvelope() ok = false, want true")
	}
	if tool.ToolCallID != "call-1" || tool.Title != "Run tests" || tool.Kind != schema.ToolKindExecute || tool.Status != schema.ToolStatusInProgress {
		t.Fatalf("tool call = %#v", tool)
	}
	if len(tool.Content) != 1 || tool.Content[0].TerminalID != "term-1" {
		t.Fatalf("tool content = %#v", tool.Content)
	}
	tool.RawInput.(map[string]any)["command"] = "mutated"
	source := env.Update.(schema.ToolCall)
	if source.RawInput.(map[string]any)["command"] != "go test ./..." {
		t.Fatalf("source tool call mutated: %#v", source.RawInput)
	}
}

func TestToolCallUpdateFromEnvelopeReturnsACPToolCallUpdate(t *testing.T) {
	t.Parallel()

	title := "RUN_COMMAND"
	status := schema.ToolStatusInProgress
	env := Envelope{
		Kind: KindSessionUpdate,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Title:         &title,
			Status:        &status,
			RawInput:      map[string]any{"command": "make test"},
		},
	}

	update, ok := ToolCallUpdateFromEnvelope(env)
	if !ok {
		t.Fatal("ToolCallUpdateFromEnvelope() ok = false, want true")
	}
	if update.ToolCallID != "call-1" || update.Title == nil || *update.Title != "RUN_COMMAND" || update.Status == nil || *update.Status != schema.ToolStatusInProgress {
		t.Fatalf("tool call update = %#v", update)
	}
	*update.Title = "mutated"
	update.RawInput.(map[string]any)["command"] = "mutated"
	source := env.Update.(schema.ToolCallUpdate)
	if source.Title == nil || *source.Title != "RUN_COMMAND" || source.RawInput.(map[string]any)["command"] != "make test" {
		t.Fatalf("source tool update mutated: %#v", source)
	}
}

func TestPermissionRequestFromEnvelopeReturnsACPRequest(t *testing.T) {
	t.Parallel()

	title := "RUN_COMMAND"
	status := schema.ToolStatusPending
	env := Envelope{
		Kind: KindRequestPermission,
		Permission: &schema.RequestPermissionRequest{
			SessionID: "session-1",
			ToolCall: schema.ToolCallUpdate{
				SessionUpdate: schema.UpdateToolCallInfo,
				ToolCallID:    "call-approval",
				Title:         &title,
				Status:        &status,
				RawInput:      map[string]any{"command": "make test"},
			},
			Options: []schema.PermissionOption{{OptionID: schema.PermAllowOnce, Name: "Allow once", Kind: schema.PermAllowOnce}},
			Meta:    map[string]any{"caelis": map[string]any{"approval": map[string]any{"mode": "manual"}}},
		},
	}

	request, ok := PermissionRequestFromEnvelope(env)
	if !ok {
		t.Fatal("PermissionRequestFromEnvelope() ok = false, want true")
	}
	if request.SessionID != "session-1" || request.ToolCall.ToolCallID != "call-approval" {
		t.Fatalf("permission request = %#v", request)
	}
	if len(request.Options) != 1 || request.Options[0].OptionID != schema.PermAllowOnce {
		t.Fatalf("permission options = %#v", request.Options)
	}
	request.Options[0].OptionID = "mutated"
	request.ToolCall.RawInput.(map[string]any)["command"] = "mutated"
	if env.Permission.Options[0].OptionID != schema.PermAllowOnce || env.Permission.ToolCall.RawInput.(map[string]any)["command"] != "make test" {
		t.Fatalf("source permission request mutated: %#v", env.Permission)
	}
}

func TestRunStateFromEnvelopeProjectsApprovalWait(t *testing.T) {
	t.Parallel()

	env := Envelope{
		Kind:      KindRequestPermission,
		SessionID: "session-1",
		HandleID:  "handle-1",
		RunID:     "run-1",
		TurnID:    "turn-1",
		Permission: &schema.RequestPermissionRequest{ToolCall: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
		}},
	}
	state, ok := RunStateFromEnvelope(env)
	if !ok {
		t.Fatal("RunStateFromEnvelope() ok = false, want true")
	}
	if state.Status != RunStateWaitingApproval || !state.WaitingApproval || !state.HasActiveTurn {
		t.Fatalf("run state = %#v, want waiting approval", state)
	}
}
