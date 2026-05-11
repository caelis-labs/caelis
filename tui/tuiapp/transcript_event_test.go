package tuiapp

import (
	"strings"
	"testing"

	appgateway "github.com/OnslaughtSnail/caelis/gateway"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func TestProjectGatewayEventToTranscriptEvents_AssistantAndUsage(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(appgateway.Event{
		Kind:       appgateway.EventKindAssistantMessage,
		SessionRef: sdksession.SessionRef{SessionID: "root-session"},
		Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
		Narrative: &appgateway.NarrativePayload{
			Role:          appgateway.NarrativeRoleAssistant,
			Actor:         "assistant",
			ReasoningText: "think",
			Text:          "answer",
			Final:         true,
		},
		Usage: &appgateway.UsageSnapshot{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
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

	events := ProjectGatewayEventToTranscriptEvents(appgateway.Event{
		Kind:       appgateway.EventKindApprovalRequested,
		SessionRef: sdksession.SessionRef{SessionID: "root-session"},
		Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
		ApprovalPayload: &appgateway.ApprovalPayload{
			ToolName: "BASH",
			RawInput: map[string]any{"command": "go test ./gateway/..."},
			Status:   appgateway.ApprovalStatusPending,
		},
	})

	if len(events) != 0 {
		t.Fatalf("ProjectGatewayEventToTranscriptEvents() = %#v, want no persisted approval transcript events", events)
	}
}

func TestProjectGatewayEventToTranscriptEvents_ProjectsTerminalAutomaticApprovalReview(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(appgateway.Event{
		Kind:       appgateway.EventKindApprovalReview,
		SessionRef: sdksession.SessionRef{SessionID: "root-session"},
		Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
		ApprovalPayload: &appgateway.ApprovalPayload{
			ToolCallID:     "perm-call-1",
			ToolName:       "request_permissions",
			RawInput:       map[string]any{"reason": "need access"},
			ReviewStatus:   appgateway.ApprovalReviewStatusApproved,
			DecisionSource: "auto-review",
			ReviewText:     "Automatic approval review approved (risk: low, authorization: high): required by task.",
			Risk:           "low",
			Authorization:  "high",
		},
	})

	if got := len(events); got != 1 {
		t.Fatalf("len(events) = %d, want 1", got)
	}
	if events[0].Kind != TranscriptEventApproval || events[0].ApprovalStatus != string(appgateway.ApprovalReviewStatusApproved) {
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

	pending := ProjectGatewayEventToTranscriptEvents(appgateway.Event{
		Kind:       appgateway.EventKindApprovalReview,
		SessionRef: sdksession.SessionRef{SessionID: "root-session"},
		Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
		ApprovalPayload: &appgateway.ApprovalPayload{
			ToolName:       "request_permissions",
			ReviewStatus:   appgateway.ApprovalReviewStatusInProgress,
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
		event appgateway.Event
	}{
		{
			name: "user event",
			event: appgateway.Event{
				Kind:       appgateway.EventKindUserMessage,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				Origin: &appgateway.EventOrigin{
					Scope:         appgateway.EventScopeParticipant,
					ScopeID:       "participant-turn-1",
					ParticipantID: "participant-1",
					Actor:         "@jeff",
				},
				Narrative: &appgateway.NarrativePayload{
					Role:  appgateway.NarrativeRoleUser,
					Text:  "总结一下工作",
					Scope: appgateway.EventScopeParticipant,
				},
			},
		},
		{
			name: "assistant user role",
			event: appgateway.Event{
				Kind:       appgateway.EventKindAssistantMessage,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				Origin: &appgateway.EventOrigin{
					Scope:         appgateway.EventScopeParticipant,
					ScopeID:       "participant-turn-1",
					ParticipantID: "participant-1",
					Actor:         "@jeff",
				},
				Narrative: &appgateway.NarrativePayload{
					Role:  appgateway.NarrativeRoleUser,
					Text:  "总结一下工作",
					Scope: appgateway.EventScopeParticipant,
				},
			},
		},
		{
			name: "payload scope without origin",
			event: appgateway.Event{
				Kind:       appgateway.EventKindUserMessage,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				Narrative: &appgateway.NarrativePayload{
					Role:          appgateway.NarrativeRoleUser,
					Text:          "总结一下工作",
					Scope:         appgateway.EventScopeParticipant,
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

	events := ProjectGatewayEventToTranscriptEvents(appgateway.Event{
		Kind:       appgateway.EventKindUserMessage,
		SessionRef: sdksession.SessionRef{SessionID: "root-session"},
		Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
		Narrative: &appgateway.NarrativePayload{
			Role:  appgateway.NarrativeRoleUser,
			Text:  "/claude 总结一下工作",
			Scope: appgateway.EventScopeMain,
		},
	})

	if got := len(events); got != 1 {
		t.Fatalf("len(events) = %d, want 1", got)
	}
	if events[0].Kind != TranscriptEventNarrative || events[0].NarrativeKind != TranscriptNarrativeUser || events[0].Text != "/claude 总结一下工作" {
		t.Fatalf("events[0] = %#v, want main user narrative", events[0])
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
				updated, _ := m.Update(appgateway.EventEnvelope{
					Event: appgateway.Event{
						Kind:       appgateway.EventKindAssistantMessage,
						SessionRef: sdksession.SessionRef{SessionID: "root-session"},
						Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
						Narrative: &appgateway.NarrativePayload{
							Role:  appgateway.NarrativeRoleAssistant,
							Text:  "hello ",
							Final: false,
						},
					},
				})
				m = updated.(*Model)
				updated, _ = m.Update(appgateway.EventEnvelope{
					Event: appgateway.Event{
						Kind:       appgateway.EventKindAssistantMessage,
						SessionRef: sdksession.SessionRef{SessionID: "root-session"},
						Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
						Narrative: &appgateway.NarrativePayload{
							Role:  appgateway.NarrativeRoleAssistant,
							Text:  "hello world",
							Final: true,
						},
					},
				})
				return updated.(*Model)
			},
			want: "Main(session=root-session,status=running)\n  assistant:hello world",
		},
		{
			name: "tool call output result",
			run: func(m *Model) *Model {
				updated, _ := m.Update(appgateway.EventEnvelope{
					Event: appgateway.Event{
						Kind:       appgateway.EventKindToolCall,
						SessionRef: sdksession.SessionRef{SessionID: "root-session"},
						Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
						ToolCall: &appgateway.ToolCallPayload{
							CallID:   "call-1",
							ToolName: "BASH",
							RawInput: map[string]any{"command": `echo "hi"`},
							Status:   appgateway.ToolStatusRunning,
						},
					},
				})
				m = updated.(*Model)
				updated, _ = m.Update(appgateway.EventEnvelope{
					Event: appgateway.Event{
						Kind:       appgateway.EventKindToolResult,
						SessionRef: sdksession.SessionRef{SessionID: "root-session"},
						Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
						ToolResult: &appgateway.ToolResultPayload{
							CallID:    "call-1",
							ToolName:  "BASH",
							RawInput:  map[string]any{"command": `echo "hi"`},
							RawOutput: map[string]any{"text": "line 1"},
							Status:    appgateway.ToolStatusRunning,
						},
					},
				})
				m = updated.(*Model)
				updated, _ = m.Update(appgateway.EventEnvelope{
					Event: appgateway.Event{
						Kind:       appgateway.EventKindToolResult,
						SessionRef: sdksession.SessionRef{SessionID: "root-session"},
						Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
						ToolResult: &appgateway.ToolResultPayload{
							CallID:    "call-1",
							ToolName:  "BASH",
							RawInput:  map[string]any{"command": `echo "hi"`},
							RawOutput: map[string]any{"stdout": "done"},
							Status:    appgateway.ToolStatusCompleted,
						},
					},
				})
				return updated.(*Model)
			},
			want: "Main(session=root-session,status=running)\n  tool(call-1,BASH,done,args=echo \"hi\",output=done)",
		},
		{
			name: "approval overlay is not transcript",
			run: func(m *Model) *Model {
				updated, _ := m.Update(appgateway.EventEnvelope{
					Event: appgateway.Event{
						Kind:       appgateway.EventKindApprovalRequested,
						SessionRef: sdksession.SessionRef{SessionID: "root-session"},
						Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
						ApprovalPayload: &appgateway.ApprovalPayload{
							ToolName: "BASH",
							RawInput: map[string]any{"command": "rm -rf /tmp/demo"},
							Status:   appgateway.ApprovalStatusPending,
						},
					},
				})
				return updated.(*Model)
			},
			want: "",
		},
		{
			name: "participant and subagent lanes",
			run: func(m *Model) *Model {
				updated, _ := m.Update(appgateway.EventEnvelope{
					Event: appgateway.Event{
						Kind:       appgateway.EventKindAssistantMessage,
						SessionRef: sdksession.SessionRef{SessionID: "participant-session"},
						Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeParticipant, ScopeID: "participant-session"},
						Narrative: &appgateway.NarrativePayload{
							Role:  appgateway.NarrativeRoleAssistant,
							Text:  "participant answer",
							Final: true,
						},
					},
				})
				m = updated.(*Model)
				updated, _ = m.Update(appgateway.EventEnvelope{
					Event: appgateway.Event{
						Kind:       appgateway.EventKindLifecycle,
						SessionRef: sdksession.SessionRef{SessionID: "participant-session"},
						Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeParticipant, ScopeID: "participant-session"},
						Lifecycle: &appgateway.LifecyclePayload{
							Status: appgateway.LifecycleStatusCompleted,
						},
					},
				})
				m = updated.(*Model)
				updated, _ = m.Update(appgateway.EventEnvelope{
					Event: appgateway.Event{
						Kind:       appgateway.EventKindAssistantMessage,
						SessionRef: sdksession.SessionRef{SessionID: "root-session"},
						Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeSubagent, ScopeID: "spawn-1", Actor: "copilot"},
						Narrative: &appgateway.NarrativePayload{
							Role:  appgateway.NarrativeRoleAssistant,
							Text:  "subagent answer",
							Final: true,
						},
					},
				})
				m = updated.(*Model)
				updated, _ = m.Update(SubagentStatusMsg{SpawnID: "spawn-1", State: "completed"})
				return updated.(*Model)
			},
			want: "Participant(session=participant-session,status=completed)\n  assistant:participant answer\nSubagent(spawn=spawn-1,status=completed)\n  assistant:subagent answer",
		},
		{
			name: "replayed durable events",
			run: func(m *Model) *Model {
				for _, env := range []appgateway.EventEnvelope{
					{
						Cursor: "c1",
						Event: appgateway.Event{
							Kind:       appgateway.EventKindAssistantMessage,
							SessionRef: sdksession.SessionRef{SessionID: "root-session"},
							Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
							Narrative: &appgateway.NarrativePayload{
								Role:  appgateway.NarrativeRoleAssistant,
								Text:  "durable answer",
								Final: true,
							},
						},
					},
					{
						Cursor: "c2",
						Event: appgateway.Event{
							Kind:       appgateway.EventKindLifecycle,
							SessionRef: sdksession.SessionRef{SessionID: "root-session"},
							Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
							Lifecycle: &appgateway.LifecyclePayload{
								Status: appgateway.LifecycleStatusCompleted,
							},
						},
					},
				} {
					updated, _ := m.Update(env)
					m = updated.(*Model)
				}
				return m
			},
			want: "Main(session=root-session,status=completed)\n  assistant:durable answer",
		},
		{
			name: "interrupted turn",
			run: func(m *Model) *Model {
				updated, _ := m.Update(appgateway.EventEnvelope{
					Event: appgateway.Event{
						Kind:       appgateway.EventKindToolCall,
						SessionRef: sdksession.SessionRef{SessionID: "root-session"},
						Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
						ToolCall: &appgateway.ToolCallPayload{
							CallID:   "call-1",
							ToolName: "READ",
							RawInput: map[string]any{"path": "/tmp/demo"},
							Status:   appgateway.ToolStatusRunning,
						},
					},
				})
				m = updated.(*Model)
				updated, _ = m.Update(appgateway.EventEnvelope{
					Event: appgateway.Event{
						Kind:       appgateway.EventKindLifecycle,
						SessionRef: sdksession.SessionRef{SessionID: "root-session"},
						Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
						Lifecycle: &appgateway.LifecyclePayload{
							Status: appgateway.LifecycleStatusInterrupted,
						},
					},
				})
				return updated.(*Model)
			},
			want: "Main(session=root-session,status=interrupted)\n  tool(call-1,READ,running,args=/tmp/demo,output=)",
		},
		{
			name: "failed tool call",
			run: func(m *Model) *Model {
				updated, _ := m.Update(appgateway.EventEnvelope{
					Event: appgateway.Event{
						Kind:       appgateway.EventKindToolCall,
						SessionRef: sdksession.SessionRef{SessionID: "root-session"},
						Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
						ToolCall: &appgateway.ToolCallPayload{
							CallID:   "call-1",
							ToolName: "BASH",
							RawInput: map[string]any{"command": "false"},
							Status:   appgateway.ToolStatusRunning,
						},
					},
				})
				m = updated.(*Model)
				updated, _ = m.Update(appgateway.EventEnvelope{
					Event: appgateway.Event{
						Kind:       appgateway.EventKindToolResult,
						SessionRef: sdksession.SessionRef{SessionID: "root-session"},
						Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeMain, ScopeID: "root-session"},
						ToolResult: &appgateway.ToolResultPayload{
							CallID:    "call-1",
							ToolName:  "BASH",
							RawInput:  map[string]any{"command": "false"},
							RawOutput: map[string]any{"stderr": "exit 1"},
							Status:    appgateway.ToolStatusFailed,
							Error:     true,
						},
					},
				})
				return updated.(*Model)
			},
			want: "Main(session=root-session,status=running)\n  tool(call-1,BASH,failed,args=false,output=exit 1)",
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
	for _, env := range []appgateway.EventEnvelope{
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeSubagent, ScopeID: "child-1", Actor: "copilot"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "BASH",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeSubagent,
				RawInput: map[string]any{"command": "go test ./tui/tuiapp/..."},
			},
		}},
		{Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Origin:     &appgateway.EventOrigin{Scope: appgateway.EventScopeSubagent, ScopeID: "child-1", Actor: "copilot"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:   "call-1",
				ToolName: "BASH",
				Status:   appgateway.ToolStatusCompleted,
				Scope:    appgateway.EventScopeSubagent,
				RawInput: map[string]any{"command": "go test ./tui/tuiapp/..."},
				RawOutput: map[string]any{
					"stdout":    "ok\n",
					"exit_code": 0,
				},
			},
		}},
	} {
		updated, _ := model.Update(env)
		model = updated.(*Model)
	}

	got := snapshotTranscriptModel(model)
	want := "Subagent(spawn=child-1,status=running)\n  tool(call-1,BASH,done,args=go test ./tui/tuiapp/...,output=ok)"
	if got != want {
		t.Fatalf("snapshot mismatch\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestProjectGatewayEventACPFetchToolUsesReadableQueryArgs(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(appgateway.Event{
		Kind:   appgateway.EventKindToolCall,
		Origin: &appgateway.EventOrigin{Source: "acp_participant", Scope: appgateway.EventScopeParticipant, ScopeID: "codex-001"},
		ToolCall: &appgateway.ToolCallPayload{
			CallID:    "ws-1",
			ToolTitle: "Searching the Web",
			ToolKind:  "fetch",
			Status:    appgateway.ToolStatusRunning,
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

	events := ProjectGatewayEventToTranscriptEvents(appgateway.Event{
		Kind:   appgateway.EventKindToolCall,
		Origin: &appgateway.EventOrigin{Source: "acp_participant", Scope: appgateway.EventScopeParticipant, ScopeID: "codex-001"},
		ToolCall: &appgateway.ToolCallPayload{
			CallID:    "ws-1",
			ToolTitle: "Searching the Web",
			ToolKind:  "fetch",
			Status:    appgateway.ToolStatusRunning,
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

	events := ProjectGatewayEventToTranscriptEvents(appgateway.Event{
		Kind:   appgateway.EventKindToolResult,
		Origin: &appgateway.EventOrigin{Source: "acp_participant", Scope: appgateway.EventScopeParticipant, ScopeID: "codex-001"},
		ToolResult: &appgateway.ToolResultPayload{
			CallID:    "ws-1",
			ToolTitle: "Searching the Web",
			ToolKind:  "fetch",
			Status:    appgateway.ToolStatusCompleted,
			RawInput: map[string]any{
				"query": "weather: Shanghai, China",
			},
			RawOutput: map[string]any{
				"text": "result 01\nresult 02",
			},
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
			events := ProjectGatewayEventToTranscriptEvents(appgateway.Event{
				Kind:   appgateway.EventKindToolCall,
				Origin: &appgateway.EventOrigin{Source: "acp_participant", Scope: appgateway.EventScopeParticipant, ScopeID: "codex-001"},
				ToolCall: &appgateway.ToolCallPayload{
					CallID:    "call-1",
					ToolTitle: tt.title,
					ToolKind:  tt.kind,
					Status:    appgateway.ToolStatusRunning,
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

func TestProjectGatewayEventTaskArgsUseProtocolRawInput(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(appgateway.Event{
		Kind: appgateway.EventKindToolCall,
		ToolCall: &appgateway.ToolCallPayload{
			CallID:   "task-call",
			ToolName: "TASK",
			Status:   appgateway.ToolStatusRunning,
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

func TestProjectGatewayEventTaskWriteUsesCaelisMetaTarget(t *testing.T) {
	t.Parallel()

	events := ProjectGatewayEventToTranscriptEvents(appgateway.Event{
		Kind: appgateway.EventKindToolResult,
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
		ToolResult: &appgateway.ToolResultPayload{
			CallID:   "task-write",
			ToolName: "TASK",
			ToolKind: "execute",
			Status:   appgateway.ToolStatusCompleted,
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

	events := ProjectGatewayEventToTranscriptEvents(appgateway.Event{
		Kind: appgateway.EventKindAssistantMessage,
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
		Narrative: &appgateway.NarrativePayload{
			Role: appgateway.NarrativeRoleAssistant,
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
		case *SubagentPanelBlock:
			lines = append(lines, "Subagent(spawn="+typed.SpawnID+",status="+typed.Status+")")
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
