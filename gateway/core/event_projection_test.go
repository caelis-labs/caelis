package core

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
)

func TestCanonicalApprovalPayloadTableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		req  *sdkruntime.ApprovalRequest
		want func(*testing.T, *ApprovalPayload)
	}{
		{
			name: "runtime call fallback",
			req: &sdkruntime.ApprovalRequest{
				Call: sdktool.Call{
					Name:  "bash",
					Input: json.RawMessage(`{"command":"echo hi"}`),
				},
			},
			want: func(t *testing.T, payload *ApprovalPayload) {
				t.Helper()
				if payload == nil {
					t.Fatal("canonicalApprovalPayload() = nil, want payload")
					return
				}
				if payload.ToolName != "bash" {
					t.Fatalf("payload.ToolName = %q, want %q", payload.ToolName, "bash")
				}
				if payload.RawInput["command"] != "echo hi" {
					t.Fatalf("payload.RawInput = %#v, want command", payload.RawInput)
				}
				if payload.Status != ApprovalStatusPending {
					t.Fatalf("payload.Status = %q, want %q", payload.Status, ApprovalStatusPending)
				}
			},
		},
		{
			name: "protocol approval options",
			req: &sdkruntime.ApprovalRequest{
				Approval: &sdksession.ProtocolApproval{
					ToolCall: sdksession.ProtocolToolCall{
						ID:       "call-bash-approval",
						Name:     "BASH",
						RawInput: map[string]any{"command": "rm -rf /tmp/demo"},
					},
					Options: []sdksession.ProtocolApprovalOption{
						{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
					},
				},
			},
			want: func(t *testing.T, payload *ApprovalPayload) {
				t.Helper()
				if payload == nil {
					t.Fatal("canonicalApprovalPayload() = nil, want payload")
					return
				}
				if payload.ToolName != "BASH" {
					t.Fatalf("payload.ToolName = %q, want %q", payload.ToolName, "BASH")
				}
				if payload.ToolCallID != "call-bash-approval" {
					t.Fatalf("payload.ToolCallID = %q, want %q", payload.ToolCallID, "call-bash-approval")
				}
				if payload.RawInput["command"] != "rm -rf /tmp/demo" {
					t.Fatalf("payload.RawInput = %#v, want command", payload.RawInput)
				}
				if len(payload.Options) != 1 || payload.Options[0].ID != "allow_once" {
					t.Fatalf("payload.Options = %#v, want allow_once option", payload.Options)
				}
				if payload.Status != ApprovalStatusPending {
					t.Fatalf("payload.Status = %q, want %q", payload.Status, ApprovalStatusPending)
				}
			},
		},
		{
			name: "permission metadata",
			req: &sdkruntime.ApprovalRequest{
				Tool: sdktool.Definition{Name: "BASH"},
				Call: sdktool.Call{
					Name:  "BASH",
					Input: json.RawMessage(`{"command":"make generate"}`),
				},
				Metadata: map[string]any{
					"approval_reason":     "additional sandbox permissions require user approval",
					"sandbox_permissions": "with_additional_permissions",
					"justification":       "Do you want to grant a cache path?",
					"additional_permissions": map[string]any{
						"network": map[string]any{"enabled": true},
					},
				},
			},
			want: func(t *testing.T, payload *ApprovalPayload) {
				t.Helper()
				if payload == nil {
					t.Fatal("canonicalApprovalPayload() = nil, want payload")
				}
				if payload.Reason != "additional sandbox permissions require user approval" {
					t.Fatalf("payload.Reason = %q", payload.Reason)
				}
				if payload.Justification != "Do you want to grant a cache path?" {
					t.Fatalf("payload.Justification = %q", payload.Justification)
				}
				if payload.SandboxPermissions != "with_additional_permissions" {
					t.Fatalf("payload.SandboxPermissions = %q", payload.SandboxPermissions)
				}
				if payload.AdditionalPermissions["network"] == nil {
					t.Fatalf("payload.AdditionalPermissions = %#v, want network grant", payload.AdditionalPermissions)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.want(t, canonicalApprovalPayload(tt.req))
		})
	}
}

func TestProjectSessionEventPreservesMinimalCaelisMeta(t *testing.T) {
	t.Parallel()

	env, ok := ProjectSessionEvent(sdksession.SessionRef{SessionID: "sess-1"}, &sdksession.Event{
		ID:   "tool-call-task",
		Type: sdksession.EventTypeToolCall,
		Meta: map[string]any{
			"caelis": map[string]any{
				"version": 1,
				"runtime": map[string]any{
					"tool": map[string]any{
						"name": "TASK",
					},
				},
			},
		},
		Protocol: &sdksession.EventProtocol{
			ToolCall: &sdksession.ProtocolToolCall{
				ID:     "call-task",
				Name:   "TASK",
				Status: "running",
			},
		},
	})
	if !ok || env.Event.ToolCall == nil {
		t.Fatalf("ProjectSessionEvent() = (%#v, %v), want tool call", env, ok)
	}
	caelis, ok := env.Event.Meta["caelis"].(map[string]any)
	if !ok {
		t.Fatalf("event.Meta = %#v, want caelis meta", env.Event.Meta)
	}
	if _, ok := caelis["display"]; ok {
		t.Fatalf("meta.caelis = %#v, should not synthesize display data", caelis)
	}
	runtimeMeta, ok := caelis["runtime"].(map[string]any)
	if !ok {
		t.Fatalf("meta.caelis = %#v, want runtime map", caelis)
	}
	tool, ok := runtimeMeta["tool"].(map[string]any)
	if !ok || tool["name"] != "TASK" {
		t.Fatalf("runtime.tool = %#v, want TASK", runtimeMeta["tool"])
	}
}

func TestProjectSessionEventParticipantPromptUsesTurnIDScope(t *testing.T) {
	t.Parallel()

	ref := sdksession.SessionRef{SessionID: "root-session"}
	for _, turnID := range []string{"participant-turn-1", "participant-turn-2"} {
		turnID := turnID
		t.Run(turnID, func(t *testing.T) {
			t.Parallel()

			env, ok := ProjectSessionEvent(ref, &sdksession.Event{
				ID:         turnID + "-assistant",
				Type:       sdksession.EventTypeAssistant,
				Text:       "done",
				Visibility: sdksession.VisibilityCanonical,
				Scope: &sdksession.EventScope{
					TurnID: turnID,
					Source: "acp_participant",
					Participant: sdksession.ParticipantRef{
						ID:   "participant-jeff",
						Kind: sdksession.ParticipantKindACP,
						Role: sdksession.ParticipantRoleSidecar,
					},
					ACP: sdksession.ACPRef{SessionID: "remote-jeff"},
				},
			})
			if !ok {
				t.Fatal("ProjectSessionEvent() ok = false, want true")
			}
			if env.Event.Origin == nil {
				t.Fatal("event.Origin = nil, want participant origin")
			}
			if env.Event.Origin.ScopeID != turnID {
				t.Fatalf("event.Origin.ScopeID = %q, want %q", env.Event.Origin.ScopeID, turnID)
			}
			if env.Event.Origin.ParticipantSessionID != "remote-jeff" {
				t.Fatalf("event.Origin.ParticipantSessionID = %q, want remote session preserved", env.Event.Origin.ParticipantSessionID)
			}
		})
	}
}

func TestProjectSessionEventsCanonicalPayloadsTableDriven(t *testing.T) {
	t.Parallel()

	reasoningMessage := sdkmodel.NewReasoningMessage(sdkmodel.RoleAssistant, "think through options", sdkmodel.ReasoningVisibilityVisible)
	spaceReasoningMessage := sdkmodel.NewReasoningMessage(sdkmodel.RoleAssistant, " ", sdkmodel.ReasoningVisibilityVisible)

	tests := []struct {
		name string
		ev   *sdksession.Event
		want func(*testing.T, EventEnvelope)
	}{
		{
			name: "assistant text",
			ev: &sdksession.Event{
				ID:         "assistant-1",
				Type:       sdksession.EventTypeAssistant,
				Text:       "done",
				Visibility: sdksession.VisibilityCanonical,
			},
			want: func(t *testing.T, env EventEnvelope) {
				t.Helper()
				if env.Event.Kind != EventKindAssistantMessage {
					t.Fatalf("event.Kind = %q, want %q", env.Event.Kind, EventKindAssistantMessage)
				}
				if env.Event.Narrative == nil {
					t.Fatal("event.Narrative = nil, want payload")
				}
				if env.Event.Narrative.Role != NarrativeRoleAssistant || env.Event.Narrative.Text != "done" || !env.Event.Narrative.Final {
					t.Fatalf("event.Narrative = %+v", env.Event.Narrative)
				}
			},
		},
		{
			name: "reasoning",
			ev: &sdksession.Event{
				ID:         "reasoning-1",
				Type:       sdksession.EventTypeAssistant,
				Text:       "think through options",
				Visibility: sdksession.VisibilityUIOnly,
				Message:    &reasoningMessage,
				Protocol: &sdksession.EventProtocol{
					UpdateType: string(sdksession.ProtocolUpdateTypeAgentThought),
				},
			},
			want: func(t *testing.T, env EventEnvelope) {
				t.Helper()
				if env.Event.Narrative == nil {
					t.Fatal("event.Narrative = nil, want payload")
				}
				if env.Event.Narrative.Text != "" {
					t.Fatalf("event.Narrative.Text = %q, want empty reasoning-only answer text", env.Event.Narrative.Text)
				}
				if env.Event.Narrative.ReasoningText != "think through options" {
					t.Fatalf("event.Narrative.ReasoningText = %q, want %q", env.Event.Narrative.ReasoningText, "think through options")
				}
			},
		},
		{
			name: "reasoning preserves boundary whitespace",
			ev: &sdksession.Event{
				ID:         "reasoning-space",
				Type:       sdksession.EventTypeAssistant,
				Text:       " ",
				Visibility: sdksession.VisibilityUIOnly,
				Message:    &spaceReasoningMessage,
				Protocol: &sdksession.EventProtocol{
					UpdateType: string(sdksession.ProtocolUpdateTypeAgentThought),
				},
			},
			want: func(t *testing.T, env EventEnvelope) {
				t.Helper()
				if env.Event.Narrative == nil {
					t.Fatal("event.Narrative = nil, want payload")
				}
				if env.Event.Narrative.ReasoningText != " " {
					t.Fatalf("event.Narrative.ReasoningText = %q, want single space", env.Event.Narrative.ReasoningText)
				}
			},
		},
		{
			name: "reasoning stream preserves whitespace without message",
			ev: &sdksession.Event{
				ID:         "reasoning-space-no-message",
				Type:       sdksession.EventTypeAssistant,
				Text:       " ",
				Visibility: sdksession.VisibilityUIOnly,
				Protocol: &sdksession.EventProtocol{
					UpdateType: string(sdksession.ProtocolUpdateTypeAgentThought),
				},
			},
			want: func(t *testing.T, env EventEnvelope) {
				t.Helper()
				if env.Event.Narrative == nil {
					t.Fatal("event.Narrative = nil, want payload")
				}
				if env.Event.Narrative.Text != "" {
					t.Fatalf("event.Narrative.Text = %q, want empty reasoning-only answer text", env.Event.Narrative.Text)
				}
				if env.Event.Narrative.ReasoningText != " " {
					t.Fatalf("event.Narrative.ReasoningText = %q, want single space", env.Event.Narrative.ReasoningText)
				}
			},
		},
		{
			name: "assistant stream preserves whitespace without message",
			ev: &sdksession.Event{
				ID:         "assistant-space-no-message",
				Type:       sdksession.EventTypeAssistant,
				Text:       " ",
				Visibility: sdksession.VisibilityUIOnly,
				Protocol: &sdksession.EventProtocol{
					UpdateType: string(sdksession.ProtocolUpdateTypeAgentMessage),
				},
			},
			want: func(t *testing.T, env EventEnvelope) {
				t.Helper()
				if env.Event.Narrative == nil {
					t.Fatal("event.Narrative = nil, want payload")
				}
				if env.Event.Narrative.Text != " " {
					t.Fatalf("event.Narrative.Text = %q, want single space", env.Event.Narrative.Text)
				}
				if env.Event.Narrative.Final {
					t.Fatal("event.Narrative.Final = true, want streaming UI-only payload")
				}
			},
		},
		{
			name: "plan",
			ev: &sdksession.Event{
				ID:   "plan-1",
				Type: sdksession.EventTypePlan,
				Protocol: &sdksession.EventProtocol{
					Plan: &sdksession.ProtocolPlan{
						Entries: []sdksession.ProtocolPlanEntry{
							{Content: "Inspect gateway event flow", Status: "in_progress", Priority: "high"},
						},
					},
				},
			},
			want: func(t *testing.T, env EventEnvelope) {
				t.Helper()
				if env.Event.Kind != EventKindPlanUpdate {
					t.Fatalf("event.Kind = %q, want %q", env.Event.Kind, EventKindPlanUpdate)
				}
				if env.Event.Plan == nil || len(env.Event.Plan.Entries) != 1 {
					t.Fatalf("event.Plan = %+v, want one entry", env.Event.Plan)
				}
				if entry := env.Event.Plan.Entries[0]; entry.Content != "Inspect gateway event flow" || entry.Status != "in_progress" || entry.Priority != "high" {
					t.Fatalf("event.Plan.Entries[0] = %+v", entry)
				}
			},
		},
		{
			name: "tool call started",
			ev: &sdksession.Event{
				ID:   "tool-call-started",
				Type: sdksession.EventTypeToolCall,
				Protocol: &sdksession.EventProtocol{
					ToolCall: &sdksession.ProtocolToolCall{
						ID:       "call-1",
						Name:     "READ",
						RawInput: map[string]any{"path": "/tmp/demo.txt"},
					},
				},
			},
			want: func(t *testing.T, env EventEnvelope) {
				t.Helper()
				if env.Event.ToolCall == nil {
					t.Fatal("event.ToolCall = nil, want payload")
				}
				if env.Event.ToolCall.Status != ToolStatusStarted {
					t.Fatalf("event.ToolCall.Status = %q, want %q", env.Event.ToolCall.Status, ToolStatusStarted)
				}
				if got := env.Event.ToolCall.RawInput["path"]; got != "/tmp/demo.txt" {
					t.Fatalf("event.ToolCall.RawInput[path] = %#v, want /tmp/demo.txt", got)
				}
			},
		},
		{
			name: "tool call running",
			ev: &sdksession.Event{
				ID:   "tool-call-running",
				Type: sdksession.EventTypeToolCall,
				Protocol: &sdksession.EventProtocol{
					ToolCall: &sdksession.ProtocolToolCall{
						ID:       "call-2",
						Name:     "BASH",
						Status:   "running",
						RawInput: map[string]any{"command": "sleep 1"},
					},
				},
			},
			want: func(t *testing.T, env EventEnvelope) {
				t.Helper()
				if env.Event.ToolCall == nil {
					t.Fatal("event.ToolCall = nil, want payload")
				}
				if env.Event.ToolCall.Status != ToolStatusRunning {
					t.Fatalf("event.ToolCall.Status = %q, want %q", env.Event.ToolCall.Status, ToolStatusRunning)
				}
			},
		},
		{
			name: "tool result completed",
			ev: &sdksession.Event{
				ID:   "tool-result-completed",
				Type: sdksession.EventTypeToolResult,
				Protocol: &sdksession.EventProtocol{
					ToolCall: &sdksession.ProtocolToolCall{
						ID:        "call-3",
						Name:      "READ",
						Status:    "completed",
						RawInput:  map[string]any{"path": "/tmp/demo.txt"},
						RawOutput: map[string]any{"path": "/tmp/demo.txt"},
					},
				},
			},
			want: func(t *testing.T, env EventEnvelope) {
				t.Helper()
				if env.Event.ToolResult == nil {
					t.Fatal("event.ToolResult = nil, want payload")
				}
				if env.Event.ToolResult.Status != ToolStatusCompleted || env.Event.ToolResult.Error {
					t.Fatalf("event.ToolResult = %+v", env.Event.ToolResult)
				}
			},
		},
		{
			name: "tool result preserves acp raw payload without synthesizing display meta",
			ev: &sdksession.Event{
				ID:   "tool-result-read-display",
				Type: sdksession.EventTypeToolResult,
				Protocol: &sdksession.EventProtocol{
					ToolCall: &sdksession.ProtocolToolCall{
						ID:       "call-read",
						Name:     "READ",
						Status:   "completed",
						RawInput: map[string]any{"path": "/tmp/demo.py", "offset": 0, "limit": 100},
						RawOutput: map[string]any{
							"path":       "/tmp/demo.py",
							"start_line": 1,
							"end_line":   100,
							"has_more":   true,
							"content":    "1: print('hello')",
						},
					},
				},
			},
			want: func(t *testing.T, env EventEnvelope) {
				t.Helper()
				if env.Event.ToolResult == nil {
					t.Fatal("event.ToolResult = nil, want payload")
				}
				if got := env.Event.ToolResult.RawInput["path"]; got != "/tmp/demo.py" {
					t.Fatalf("event.ToolResult.RawInput[path] = %#v", got)
				}
				if got := env.Event.ToolResult.RawOutput["start_line"]; got != 1 {
					t.Fatalf("event.ToolResult.RawOutput[start_line] = %#v", got)
				}
				if _, ok := env.Event.Meta["path"]; ok {
					t.Fatalf("event.Meta = %#v, raw tool fields must stay under meta.caelis", env.Event.Meta)
				}
				if env.Event.Meta != nil {
					if caelis, ok := env.Event.Meta["caelis"].(map[string]any); ok {
						if _, hasDisplay := caelis["display"]; hasDisplay {
							t.Fatalf("meta.caelis = %#v, should not synthesize display data", caelis)
						}
					}
				}
			},
		},
		{
			name: "tool result failed",
			ev: &sdksession.Event{
				ID:   "tool-result-failed",
				Type: sdksession.EventTypeToolResult,
				Protocol: &sdksession.EventProtocol{
					ToolCall: &sdksession.ProtocolToolCall{
						ID:        "call-4",
						Name:      "BASH",
						Status:    "error",
						RawInput:  map[string]any{"command": "exit 1"},
						RawOutput: map[string]any{"error": "exit status 1"},
					},
				},
			},
			want: func(t *testing.T, env EventEnvelope) {
				t.Helper()
				if env.Event.ToolResult == nil {
					t.Fatal("event.ToolResult = nil, want payload")
				}
				if env.Event.ToolResult.Status != ToolStatusFailed || !env.Event.ToolResult.Error {
					t.Fatalf("event.ToolResult = %+v", env.Event.ToolResult)
				}
			},
		},
		{
			name: "participant subagent",
			ev: &sdksession.Event{
				ID:   "participant-1",
				Type: sdksession.EventTypeParticipant,
				Scope: &sdksession.EventScope{
					TurnID: "turn-1",
					Participant: sdksession.ParticipantRef{
						ID:           "participant-1",
						Kind:         sdksession.ParticipantKindSubagent,
						Role:         sdksession.ParticipantRoleSidecar,
						DelegationID: "delegation-1",
					},
					ACP: sdksession.ACPRef{SessionID: "remote-session-1"},
				},
				Protocol: &sdksession.EventProtocol{
					Participant: &sdksession.ProtocolParticipant{Action: "attached"},
				},
			},
			want: func(t *testing.T, env EventEnvelope) {
				t.Helper()
				if env.Event.Participant == nil {
					t.Fatal("event.Participant = nil, want payload")
				}
				if env.Event.Participant.Action != ParticipantActionAttached || env.Event.Participant.Scope != EventScopeSubagent {
					t.Fatalf("event.Participant = %+v", env.Event.Participant)
				}
				if env.Event.Origin == nil || env.Event.Origin.Scope != EventScopeSubagent || env.Event.Origin.ScopeID != "remote-session-1" {
					t.Fatalf("event.Origin = %+v, want subagent origin", env.Event.Origin)
				}
			},
		},
		{
			name: "lifecycle",
			ev: &sdksession.Event{
				ID:   "lifecycle-1",
				Type: sdksession.EventTypeLifecycle,
				Scope: &sdksession.EventScope{
					Participant: sdksession.ParticipantRef{ID: "participant-1"},
				},
				Lifecycle: &sdksession.EventLifecycle{
					Status: "waiting_approval",
					Reason: "tool gate",
				},
			},
			want: func(t *testing.T, env EventEnvelope) {
				t.Helper()
				if env.Event.Lifecycle == nil {
					t.Fatal("event.Lifecycle = nil, want payload")
				}
				if env.Event.Lifecycle.Status != LifecycleStatusWaitingApproval || env.Event.Lifecycle.Reason != "tool gate" {
					t.Fatalf("event.Lifecycle = %+v", env.Event.Lifecycle)
				}
			},
		},
		{
			name: "usage snapshot",
			ev: &sdksession.Event{
				ID:   "usage-1",
				Type: sdksession.EventTypeAssistant,
				Text: "done",
				Meta: map[string]any{
					"usage": map[string]any{
						"prompt_tokens":       12,
						"cached_input_tokens": 7,
						"completion_tokens":   5,
						"reasoning_tokens":    3,
						"total_tokens":        17,
					},
				},
			},
			want: func(t *testing.T, env EventEnvelope) {
				t.Helper()
				if env.Event.Usage == nil {
					t.Fatal("event.Usage = nil, want payload")
				}
				if env.Event.Usage.PromptTokens != 12 || env.Event.Usage.CachedInputTokens != 7 || env.Event.Usage.CompletionTokens != 5 || env.Event.Usage.ReasoningTokens != 3 || env.Event.Usage.TotalTokens != 17 {
					t.Fatalf("event.Usage = %+v", env.Event.Usage)
				}
			},
		},
		{
			name: "top-level usage snapshot",
			ev: &sdksession.Event{
				ID:   "usage-2",
				Type: sdksession.EventTypeAssistant,
				Text: "done",
				Meta: map[string]any{
					"prompt_tokens":       12,
					"cached_input_tokens": 7,
					"completion_tokens":   5,
					"completion_tokens_details": map[string]any{
						"reasoning_tokens": 3,
					},
					"total_tokens": 17,
				},
			},
			want: func(t *testing.T, env EventEnvelope) {
				t.Helper()
				if env.Event.Usage == nil {
					t.Fatal("event.Usage = nil, want payload")
				}
				if env.Event.Usage.PromptTokens != 12 || env.Event.Usage.CachedInputTokens != 7 || env.Event.Usage.CompletionTokens != 5 || env.Event.Usage.ReasoningTokens != 3 || env.Event.Usage.TotalTokens != 17 {
					t.Fatalf("event.Usage = %+v", env.Event.Usage)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			projected := projectSessionEvents(sdksession.SessionRef{SessionID: "root-session"}, []*sdksession.Event{tt.ev})
			if len(projected) != 1 {
				t.Fatalf("projectSessionEvents() len = %d, want 1", len(projected))
			}
			tt.want(t, projected[0])
		})
	}
}

func TestProjectSessionEventsPreservesProtocolToolCallID(t *testing.T) {
	t.Parallel()

	events := projectSessionEvents(sdksession.SessionRef{SessionID: "root-session"}, []*sdksession.Event{{
		ID:   "tool-1",
		Type: sdksession.EventTypeToolCall,
		Meta: map[string]any{"caelis": map[string]any{"runtime": map[string]any{"tool": map[string]any{"name": "BASH"}}}},
		Protocol: &sdksession.EventProtocol{
			Update: &sdksession.ProtocolUpdate{
				SessionUpdate: string(sdksession.ProtocolUpdateTypeToolCall),
				ToolCallID:    "call-1",
				Kind:          "execute",
				Title:         "BASH echo hi",
				RawInput:      map[string]any{"command": "echo hi"},
			},
		},
	}})
	if len(events) != 1 {
		t.Fatalf("projectSessionEvents() len = %d, want 1", len(events))
	}
	if events[0].Event.Kind != EventKindToolCall {
		t.Fatalf("event kind = %q, want %q", events[0].Event.Kind, EventKindToolCall)
	}
	payload := events[0].Event.ToolCall
	if payload == nil {
		t.Fatal("tool call payload = nil, want canonical payload")
		return
	}
	if payload.CallID != "call-1" {
		t.Fatalf("payload.CallID = %q, want %q", payload.CallID, "call-1")
	}
	if payload.ToolName != "BASH" {
		t.Fatalf("payload.ToolName = %q, want %q", payload.ToolName, "BASH")
	}
	if payload.Status != ToolStatusStarted {
		t.Fatalf("payload.Status = %q, want %q", payload.Status, ToolStatusStarted)
	}
}

func TestProjectSessionEventsFallsBackToMessageToolUseRawInput(t *testing.T) {
	t.Parallel()

	message := sdkmodel.MessageFromToolCalls(sdkmodel.RoleAssistant, []sdkmodel.ToolCall{{
		ID:   "spawn-call",
		Name: "SPAWN",
		Args: `{"agent":"claude","prompt":"写一首四行英文短诗"}`,
	}}, "")
	events := projectSessionEvents(sdksession.SessionRef{SessionID: "root-session"}, []*sdksession.Event{{
		ID:      "tool-1",
		Type:    sdksession.EventTypeToolCall,
		Message: &message,
		Meta:    map[string]any{"caelis": map[string]any{"runtime": map[string]any{"tool": map[string]any{"name": "SPAWN"}}}},
		Protocol: &sdksession.EventProtocol{
			ToolCall: &sdksession.ProtocolToolCall{
				ID:     "spawn-call",
				Name:   "SPAWN",
				Kind:   "execute",
				Status: "running",
			},
		},
	}})
	if len(events) != 1 || events[0].Event.ToolCall == nil {
		t.Fatalf("projectSessionEvents() = %#v, want tool call", events)
	}
	payload := events[0].Event.ToolCall
	if payload.CallID != "spawn-call" {
		t.Fatalf("payload.CallID = %q, want spawn-call", payload.CallID)
	}
	if got := payload.RawInput["prompt"]; got != "写一首四行英文短诗" {
		t.Fatalf("payload.RawInput = %#v, want prompt from Message.ToolCalls", payload.RawInput)
	}
}

func TestProjectSessionEventExportsSingleCanonicalEnvelope(t *testing.T) {
	t.Parallel()

	env, ok := ProjectSessionEvent(sdksession.SessionRef{SessionID: "root-session"}, &sdksession.Event{
		ID:         "evt-1",
		Type:       sdksession.EventTypeAssistant,
		Visibility: sdksession.VisibilityCanonical,
		Text:       "structured side output",
		Scope: &sdksession.EventScope{
			Participant: sdksession.ParticipantRef{
				ID:           "agent-1",
				Kind:         sdksession.ParticipantKindSubagent,
				Role:         sdksession.ParticipantRoleDelegated,
				DelegationID: "task-1",
			},
		},
	})
	if !ok {
		t.Fatal("ProjectSessionEvent() ok = false, want true")
	}
	if env.Cursor != "evt-1" || env.Event.Kind != EventKindAssistantMessage {
		t.Fatalf("env = %#v, want assistant envelope with cursor", env)
	}
	if env.Event.Origin == nil || env.Event.Origin.Scope != EventScopeSubagent || env.Event.Narrative == nil || env.Event.Narrative.Text != "structured side output" {
		t.Fatalf("env.Event = %#v, want subagent assistant narrative", env.Event)
	}
}

func TestProjectSessionEventPreservesMainACPSource(t *testing.T) {
	t.Parallel()

	env, ok := ProjectSessionEvent(sdksession.SessionRef{SessionID: "root-session"}, &sdksession.Event{
		Type:       sdksession.EventTypeAssistant,
		Visibility: sdksession.VisibilityCanonical,
		Text:       "main acp output",
		Scope: &sdksession.EventScope{
			Source: "acp",
		},
	})
	if !ok {
		t.Fatal("ProjectSessionEvent() ok = false, want true")
	}
	if env.Event.Origin == nil || env.Event.Origin.Scope != EventScopeMain || env.Event.Origin.Source != "acp" {
		t.Fatalf("origin = %#v, want main ACP source", env.Event.Origin)
	}
}

func TestProjectSessionEventACPMessageChunkIsStreamingDelta(t *testing.T) {
	t.Parallel()

	env, ok := ProjectSessionEvent(sdksession.SessionRef{SessionID: "root-session"}, &sdksession.Event{
		Type:       sdksession.EventTypeAssistant,
		Visibility: sdksession.VisibilityCanonical,
		Text:       "上海",
		Scope:      &sdksession.EventScope{Source: "acp_participant"},
		Protocol:   &sdksession.EventProtocol{UpdateType: string(sdksession.ProtocolUpdateTypeAgentMessage)},
	})
	if !ok {
		t.Fatal("ProjectSessionEvent() ok = false, want true")
	}
	if env.Event.Narrative == nil {
		t.Fatalf("projected event = %#v, want narrative payload", env.Event)
	}
	if env.Event.Narrative.Final {
		t.Fatalf("narrative.Final = true, want ACP agent_message_chunk to stay streaming")
	}
	if env.Event.Narrative.Text != "上海" {
		t.Fatalf("narrative.Text = %q, want streamed chunk", env.Event.Narrative.Text)
	}
}

func TestProjectSessionEventPersistedACPMessageChunkIsReplayFinal(t *testing.T) {
	t.Parallel()

	env, ok := ProjectSessionEvent(sdksession.SessionRef{SessionID: "root-session"}, &sdksession.Event{
		ID:         "evt-1",
		Type:       sdksession.EventTypeAssistant,
		Visibility: sdksession.VisibilityCanonical,
		Text:       "上海今天小雨。",
		Scope:      &sdksession.EventScope{Source: "acp_participant"},
		Protocol:   &sdksession.EventProtocol{UpdateType: string(sdksession.ProtocolUpdateTypeAgentMessage)},
	})
	if !ok {
		t.Fatal("ProjectSessionEvent() ok = false, want true")
	}
	if env.Event.Narrative == nil {
		t.Fatalf("projected event = %#v, want narrative payload", env.Event)
	}
	if !env.Event.Narrative.Final {
		t.Fatalf("narrative.Final = false, want persisted ACP replay chunk to be final")
	}
	if env.Event.Narrative.Text != "上海今天小雨。" {
		t.Fatalf("narrative.Text = %q, want persisted assistant text", env.Event.Narrative.Text)
	}
}

func TestProjectSessionEventProjectsThoughtTextWithoutMessage(t *testing.T) {
	t.Parallel()

	env, ok := ProjectSessionEvent(sdksession.SessionRef{SessionID: "root-session"}, &sdksession.Event{
		Type:       sdksession.EventTypeAssistant,
		Visibility: sdksession.VisibilityCanonical,
		Text:       "thinking through side output",
		Protocol: &sdksession.EventProtocol{
			UpdateType: string(sdksession.ProtocolUpdateTypeAgentThought),
		},
	})
	if !ok {
		t.Fatal("ProjectSessionEvent() ok = false, want true")
	}
	if env.Event.Narrative == nil || env.Event.Narrative.ReasoningText != "thinking through side output" || env.Event.Narrative.Text != "" {
		t.Fatalf("narrative = %#v, want reasoning-only thought text", env.Event.Narrative)
	}
}

func TestEventPublicContractDoesNotExposeRawCompatibilityFields(t *testing.T) {
	t.Parallel()

	eventType := reflect.TypeOf(Event{})
	for _, name := range []string{"SessionEvent", "Approval"} {
		if _, ok := eventType.FieldByName(name); ok {
			t.Fatalf("Event exposes raw compatibility field %s; want canonical payloads only", name)
		}
	}
	for _, kind := range []EventKind{
		EventKindUserMessage,
		EventKindAssistantMessage,
		EventKindPlanUpdate,
		EventKindToolCall,
		EventKindToolResult,
		EventKindParticipant,
		EventKindHandoff,
		EventKindCompact,
		EventKindNotice,
		EventKindSystemMessage,
		EventKindApprovalRequested,
		EventKindApprovalReview,
		EventKindLifecycle,
	} {
		if strings.Contains(string(kind), "session_") {
			t.Fatalf("EventKind %q exposes raw session compatibility naming", kind)
		}
	}
}

func TestCanonicalToolStatusCoversStandardLifecycle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want ToolStatus
	}{
		{name: "waiting approval", raw: "waiting_approval", want: ToolStatusWaitingApproval},
		{name: "interrupted", raw: "interrupted", want: ToolStatusInterrupted},
		{name: "cancelled", raw: "cancelled", want: ToolStatusCancelled},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := canonicalToolCallStatus(tt.raw); got != tt.want {
				t.Fatalf("canonicalToolCallStatus(%q) = %q, want %q", tt.raw, got, tt.want)
			}
			if got := canonicalToolResultStatus(tt.raw, false); got != tt.want {
				t.Fatalf("canonicalToolResultStatus(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestEventEnvelopeJSONUsesStableProtocolNames(t *testing.T) {
	t.Parallel()

	env := EventEnvelope{
		Cursor: "cursor-1",
		Event: Event{
			Kind:     EventKindToolCall,
			HandleID: "handle-1",
			RunID:    "run-1",
			TurnID:   "turn-1",
			ToolCall: &ToolCallPayload{
				CallID:   "call-1",
				ToolName: "BASH",
				RawInput: map[string]any{"command": "go test ./gateway/..."},
				Status:   ToolStatusWaitingApproval,
				Scope:    EventScopeMain,
			},
		},
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal(EventEnvelope) error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal(EventEnvelope) error = %v", err)
	}
	if _, ok := got["Cursor"]; ok {
		t.Fatalf("EventEnvelope JSON = %s, leaked Go field Cursor", data)
	}
	event, ok := got["event"].(map[string]any)
	if !ok {
		t.Fatalf("EventEnvelope JSON = %s, want event object", data)
	}
	for _, key := range []string{"kind", "handle_id", "run_id", "turn_id", "tool_call"} {
		if _, ok := event[key]; !ok {
			t.Fatalf("EventEnvelope JSON event = %#v, missing %q", event, key)
		}
	}
	tool, ok := event["tool_call"].(map[string]any)
	if !ok {
		t.Fatalf("EventEnvelope JSON event = %#v, want tool_call object", event)
	}
	rawInput, _ := tool["raw_input"].(map[string]any)
	if rawInput["command"] != "go test ./gateway/..." || tool["status"] != string(ToolStatusWaitingApproval) {
		t.Fatalf("tool_call JSON = %#v", tool)
	}
}

func TestEventEnvelopeJSONUsesStableErrorPayload(t *testing.T) {
	t.Parallel()

	env := EventEnvelope{
		Event: Event{Kind: EventKindLifecycle},
		Err: &Error{
			Kind:        KindValidation,
			Code:        CodeInvalidRequest,
			Message:     "input is required",
			Detail:      "empty prompt",
			Retryable:   false,
			UserVisible: true,
		},
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal(EventEnvelope) error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal(EventEnvelope) error = %v", err)
	}
	payload, ok := got["err"].(map[string]any)
	if !ok {
		t.Fatalf("EventEnvelope JSON = %s, want err object", data)
	}
	for _, key := range []string{"kind", "code", "message", "detail", "user_visible"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("err JSON = %#v, missing %q", payload, key)
		}
	}
	if _, ok := payload["Cause"]; ok {
		t.Fatalf("err JSON = %#v, leaked Cause", payload)
	}
	if _, ok := payload["Message"]; ok {
		t.Fatalf("err JSON = %#v, leaked Go field Message", payload)
	}
}

func TestReplayAfterCursorReturnsCursorNotFound(t *testing.T) {
	t.Parallel()

	_, err := replayAfterCursor([]EventEnvelope{
		{Cursor: "e1"},
		{Cursor: "e2"},
	}, "missing", 0)
	if err == nil {
		t.Fatal("replayAfterCursor() error = nil, want cursor_not_found")
	}
	var gwErr *Error
	if !As(err, &gwErr) || gwErr.Code != CodeCursorNotFound {
		t.Fatalf("replayAfterCursor() error = %v, want cursor_not_found", err)
	}
}
