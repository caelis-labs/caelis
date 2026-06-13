package tuiapp

import (
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestProjectGatewayEventToTranscriptEvents_AssistantAndUsage(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind:       gateway.EventKindAssistantMessage,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
		Narrative: &gateway.NarrativePayload{
			Role:          gateway.NarrativeRoleAssistant,
			Actor:         "assistant",
			ReasoningText: "think",
			Text:          "answer",
			Final:         true,
		},
		Usage: &gateway.UsageSnapshot{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	})

	if got := len(events); got != 3 {
		t.Fatalf("len(events) = %d, want 3", got)
	}
	if events[0].Kind != TranscriptEventNarrative || events[0].NarrativeKind != TranscriptNarrativeReasoning || events[0].Text != "think" || !events[0].Final {
		t.Fatalf("events[0] = %#v, want final reasoning narrative", events[0])
	}
	if events[1].Kind != TranscriptEventNarrative || events[1].NarrativeKind != TranscriptNarrativeAssistant || events[1].Text != "answer" || !events[1].Final {
		t.Fatalf("events[1] = %#v, want final assistant narrative", events[1])
	}
	if events[2].Kind != TranscriptEventUsage || events[2].Usage == nil || events[2].Usage.TotalTokens != 15 {
		t.Fatalf("events[2] = %#v, want usage snapshot", events[2])
	}
}

func TestProjectGatewayEventToTranscriptEvents_DoesNotPersistApproval(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind:       gateway.EventKindApprovalRequested,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
		ApprovalPayload: &gateway.ApprovalPayload{
			ToolName: "RUN_COMMAND",
			RawInput: map[string]any{"command": "go test ./kernel/..."},
			Status:   gateway.ApprovalStatusPending,
		},
	})

	if len(events) != 0 {
		t.Fatalf("ProjectGatewayEventToTranscriptEvents() = %#v, want no persisted approval transcript events", events)
	}
}

func TestProjectGatewayEventToTranscriptEvents_ProjectsTerminalAutomaticApprovalReview(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind:       gateway.EventKindApprovalReview,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
		ApprovalPayload: &gateway.ApprovalPayload{
			ToolCallID:     "perm-call-1",
			ToolName:       "custom_tool",
			RawInput:       map[string]any{"reason": "need access"},
			ReviewStatus:   gateway.ApprovalReviewStatusApproved,
			DecisionSource: "auto-review",
			ReviewText:     "Automatic approval review approved (risk: low, authorization: high): required by task.",
			Risk:           "low",
			Authorization:  "high",
		},
	})

	if got := len(events); got != 1 {
		t.Fatalf("len(events) = %d, want 1", got)
	}
	if events[0].Kind != TranscriptEventApproval || events[0].ApprovalStatus != string(gateway.ApprovalReviewStatusApproved) {
		t.Fatalf("events[0] = %#v, want approval review transcript event", events[0])
	}
	if events[0].ToolCallID != "perm-call-1" {
		t.Fatalf("ToolCallID = %q, want %q", events[0].ToolCallID, "perm-call-1")
	}
	if !strings.Contains(events[0].ApprovalText, "Automatic approval review approved") {
		t.Fatalf("ApprovalText = %q, want review text", events[0].ApprovalText)
	}
	if events[0].ApprovalRisk != "low" || events[0].ApprovalAuth != "high" {
		t.Fatalf("approval metadata = (%q, %q), want risk low and authorization high", events[0].ApprovalRisk, events[0].ApprovalAuth)
	}
	if strings.Contains(events[0].ApprovalText, "⚠") {
		t.Fatalf("ApprovalText = %q, should not use warning prefix", events[0].ApprovalText)
	}

	pending := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind:       gateway.EventKindApprovalReview,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
		ApprovalPayload: &gateway.ApprovalPayload{
			ToolName:       "custom_tool",
			ReviewStatus:   gateway.ApprovalReviewStatusInProgress,
			DecisionSource: "auto-review",
		},
	})
	if len(pending) != 0 {
		t.Fatalf("pending approval review events = %#v, want no transcript events", pending)
	}
}

func TestProjectGatewayEventToTranscriptEvents_SuppressesParticipantUserEcho(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		event gateway.Event
	}{
		{
			name: "user event",
			event: gateway.Event{
				Kind:       gateway.EventKindUserMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Origin: &gateway.EventOrigin{
					Scope:         gateway.EventScopeParticipant,
					ScopeID:       "participant-turn-1",
					ParticipantID: "participant-1",
					Actor:         "@jeff",
				},
				Narrative: &gateway.NarrativePayload{
					Role:  gateway.NarrativeRoleUser,
					Text:  "总结一下工作",
					Scope: gateway.EventScopeParticipant,
				},
			},
		},
		{
			name: "assistant user role",
			event: gateway.Event{
				Kind:       gateway.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Origin: &gateway.EventOrigin{
					Scope:         gateway.EventScopeParticipant,
					ScopeID:       "participant-turn-1",
					ParticipantID: "participant-1",
					Actor:         "@jeff",
				},
				Narrative: &gateway.NarrativePayload{
					Role:  gateway.NarrativeRoleUser,
					Text:  "总结一下工作",
					Scope: gateway.EventScopeParticipant,
				},
			},
		},
		{
			name: "payload scope without origin",
			event: gateway.Event{
				Kind:       gateway.EventKindUserMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Narrative: &gateway.NarrativePayload{
					Role:          gateway.NarrativeRoleUser,
					Text:          "总结一下工作",
					Scope:         gateway.EventScopeParticipant,
					ParticipantID: "participant-1",
				},
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if events := ProjectGatewayEventToTranscriptEvents(tc.event); len(events) != 0 {
				t.Fatalf("ProjectGatewayEventToTranscriptEvents() = %#v, want no participant user echo", events)
			}
		})
	}
}

func TestProjectGatewayEventToTranscriptEvents_KeepsMainUserMessage(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind:       gateway.EventKindUserMessage,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
		Narrative: &gateway.NarrativePayload{
			Role:  gateway.NarrativeRoleUser,
			Text:  "/claude 总结一下工作",
			Scope: gateway.EventScopeMain,
		},
	})

	if got := len(events); got != 1 {
		t.Fatalf("len(events) = %d, want 1", got)
	}
	if events[0].Kind != TranscriptEventNarrative || events[0].NarrativeKind != TranscriptNarrativeUser || events[0].Text != "/claude 总结一下工作" {
		t.Fatalf("events[0] = %#v, want main user narrative", events[0])
	}
}

func TestProjectGatewayEventProtocolUpdateRendersTerminalOutputMeta(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind: gateway.EventKindToolResult,
		Protocol: &session.EventProtocol{
			UpdateType: string(session.ProtocolUpdateTypeToolUpdate),
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
				ToolCallID:    "call-1",
				Title:         "RUN_COMMAND date",
				Kind:          "execute",
				Status:        "in_progress",
				RawInput:      map[string]any{"command": "date"},
				Content: []session.ProtocolToolCallContent{{
					Type:       "terminal",
					TerminalID: "call-1",
				}},
				Meta: map[string]any{
					"terminal_info": map[string]any{
						"terminal_id": "call-1",
						"tool":        "RUN_COMMAND",
					},
					"terminal_output": map[string]any{
						"terminal_id": "call-1",
						"data":        "line 1\n",
					},
				},
			},
		},
	})

	if len(events) != 1 {
		t.Fatalf("events = %#v, want one protocol tool event", events)
	}
	got := events[0]
	if got.ToolName != "RUN_COMMAND" || got.ToolCallID != "call-1" {
		t.Fatalf("tool identity = %#v, want RUN_COMMAND call-1", got)
	}
	if got.ToolOutput != "line 1\n" {
		t.Fatalf("ToolOutput = %q, want terminal output from _meta", got.ToolOutput)
	}
}

func TestProjectGatewayEventProtocolTaskWaitShowsActionWithoutOutput(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind: gateway.EventKindToolResult,
		Protocol: &session.EventProtocol{
			UpdateType: string(session.ProtocolUpdateTypeToolUpdate),
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
				ToolCallID:    "task-wait",
				Title:         "TASK wait task-7",
				Kind:          "execute",
				Status:        "in_progress",
				RawInput:      map[string]any{"action": "wait", "task_id": "task-7", "yield_time_ms": 2000},
				RawOutput:     map[string]any{"task_id": "task-7", "running": true},
				Meta: map[string]any{
					"caelis": map[string]any{
						"runtime": map[string]any{
							"tool": map[string]any{
								"name":        "TASK",
								"action":      "wait",
								"target_id":   "task-7",
								"target_kind": "command",
							},
						},
					},
				},
			},
		},
	})

	if len(events) != 1 {
		t.Fatalf("events = %#v, want one TASK wait action event", events)
	}
	got := events[0]
	if got.ToolArgs != "Wait 2s" {
		t.Fatalf("ToolArgs = %q, want Wait 2s", got.ToolArgs)
	}
	if got.ToolOutput != "" {
		t.Fatalf("ToolOutput = %q, want no TASK wait display output", got.ToolOutput)
	}
}

func TestTranscriptSnapshots(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		run  func(*Model) *Model
		want string
	}{
		{
			name: "assistant streaming",
			run: func(m *Model) *Model {
				updated, _ := m.Update(gatewayEventMsg(gateway.EventEnvelope{
					Event: gateway.Event{
						Kind:       gateway.EventKindAssistantMessage,
						SessionRef: session.SessionRef{SessionID: "root-session"},
						Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
						Narrative: &gateway.NarrativePayload{
							Role:  gateway.NarrativeRoleAssistant,
							Text:  "hello ",
							Final: false,
						},
					}}))

				m = updated.(*Model)
				updated, _ = m.Update(gatewayEventMsg(gateway.EventEnvelope{
					Event: gateway.Event{
						Kind:       gateway.EventKindAssistantMessage,
						SessionRef: session.SessionRef{SessionID: "root-session"},
						Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
						Narrative: &gateway.NarrativePayload{
							Role:  gateway.NarrativeRoleAssistant,
							Text:  "hello world",
							Final: true,
						},
					}}))

				return updated.(*Model)
			},
			want: "Main(session=root-session,status=running)\n  assistant:hello world",
		},
		{
			name: "tool call output result",
			run: func(m *Model) *Model {
				updated, _ := m.Update(gatewayEventMsg(gateway.EventEnvelope{
					Event: gateway.Event{
						Kind:       gateway.EventKindToolCall,
						SessionRef: session.SessionRef{SessionID: "root-session"},
						Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
						ToolCall: &gateway.ToolCallPayload{
							CallID:   "call-1",
							ToolName: "RUN_COMMAND",
							RawInput: map[string]any{"command": `echo "hi"`},
							Status:   gateway.ToolStatusRunning,
						},
					}}))

				m = updated.(*Model)
				updated, _ = m.Update(gatewayEventMsg(gateway.EventEnvelope{
					Event: gateway.Event{
						Kind:       gateway.EventKindToolResult,
						SessionRef: session.SessionRef{SessionID: "root-session"},
						Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
						ToolResult: &gateway.ToolResultPayload{
							CallID:    "call-1",
							ToolName:  "RUN_COMMAND",
							RawInput:  map[string]any{"command": `echo "hi"`},
							RawOutput: map[string]any{"text": "line 1"},
							Content:   testTerminalContent("line 1"),
							Status:    gateway.ToolStatusRunning,
						},
					}}))

				m = updated.(*Model)
				updated, _ = m.Update(gatewayEventMsg(gateway.EventEnvelope{
					Event: gateway.Event{
						Kind:       gateway.EventKindToolResult,
						SessionRef: session.SessionRef{SessionID: "root-session"},
						Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
						ToolResult: &gateway.ToolResultPayload{
							CallID:    "call-1",
							ToolName:  "RUN_COMMAND",
							RawInput:  map[string]any{"command": `echo "hi"`},
							RawOutput: map[string]any{"stdout": "done"},
							Content:   testTerminalContent("done"),
							Status:    gateway.ToolStatusCompleted,
						},
					}}))

				return updated.(*Model)
			},
			want: "Main(session=root-session,status=running)\n  tool(call-1,RUN_COMMAND,done,args=echo \"hi\",output=done)",
		},
		{
			name: "terminal contentless final preserves streamed output",
			run: func(m *Model) *Model {
				updated, _ := m.Update(gatewayEventMsg(gateway.EventEnvelope{
					Event: gateway.Event{
						Kind:       gateway.EventKindToolCall,
						SessionRef: session.SessionRef{SessionID: "root-session"},
						Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
						ToolCall: &gateway.ToolCallPayload{
							CallID:   "call-1",
							ToolName: "RUN_COMMAND",
							RawInput: map[string]any{"command": `printf hi`},
							Status:   gateway.ToolStatusRunning,
						},
					}}))

				m = updated.(*Model)
				updated, _ = m.Update(gatewayEventMsg(gateway.EventEnvelope{
					Event: gateway.Event{
						Kind:       gateway.EventKindToolResult,
						SessionRef: session.SessionRef{SessionID: "root-session"},
						Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
						ToolResult: &gateway.ToolResultPayload{
							CallID:   "call-1",
							ToolName: "RUN_COMMAND",
							RawInput: map[string]any{"command": `printf hi`},
							Content:  testTerminalContent("hi"),
							Status:   gateway.ToolStatusRunning,
						},
					}}))

				m = updated.(*Model)
				updated, _ = m.Update(gatewayEventMsg(gateway.EventEnvelope{
					Event: gateway.Event{
						Kind:       gateway.EventKindToolResult,
						SessionRef: session.SessionRef{SessionID: "root-session"},
						Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
						Meta: map[string]any{
							"caelis": map[string]any{
								"runtime": map[string]any{
									"stream": map[string]any{"mode": "final"},
								},
							},
						},
						ToolResult: &gateway.ToolResultPayload{
							CallID:   "call-1",
							ToolName: "RUN_COMMAND",
							RawInput: map[string]any{"command": `printf hi`},
							Status:   gateway.ToolStatusCompleted,
						},
					}}))

				return updated.(*Model)
			},
			want: "Main(session=root-session,status=running)\n  tool(call-1,RUN_COMMAND,done,args=printf hi,output=hi)",
		},
		{
			name: "terminal contentless final with no prior output shows placeholder",
			run: func(m *Model) *Model {
				updated, _ := m.Update(gatewayEventMsg(gateway.EventEnvelope{
					Event: gateway.Event{
						Kind:       gateway.EventKindToolCall,
						SessionRef: session.SessionRef{SessionID: "root-session"},
						Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
						ToolCall: &gateway.ToolCallPayload{
							CallID:   "call-1",
							ToolName: "RUN_COMMAND",
							RawInput: map[string]any{"command": `true`},
							Status:   gateway.ToolStatusRunning,
						},
					}}))

				m = updated.(*Model)
				updated, _ = m.Update(gatewayEventMsg(gateway.EventEnvelope{
					Event: gateway.Event{
						Kind:       gateway.EventKindToolResult,
						SessionRef: session.SessionRef{SessionID: "root-session"},
						Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
						ToolResult: &gateway.ToolResultPayload{
							CallID:    "call-1",
							ToolName:  "RUN_COMMAND",
							RawInput:  map[string]any{"command": `true`},
							RawOutput: map[string]any{"state": "completed", "exit_code": 0},
							Status:    gateway.ToolStatusCompleted,
						},
					}}))

				return updated.(*Model)
			},
			want: "Main(session=root-session,status=running)\n  tool(call-1,RUN_COMMAND,done,args=true,output=(no output))",
		},
		{
			name: "terminal contentless failed final shows failure",
			run: func(m *Model) *Model {
				updated, _ := m.Update(gatewayEventMsg(gateway.EventEnvelope{
					Event: gateway.Event{
						Kind:       gateway.EventKindToolCall,
						SessionRef: session.SessionRef{SessionID: "root-session"},
						Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
						ToolCall: &gateway.ToolCallPayload{
							CallID:   "call-1",
							ToolName: "RUN_COMMAND",
							RawInput: map[string]any{"command": `false`},
							Status:   gateway.ToolStatusRunning,
						},
					}}))

				m = updated.(*Model)
				updated, _ = m.Update(gatewayEventMsg(gateway.EventEnvelope{
					Event: gateway.Event{
						Kind:       gateway.EventKindToolResult,
						SessionRef: session.SessionRef{SessionID: "root-session"},
						Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
						ToolResult: &gateway.ToolResultPayload{
							CallID:   "call-1",
							ToolName: "RUN_COMMAND",
							RawInput: map[string]any{"command": `false`},
							Status:   gateway.ToolStatusFailed,
							Error:    true,
						},
					}}))

				return updated.(*Model)
			},
			want: "Main(session=root-session,status=running)\n  tool(call-1,RUN_COMMAND,failed,args=false,output=failed)",
		},
		{
			name: "approval overlay is not transcript",
			run: func(m *Model) *Model {
				updated, _ := m.Update(gatewayEventMsg(gateway.EventEnvelope{
					Event: gateway.Event{
						Kind:       gateway.EventKindApprovalRequested,
						SessionRef: session.SessionRef{SessionID: "root-session"},
						Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
						ApprovalPayload: &gateway.ApprovalPayload{
							ToolName: "RUN_COMMAND",
							RawInput: map[string]any{"command": "rm -rf /tmp/demo"},
							Status:   gateway.ApprovalStatusPending,
						},
					}}))

				return updated.(*Model)
			},
			want: "",
		},
		{
			name: "participant and subagent lanes",
			run: func(m *Model) *Model {
				updated, _ := m.Update(gatewayEventMsg(gateway.EventEnvelope{
					Event: gateway.Event{
						Kind:       gateway.EventKindAssistantMessage,
						SessionRef: session.SessionRef{SessionID: "participant-session"},
						Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeParticipant, ScopeID: "participant-session"},
						Narrative: &gateway.NarrativePayload{
							Role:  gateway.NarrativeRoleAssistant,
							Text:  "participant answer",
							Final: true,
						},
					}}))

				m = updated.(*Model)
				updated, _ = m.Update(gatewayEventMsg(gateway.EventEnvelope{
					Event: gateway.Event{
						Kind:       gateway.EventKindLifecycle,
						SessionRef: session.SessionRef{SessionID: "participant-session"},
						Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeParticipant, ScopeID: "participant-session"},
						Lifecycle: &gateway.LifecyclePayload{
							Status: gateway.LifecycleStatusCompleted,
						},
					}}))

				m = updated.(*Model)
				updated, _ = m.Update(gatewayEventMsg(gateway.EventEnvelope{
					Event: gateway.Event{
						Kind:       gateway.EventKindAssistantMessage,
						SessionRef: session.SessionRef{SessionID: "root-session"},
						Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeSubagent, ScopeID: "spawn-1", Actor: "copilot"},
						Narrative: &gateway.NarrativePayload{
							Role:  gateway.NarrativeRoleAssistant,
							Text:  "subagent answer",
							Final: true,
						},
					}}))

				return updated.(*Model)
			},
			want: "Participant(session=participant-session,status=completed)\n  assistant:participant answer\nParticipant(session=spawn-1,status=completed)\n  assistant:subagent answer",
		},
		{
			name: "replayed durable events",
			run: func(m *Model) *Model {
				for _, env := range []gateway.EventEnvelope{
					{
						Cursor: "c1",
						Event: gateway.Event{
							Kind:       gateway.EventKindAssistantMessage,
							SessionRef: session.SessionRef{SessionID: "root-session"},
							Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
							Narrative: &gateway.NarrativePayload{
								Role:  gateway.NarrativeRoleAssistant,
								Text:  "durable answer",
								Final: true,
							},
						},
					},
					{
						Cursor: "c2",
						Event: gateway.Event{
							Kind:       gateway.EventKindLifecycle,
							SessionRef: session.SessionRef{SessionID: "root-session"},
							Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
							Lifecycle: &gateway.LifecyclePayload{
								Status: gateway.LifecycleStatusCompleted,
							},
						},
					},
				} {
					updated, _ := m.Update(gatewayEventMsg(env))
					m = updated.(*Model)
				}
				return m
			},
			want: "Main(session=root-session,status=completed)\n  assistant:durable answer",
		},
		{
			name: "interrupted turn",
			run: func(m *Model) *Model {
				updated, _ := m.Update(gatewayEventMsg(gateway.EventEnvelope{
					Event: gateway.Event{
						Kind:       gateway.EventKindToolCall,
						SessionRef: session.SessionRef{SessionID: "root-session"},
						Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
						ToolCall: &gateway.ToolCallPayload{
							CallID:   "call-1",
							ToolName: "READ",
							RawInput: map[string]any{"path": "/tmp/demo"},
							Status:   gateway.ToolStatusRunning,
						},
					}}))

				m = updated.(*Model)
				updated, _ = m.Update(gatewayEventMsg(gateway.EventEnvelope{
					Event: gateway.Event{
						Kind:       gateway.EventKindLifecycle,
						SessionRef: session.SessionRef{SessionID: "root-session"},
						Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
						Lifecycle: &gateway.LifecyclePayload{
							Status: gateway.LifecycleStatusInterrupted,
						},
					}}))

				return updated.(*Model)
			},
			want: "Main(session=root-session,status=interrupted)\n  tool(call-1,READ,running,args=/tmp/demo,output=)",
		},
		{
			name: "failed tool call",
			run: func(m *Model) *Model {
				updated, _ := m.Update(gatewayEventMsg(gateway.EventEnvelope{
					Event: gateway.Event{
						Kind:       gateway.EventKindToolCall,
						SessionRef: session.SessionRef{SessionID: "root-session"},
						Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
						ToolCall: &gateway.ToolCallPayload{
							CallID:   "call-1",
							ToolName: "RUN_COMMAND",
							RawInput: map[string]any{"command": "false"},
							Status:   gateway.ToolStatusRunning,
						},
					}}))

				m = updated.(*Model)
				updated, _ = m.Update(gatewayEventMsg(gateway.EventEnvelope{
					Event: gateway.Event{
						Kind:       gateway.EventKindToolResult,
						SessionRef: session.SessionRef{SessionID: "root-session"},
						Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
						ToolResult: &gateway.ToolResultPayload{
							CallID:    "call-1",
							ToolName:  "RUN_COMMAND",
							RawInput:  map[string]any{"command": "false"},
							RawOutput: map[string]any{"stderr": "exit 1"},
							Content:   testTerminalContent("exit 1"),
							Status:    gateway.ToolStatusFailed,
							Error:     true,
						},
					}}))

				return updated.(*Model)
			},
			want: "Main(session=root-session,status=running)\n  tool(call-1,RUN_COMMAND,failed,args=false,output=exit 1)",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			model := newGatewayEventTestModel()
			got := snapshotTranscriptModel(tc.run(model))
			if got != tc.want {
				t.Fatalf("snapshot mismatch\nwant:\n%s\n\ngot:\n%s", tc.want, got)
			}
		})
	}
}

func TestStructuredSubagentGatewayToolRendersThroughTranscriptModel(t *testing.T) {
	t.Parallel()

	model := newGatewayEventTestModel()
	for _, env := range []gateway.EventEnvelope{
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeSubagent, ScopeID: "child-1", Actor: "copilot"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "RUN_COMMAND",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeSubagent,
				RawInput: map[string]any{"command": "go test ./surfaces/tui/app/..."},
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeSubagent, ScopeID: "child-1", Actor: "copilot"},
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "call-1",
				ToolName: "RUN_COMMAND",
				Status:   gateway.ToolStatusCompleted,
				Scope:    gateway.EventScopeSubagent,
				RawInput: map[string]any{"command": "go test ./surfaces/tui/app/..."},
				RawOutput: map[string]any{
					"stdout":    "ok\n",
					"exit_code": 0,
				},
				Content: testTerminalContent("ok\n"),
			},
		}},
	} {
		updated, _ := model.Update(gatewayEventMsg(env))
		model = updated.(*Model)
	}

	got := snapshotTranscriptModel(model)
	want := "Participant(session=child-1,status=running)\n  tool(call-1,RUN_COMMAND,done,args=go test ./surfaces/tui/app/...,output=ok)"
	if got != want {
		t.Fatalf("snapshot mismatch\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestProjectGatewayEventACPFetchToolUsesReadableQueryArgs(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind:   gateway.EventKindToolCall,
		Origin: &gateway.EventOrigin{Source: "acp_participant", Scope: gateway.EventScopeParticipant, ScopeID: "codex-001"},
		ToolCall: &gateway.ToolCallPayload{
			CallID:    "ws-1",
			ToolTitle: "Searching the Web",
			ToolKind:  "fetch",
			Status:    gateway.ToolStatusRunning,
			RawInput: map[string]any{
				"query": "weather: Shanghai, China",
				"action": map[string]any{
					"type":  "search",
					"query": "weather: Shanghai, China",
				},
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool event", events)
	}
	got := events[0]
	if got.ToolName != "fetch" || got.ToolKind != "fetch" || got.ToolTitle != "Searching the Web" {
		t.Fatalf("tool identity = name %q kind %q title %q, want ACP kind plus title", got.ToolName, got.ToolKind, got.ToolTitle)
	}
	if got.ToolArgs != `"weather: Shanghai, China"` {
		t.Fatalf("tool args = %q, want readable query", got.ToolArgs)
	}
}

func TestProjectGatewayEventACPFetchWithoutRawQueryKeepsGenericSearchTitle(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind:   gateway.EventKindToolCall,
		Origin: &gateway.EventOrigin{Source: "acp_participant", Scope: gateway.EventScopeParticipant, ScopeID: "codex-001"},
		ToolCall: &gateway.ToolCallPayload{
			CallID:    "ws-1",
			ToolTitle: "Searching the Web",
			ToolKind:  "fetch",
			Status:    gateway.ToolStatusRunning,
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool event", events)
	}
	got := events[0]
	if got.ToolArgs != "Searching the Web" {
		t.Fatalf("tool args = %q, want original title", got.ToolArgs)
	}
	if strings.Contains(got.ToolArgs, `"the Web"`) {
		t.Fatalf("tool args = %q, must not derive fake query from generic title", got.ToolArgs)
	}
}

func TestProjectGatewayEventACPFetchResultKeepsInputQueryWhenOutputHasText(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind:   gateway.EventKindToolResult,
		Origin: &gateway.EventOrigin{Source: "acp_participant", Scope: gateway.EventScopeParticipant, ScopeID: "codex-001"},
		ToolResult: &gateway.ToolResultPayload{
			CallID:    "ws-1",
			ToolTitle: "Searching the Web",
			ToolKind:  "fetch",
			Status:    gateway.ToolStatusCompleted,
			RawInput: map[string]any{
				"query": "weather: Shanghai, China",
			},
			RawOutput: map[string]any{
				"text": "result 01\nresult 02",
			},
			Content: testToolContent("result 01\nresult 02"),
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool event", events)
	}
	got := events[0]
	if got.ToolArgs != `"weather: Shanghai, China"` {
		t.Fatalf("tool args = %q, want original query", got.ToolArgs)
	}
	if strings.Contains(got.ToolArgs, "result 01") {
		t.Fatalf("tool args = %q, must not use result text as query", got.ToolArgs)
	}
}

func TestProjectGatewayEventACPToolArgsUseKindAndDoNotLeakTransportSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		title     string
		kind      string
		raw       map[string]any
		wantName  string
		wantArgs  string
		forbidden []string
	}{
		{
			name:     "codex read from parsed command",
			title:    "Read README.md",
			kind:     "read",
			wantName: "read",
			wantArgs: "README.md",
			raw: map[string]any{
				"command": []any{"/bin/zsh", "-lc", "cat README.md"},
				"parsed_cmd": []any{map[string]any{
					"type": "read",
					"cmd":  "cat README.md",
					"name": "README.md",
					"path": "README.md",
				}},
				"source": "unified_exec_startup",
			},
			forbidden: []string{"unified_exec_startup", "Read README.md"},
		},
		{
			name:     "codex search from parsed command",
			title:    "Search func WithDbTransaction|WithDbTransaction in repository",
			kind:     "search",
			wantName: "search",
			wantArgs: `"func WithDbTransaction|WithDbTransaction"`,
			raw: map[string]any{
				"command": []any{"/bin/zsh", "-lc", `rg -n "func WithDbTransaction|WithDbTransaction"`},
				"parsed_cmd": []any{map[string]any{
					"type":  "search",
					"cmd":   "rg -n func WithDbTransaction|WithDbTransaction",
					"query": "func WithDbTransaction|WithDbTransaction",
				}},
				"source": "unified_exec_startup",
			},
			forbidden: []string{"unified_exec_startup", "repository"},
		},
		{
			name:     "execute from shell command array",
			title:    "git status --short --branch",
			kind:     "execute",
			wantName: "execute",
			wantArgs: "git status --short --branch",
			raw: map[string]any{
				"command": []any{"/bin/zsh", "-lc", "git status --short --branch"},
				"source":  "unified_exec_startup",
			},
			forbidden: []string{"unified_exec_startup", "/bin/zsh -lc"},
		},
		{
			name:      "read title fallback strips action prefix",
			title:     "Read README.md",
			kind:      "read",
			wantName:  "read",
			wantArgs:  "README.md",
			raw:       nil,
			forbidden: []string{"Read README.md"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
				Kind:   gateway.EventKindToolCall,
				Origin: &gateway.EventOrigin{Source: "acp_participant", Scope: gateway.EventScopeParticipant, ScopeID: "codex-001"},
				ToolCall: &gateway.ToolCallPayload{
					CallID:    "call-1",
					ToolTitle: tt.title,
					ToolKind:  tt.kind,
					Status:    gateway.ToolStatusRunning,
					RawInput:  tt.raw,
				},
			})
			if len(events) != 1 {
				t.Fatalf("events = %#v, want one tool event", events)
			}
			got := events[0]
			if got.ToolName != tt.wantName {
				t.Fatalf("tool name = %q, want %q", got.ToolName, tt.wantName)
			}
			if got.ToolTitle != tt.title {
				t.Fatalf("tool title = %q, want %q", got.ToolTitle, tt.title)
			}
			if got.ToolArgs != tt.wantArgs {
				t.Fatalf("tool args = %q, want %q", got.ToolArgs, tt.wantArgs)
			}
			for _, forbidden := range tt.forbidden {
				if strings.Contains(got.ToolArgs, forbidden) {
					t.Fatalf("tool args = %q, must not contain %q", got.ToolArgs, forbidden)
				}
			}
		})
	}
}

func TestProjectGatewayEventRefinedListSummaryUsesRuntimeMeta(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind: gateway.EventKindToolResult,
		Meta: map[string]any{
			"caelis": map[string]any{
				"runtime": map[string]any{
					"tool": map[string]any{
						"path":  "docs",
						"count": 3,
					},
				},
			},
		},
		ToolResult: &gateway.ToolResultPayload{
			CallID:    "list-files",
			ToolKind:  "search",
			ToolTitle: "Search files",
			Status:    gateway.ToolStatusCompleted,
			RawInput: map[string]any{
				"parsed_cmd": []any{map[string]any{
					"type": "list_files",
				}},
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool event", events)
	}
	got := events[0]
	if got.ToolName != "LIST" {
		t.Fatalf("ToolName = %q, want LIST", got.ToolName)
	}
	if got.ToolArgs != "docs 3 entries" {
		t.Fatalf("ToolArgs = %q, want runtime meta list summary", got.ToolArgs)
	}
}

func TestProjectGatewayEventTaskArgsUseProtocolRawInput(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind: gateway.EventKindToolCall,
		ToolCall: &gateway.ToolCallPayload{
			CallID:   "task-call",
			ToolName: "TASK",
			Status:   gateway.ToolStatusRunning,
			RawInput: map[string]any{"action": "wait", "task_id": "emma", "yield_time_ms": 3000},
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool event", events)
	}
	got := events[0]
	if got.ToolArgs != "Wait emma 3s" {
		t.Fatalf("ToolArgs = %q, want protocol-derived TASK args", got.ToolArgs)
	}
	if got.ToolTaskID != "emma" {
		t.Fatalf("ToolTaskID = %q, want emma", got.ToolTaskID)
	}
}

func TestProjectGatewayEventTaskResultPrefersOutputHandleInArgs(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind: gateway.EventKindToolResult,
		Meta: testRuntimeToolMeta(map[string]any{
			"action":      "wait",
			"target_id":   "jeff",
			"target_kind": "subagent",
		}),
		ToolResult: &gateway.ToolResultPayload{
			CallID:   "task-result",
			ToolName: "TASK",
			Status:   gateway.ToolStatusRunning,
			RawInput: map[string]any{"action": "wait", "task_id": "self", "yield_time_ms": 3000},
			RawOutput: map[string]any{
				"action":      "wait",
				"task_id":     "jeff",
				"target_kind": "subagent",
				"running":     true,
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool event", events)
	}
	got := events[0]
	if got.ToolArgs != "Wait jeff 3s" {
		t.Fatalf("ToolArgs = %q, want output handle in TASK args", got.ToolArgs)
	}
	if got.ToolTaskID != "jeff" {
		t.Fatalf("ToolTaskID = %q, want jeff", got.ToolTaskID)
	}
}

func TestProjectGatewayEventTaskWaitHidesSuccessfulOutput(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind: gateway.EventKindToolResult,
		Meta: testRuntimeToolMeta(map[string]any{
			"action":    "wait",
			"target_id": "jeff",
		}),
		ToolResult: &gateway.ToolResultPayload{
			CallID:   "task-wait",
			ToolName: "TASK",
			Status:   gateway.ToolStatusCompleted,
			RawInput: map[string]any{"action": "wait", "task_id": "jeff"},
			Content:  testToolContent("final task output\n"),
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool event", events)
	}
	if got := events[0].ToolArgs; got != "Wait jeff" {
		t.Fatalf("ToolArgs = %q, want Wait jeff", got)
	}
	if got := events[0].ToolOutput; got != "" {
		t.Fatalf("ToolOutput = %q, want no TASK wait display output", got)
	}
}

func TestProjectGatewayEventTaskControlsHideRawOutput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		action  string
		status  gateway.ToolStatus
		output  map[string]any
		content string
		wantArg string
	}{
		{
			name:   "failed wait",
			action: "wait",
			status: gateway.ToolStatusFailed,
			output: map[string]any{
				"state":     "failed",
				"result":    "TASK_WAIT_FAILED_RAW_RESULT\n",
				"error":     "TASK_WAIT_FAILED_RAW_ERROR",
				"exit_code": 1,
			},
			content: "TASK_WAIT_FAILED_CONTENT\n",
			wantArg: "Wait jeff",
		},
		{
			name:   "cancel",
			action: "cancel",
			status: gateway.ToolStatusCancelled,
			output: map[string]any{
				"state":     "cancelled",
				"result":    "TASK_CANCEL_RAW_RESULT\n",
				"error":     "TASK_CANCEL_RAW_ERROR",
				"exit_code": -1,
			},
			content: "TASK_CANCEL_CONTENT\n",
			wantArg: "Cancel jeff",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
				Kind: gateway.EventKindToolResult,
				Meta: testRuntimeToolMeta(map[string]any{
					"action":    tc.action,
					"target_id": "jeff",
				}),
				ToolResult: &gateway.ToolResultPayload{
					CallID:    "task-" + tc.action,
					ToolName:  "TASK",
					Status:    tc.status,
					RawInput:  map[string]any{"action": tc.action, "task_id": "jeff"},
					RawOutput: tc.output,
					Content:   testToolContent(tc.content),
				},
			})
			if len(events) != 1 {
				t.Fatalf("events = %#v, want one tool event", events)
			}
			if got := events[0].ToolArgs; got != tc.wantArg {
				t.Fatalf("ToolArgs = %q, want %q", got, tc.wantArg)
			}
			if got := events[0].ToolOutput; got != "" {
				t.Fatalf("ToolOutput = %q, want no TASK control display output", got)
			}
		})
	}
}

func TestProjectGatewayEventTaskResultShowsEffectiveWaitDuration(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind: gateway.EventKindToolResult,
		Meta: testRuntimeToolMeta(map[string]any{
			"action":                  "wait",
			"target_id":               "task-7",
			"target_kind":             "command",
			"effective_yield_time_ms": 7000,
			"yield_time_ms_defaulted": true,
		}),
		ToolResult: &gateway.ToolResultPayload{
			CallID:   "task-result",
			ToolName: "TASK",
			Status:   gateway.ToolStatusRunning,
			RawInput: map[string]any{"action": "wait", "task_id": "task-7"},
			RawOutput: map[string]any{
				"task_id": "task-7",
				"state":   "running",
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool event", events)
	}
	if got := events[0].ToolArgs; got != "Wait 7s" {
		t.Fatalf("ToolArgs = %q, want default effective wait duration", got)
	}
}

func TestProjectGatewayEventTaskResultHidesWaitUntilDoneDuration(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind: gateway.EventKindToolResult,
		Meta: testRuntimeToolMeta(map[string]any{
			"action":                  "wait",
			"target_id":               "sima",
			"target_kind":             "subagent",
			"effective_yield_time_ms": 300000,
			"yield_time_ms_defaulted": true,
			"wait_until_done":         true,
		}),
		ToolResult: &gateway.ToolResultPayload{
			CallID:   "task-result",
			ToolName: "TASK",
			Status:   gateway.ToolStatusRunning,
			RawInput: map[string]any{"action": "wait", "task_id": "self"},
			RawOutput: map[string]any{
				"task_id": "sima",
				"state":   "running",
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool event", events)
	}
	if got := events[0].ToolArgs; got != "Wait sima" {
		t.Fatalf("ToolArgs = %q, want compact wait display", got)
	}
	if got := events[0].ToolOutput; got != "" {
		t.Fatalf("ToolOutput = %q, want no TASK wait display output", got)
	}
}

func TestProjectGatewayEventTaskResultShowsExplicitZeroWaitDuration(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind: gateway.EventKindToolResult,
		Meta: testRuntimeToolMeta(map[string]any{
			"action":                  "wait",
			"target_id":               "jeff",
			"target_kind":             "subagent",
			"effective_yield_time_ms": 0,
		}),
		ToolResult: &gateway.ToolResultPayload{
			CallID:   "task-result",
			ToolName: "TASK",
			Status:   gateway.ToolStatusRunning,
			RawInput: map[string]any{"action": "wait", "task_id": "self", "yield_time_ms": 0},
			RawOutput: map[string]any{
				"task_id": "jeff",
				"state":   "running",
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool event", events)
	}
	if got := events[0].ToolArgs; got != "Wait jeff 0ms" {
		t.Fatalf("ToolArgs = %q, want explicit zero wait duration", got)
	}
}

func TestProjectGatewayEventTaskWriteUsesCaelisMetaTarget(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind: gateway.EventKindToolResult,
		Meta: map[string]any{
			"caelis": map[string]any{
				"runtime": map[string]any{
					"tool": map[string]any{
						"action":      "write",
						"target_kind": "subagent",
						"target_id":   "maya",
						"input":       "请在文件末尾再加 2 行",
					},
				},
			},
		},
		ToolResult: &gateway.ToolResultPayload{
			CallID:   "task-write",
			ToolName: "TASK",
			ToolKind: "execute",
			Status:   gateway.ToolStatusCompleted,
			RawInput: map[string]any{"action": "write", "task_id": "internal-task", "input": "fallback"},
			RawOutput: map[string]any{
				"result": "已追加",
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool event", events)
	}
	got := events[0]
	if got.ToolTaskAction != "write" || got.ToolTaskTargetKind != "subagent" || got.ToolTaskID != "maya" || got.ToolTaskInput != "请在文件末尾再加 2 行" {
		t.Fatalf("task fields = action:%q target:%q id:%q input:%q", got.ToolTaskAction, got.ToolTaskTargetKind, got.ToolTaskID, got.ToolTaskInput)
	}
}

func TestProjectGatewayEventPreservesStreamParentAnchor(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind: gateway.EventKindAssistantMessage,
		Meta: map[string]any{
			"caelis": map[string]any{
				"runtime": map[string]any{
					"stream": map[string]any{
						"parent_call_id": "spawn-1",
						"parent_tool":    "SPAWN",
					},
				},
			},
		},
		Narrative: &gateway.NarrativePayload{
			Role: gateway.NarrativeRoleAssistant,
			Text: "child output",
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one narrative event", events)
	}
	if events[0].AnchorToolCallID != "spawn-1" || events[0].AnchorToolName != "SPAWN" {
		t.Fatalf("anchor = (%q, %q), want spawn parent", events[0].AnchorToolCallID, events[0].AnchorToolName)
	}
}

func TestProjectGatewayEventToolResultDoesNotDisplayRawOutputOnly(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind: gateway.EventKindToolResult,
		ToolResult: &gateway.ToolResultPayload{
			CallID:    "command-raw-only",
			ToolName:  "RUN_COMMAND",
			Status:    gateway.ToolStatusCompleted,
			RawInput:  map[string]any{"command": "go mod tidy"},
			RawOutput: map[string]any{"stdout": "network error that must not be rendered"},
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool event", events)
	}
	if got := events[0].ToolOutput; got != "" {
		t.Fatalf("ToolOutput = %q, want no synthesized terminal output", got)
	}
	if strings.Contains(events[0].ToolOutput, "network error") {
		t.Fatalf("ToolOutput = %q, must not display rawOutput-only text", events[0].ToolOutput)
	}
}

func TestProjectGatewayEventTerminalFinalDisplaysNoOutputPlaceholder(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind: gateway.EventKindToolResult,
		ToolResult: &gateway.ToolResultPayload{
			CallID:    "command-empty",
			ToolName:  "RUN_COMMAND",
			Status:    gateway.ToolStatusCompleted,
			RawInput:  map[string]any{"command": "true"},
			RawOutput: map[string]any{"state": "completed", "exit_code": 0},
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool event", events)
	}
	if got := events[0].ToolOutput; got != "(no output)" {
		t.Fatalf("ToolOutput = %q, want no-output placeholder", got)
	}
	if !events[0].ToolOutputSynthetic {
		t.Fatalf("ToolOutputSynthetic = false, want synthetic placeholder")
	}
}

func TestProjectGatewayEventACPExecuteFinalDisplaysNoOutputPlaceholder(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind: gateway.EventKindToolResult,
		Protocol: &session.EventProtocol{
			UpdateType: string(session.ProtocolUpdateTypeToolUpdate),
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
				ToolCallID:    "acp-empty",
				Kind:          "execute",
				Title:         "Terminal",
				Status:        "completed",
				RawInput:      map[string]any{"command": "true"},
				RawOutput:     map[string]any{"exit_code": 0},
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one protocol tool event", events)
	}
	if got := events[0].ToolOutput; got != "(no output)" {
		t.Fatalf("ToolOutput = %q, want no-output placeholder", got)
	}
	if !events[0].ToolOutputSynthetic {
		t.Fatalf("ToolOutputSynthetic = false, want synthetic placeholder")
	}
}

func TestProjectGatewayEventACPExecuteInfersFinalNoOutputFromExitCode(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind: gateway.EventKindToolResult,
		Protocol: &session.EventProtocol{
			UpdateType: string(session.ProtocolUpdateTypeToolUpdate),
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
				ToolCallID:    "acp-empty-no-status",
				Kind:          "execute",
				Title:         "Terminal",
				RawInput:      map[string]any{"command": "git diff --check"},
				RawOutput:     map[string]any{"exit_code": 0},
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one protocol tool event", events)
	}
	if got := events[0].ToolStatus; got != string(gateway.ToolStatusCompleted) {
		t.Fatalf("ToolStatus = %q, want completed", got)
	}
	if got := events[0].ToolOutput; got != "(no output)" {
		t.Fatalf("ToolOutput = %q, want no-output placeholder", got)
	}
	if !events[0].ToolOutputSynthetic {
		t.Fatalf("ToolOutputSynthetic = false, want synthetic placeholder")
	}
}

func TestProjectGatewayEventACPToolUpdateMapsTerminatedStateToFinalStatus(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind: gateway.EventKindToolResult,
		Protocol: &session.EventProtocol{
			UpdateType: string(session.ProtocolUpdateTypeToolUpdate),
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
				ToolCallID:    "acp-terminated",
				Kind:          "execute",
				Title:         "Terminal",
				RawInput:      map[string]any{"command": "sleep 10"},
				RawOutput:     map[string]any{"state": "terminated"},
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one protocol tool event", events)
	}
	if got := events[0].ToolStatus; got != string(gateway.ToolStatusInterrupted) {
		t.Fatalf("ToolStatus = %q, want interrupted", got)
	}
	if !events[0].Final {
		t.Fatalf("Final = false, want terminated raw state to project as final")
	}
}

func TestProjectGatewayEventACPDiffCompactsEditTitleAndFileHeader(t *testing.T) {
	t.Parallel()

	oldText := "old line\n"
	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind: gateway.EventKindToolResult,
		Protocol: &session.EventProtocol{
			UpdateType: string(session.ProtocolUpdateTypeToolUpdate),
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
				ToolCallID:    "edit-1",
				Kind:          "edit",
				Title:         "Edit /home/xueyongzhi/WorkDir/code/caelis/internal/adapters/store/memory/store_test.go",
				Status:        "completed",
				Content: []session.ProtocolToolCallContent{{
					Type:    "diff",
					Path:    "/home/xueyongzhi/WorkDir/code/caelis/internal/adapters/store/memory/store_test.go",
					OldText: &oldText,
					NewText: "new line\n",
				}},
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one protocol tool event", events)
	}
	if got := events[0].ToolArgs; got != "store_test.go +1 -1" {
		t.Fatalf("ToolArgs = %q, want compact diff header", got)
	}
	if strings.Contains(events[0].ToolOutput, "/home/xueyongzhi/") {
		t.Fatalf("ToolOutput leaked absolute path: %q", events[0].ToolOutput)
	}
	for _, want := range []string{"store_test.go +1 -1", "@@ -1,1 +1,1 @@", "-old line", "+new line"} {
		if !strings.Contains(events[0].ToolOutput, want) {
			t.Fatalf("ToolOutput = %q, want %q", events[0].ToolOutput, want)
		}
	}
}

func TestProjectGatewayEventTerminalReferenceFinalDisplaysNoOutputPlaceholder(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind: gateway.EventKindToolResult,
		ToolResult: &gateway.ToolResultPayload{
			CallID:    "terminal-ref-empty",
			ToolName:  "RUN_COMMAND",
			Status:    gateway.ToolStatusCompleted,
			RawInput:  map[string]any{"command": "true"},
			RawOutput: map[string]any{"exit_code": 0},
			Content: []session.ProtocolToolCallContent{{
				Type:       "terminal",
				TerminalID: "term-empty",
			}},
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool event", events)
	}
	if got := events[0].ToolOutput; got != "(no output)" {
		t.Fatalf("ToolOutput = %q, want no-output placeholder", got)
	}
	if !events[0].ToolOutputSynthetic {
		t.Fatalf("ToolOutputSynthetic = false, want synthetic placeholder")
	}
}

func TestProjectGatewayEventToolResultDiscardsUnsupportedACPContent(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind: gateway.EventKindToolResult,
		ToolResult: &gateway.ToolResultPayload{
			CallID:   "tool-future-content",
			ToolName: "RUN_COMMAND",
			Status:   gateway.ToolStatusCompleted,
			Content: []session.ProtocolToolCallContent{{
				Type:    "image",
				Content: map[string]any{"type": "image", "url": "ignored"},
			}},
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool event", events)
	}
	if got := events[0].ToolOutput; got != "" {
		t.Fatalf("ToolOutput = %q, want unsupported terminal content discarded without synthetic output", got)
	}
}

func TestProjectGatewayEventToolResultSeparatesTerminalContentItems(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(gateway.Event{
		Kind: gateway.EventKindToolResult,
		ToolResult: &gateway.ToolResultPayload{
			CallID:   "command-lines",
			ToolName: "RUN_COMMAND",
			Status:   gateway.ToolStatusCompleted,
			RawInput: map[string]any{"command": "Get-ChildItem -Name"},
			Content: []session.ProtocolToolCallContent{
				{Type: "terminal", TerminalID: "command-lines", Content: session.ProtocolTextContent("caelis")},
				{Type: "terminal", TerminalID: "command-lines", Content: session.ProtocolTextContent("codex")},
				{Type: "terminal", TerminalID: "command-lines", Content: session.ProtocolTextContent("demo")},
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool event", events)
	}
	if got := events[0].ToolOutput; got != "caelis\ncodex\ndemo" {
		t.Fatalf("ToolOutput = %q, want terminal content items separated by newlines", got)
	}
}

func snapshotTranscriptModel(m *Model) string {
	lines := make([]string, 0, len(m.doc.Blocks())*2)
	for _, block := range m.doc.Blocks() {
		switch typed := block.(type) {
		case *MainACPTurnBlock:
			lines = append(lines, "Main(session="+typed.SessionID+",status="+typed.Status+")")
			for _, ev := range typed.Events {
				lines = append(lines, "  "+snapshotSubagentEvent(ev))
			}
		case *ParticipantTurnBlock:
			lines = append(lines, "Participant(session="+typed.SessionID+",status="+typed.Status+")")
			for _, ev := range typed.Events {
				lines = append(lines, "  "+snapshotSubagentEvent(ev))
			}
		case *TranscriptBlock:
			text := strings.TrimSpace(typed.Raw)
			if text != "" {
				lines = append(lines, "Transcript("+text+")")
			}
		}
	}
	return strings.Join(lines, "\n")
}

func snapshotSubagentEvent(ev SubagentEvent) string {
	switch ev.Kind {
	case SEAssistant:
		return "assistant:" + strings.TrimSpace(ev.Text)
	case SEReasoning:
		return "reasoning:" + strings.TrimSpace(ev.Text)
	case SEToolCall:
		status := "running"
		if ev.Done {
			if ev.Err {
				status = "failed"
			} else {
				status = "done"
			}
		}
		return "tool(" + ev.CallID + "," + ev.Name + "," + status + ",args=" + strings.TrimSpace(ev.Args) + ",output=" + strings.TrimSpace(ev.Output) + ")"
	case SEPlan:
		parts := make([]string, 0, len(ev.PlanEntries))
		for _, entry := range ev.PlanEntries {
			parts = append(parts, entry.Status+":"+entry.Content)
		}
		return "plan:" + strings.Join(parts, ",")
	case SEApproval:
		return "approval:" + strings.TrimSpace(ev.ApprovalTool) + "|" + strings.TrimSpace(ev.ApprovalCommand) + "|" + strings.TrimSpace(ev.ApprovalStatus) + "|" + strings.TrimSpace(ev.ApprovalRisk) + "|" + strings.TrimSpace(ev.ApprovalAuth) + "|" + strings.TrimSpace(ev.ApprovalText)
	default:
		return "unknown"
	}
}
