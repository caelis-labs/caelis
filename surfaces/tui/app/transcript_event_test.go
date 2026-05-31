package tuiapp

import (
	"strings"
	"testing"

	coremodel "github.com/OnslaughtSnail/caelis/core/model"
	coresession "github.com/OnslaughtSnail/caelis/core/session"
	coretool "github.com/OnslaughtSnail/caelis/core/tool"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

func TestProjectCoreSessionEventToTranscriptEvents_AssistantReasoningAndText(t *testing.T) {
	t.Parallel()

	events := ProjectCoreSessionEventToTranscriptEvents(coresession.Event{
		ID:        "core-assistant",
		SessionID: "root-session",
		Type:      coresession.EventAssistant,
		Actor:     coresession.ActorRef{Kind: coresession.ActorController, Name: "local"},
		Message: &coremodel.Message{
			Role: coremodel.RoleAssistant,
			Parts: []coremodel.Part{
				coremodel.NewReasoningPart("think through it", coremodel.ReasoningVisible),
				coremodel.NewTextPart("final answer"),
			},
		},
	})

	if len(events) != 2 {
		t.Fatalf("events = %#v, want reasoning and answer transcript events", events)
	}
	if events[0].NarrativeKind != TranscriptNarrativeReasoning || events[0].Text != "think through it" || !events[0].Final {
		t.Fatalf("reasoning event = %#v, want final visible reasoning", events[0])
	}
	if events[1].NarrativeKind != TranscriptNarrativeAssistant || events[1].Text != "final answer" || events[1].Actor != "local" {
		t.Fatalf("assistant event = %#v, want final answer from core session event", events[1])
	}
}

func TestProjectCoreSessionEventToTranscriptEvents_ToolResultUsesCoreContent(t *testing.T) {
	t.Parallel()

	events := ProjectCoreSessionEventToTranscriptEvents(coresession.Event{
		ID:        "core-tool",
		SessionID: "root-session",
		Type:      coresession.EventToolResult,
		Tool: &coresession.ToolEvent{
			ID:     "call-1",
			Name:   "RUN_COMMAND",
			Kind:   "execute",
			Status: coresession.ToolCompleted,
			Input:  map[string]any{"command": "printf ok"},
			Content: []coresession.ToolContent{{
				Type: "text",
				Text: "ok\n",
			}},
		},
	})

	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool transcript event", events)
	}
	got := events[0]
	if got.ToolCallID != "call-1" || got.ToolName != "RUN_COMMAND" || got.ToolOutput != "ok\n" || got.ToolStatus != "completed" {
		t.Fatalf("tool transcript = %#v, want core tool content projected directly", got)
	}
}

func TestProjectCoreSessionEventToTranscriptEvents_ProjectsTaskActions(t *testing.T) {
	t.Parallel()

	events := ProjectCoreSessionEventToTranscriptEvents(coresession.Event{
		ID:        "core-tool-task",
		SessionID: "root-session",
		Type:      coresession.EventToolResult,
		Tool: &coresession.ToolEvent{
			ID:     "call-1",
			Name:   "RUN_COMMAND",
			Kind:   "execute",
			Status: coresession.ToolRunning,
			Input:  map[string]any{"command": "npm run dev"},
			Meta: coretool.WithRuntimeTaskMeta(nil, map[string]any{
				"task_id":        "task-dev",
				"state":          "running",
				"supports_input": true,
			}),
		},
	})

	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool transcript event", events)
	}
	actions := events[0].ToolActions
	if !hasTranscriptToolAction(actions, "task.tail:task-dev", "/task tail task-dev") ||
		!hasTranscriptToolAction(actions, "task.write:task-dev", "/task write task-dev -- ") ||
		!hasTranscriptToolAction(actions, "task.cancel:task-dev", "/task cancel task-dev") {
		t.Fatalf("actions = %#v, want task tail/write/cancel descriptors", actions)
	}
}

func TestProjectCoreSessionEventToTranscriptEvents_MergesToolRuntimeMeta(t *testing.T) {
	t.Parallel()

	events := ProjectCoreSessionEventToTranscriptEvents(coresession.Event{
		ID:        "core-tool-meta",
		SessionID: "root-session",
		Type:      coresession.EventToolResult,
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
		Tool: &coresession.ToolEvent{
			ID:     "task-1",
			Name:   "TASK",
			Status: coresession.ToolCompleted,
			Meta: map[string]any{
				"caelis": map[string]any{
					"runtime": map[string]any{
						"tool": map[string]any{
							"target_id":   "maya",
							"target_kind": "subagent",
							"action":      "write",
							"input":       "continue",
						},
					},
				},
			},
		},
	})

	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool transcript event", events)
	}
	got := events[0]
	if got.AnchorToolCallID != "spawn-1" || got.AnchorToolName != "SPAWN" {
		t.Fatalf("anchor = (%q, %q), want stream parent from event meta", got.AnchorToolCallID, got.AnchorToolName)
	}
	if got.ToolTaskID != "maya" || got.ToolTaskAction != "write" || got.ToolTaskTargetKind != "subagent" || got.ToolTaskInput != "continue" {
		t.Fatalf("task fields = id:%q action:%q target:%q input:%q, want tool runtime meta fields", got.ToolTaskID, got.ToolTaskAction, got.ToolTaskTargetKind, got.ToolTaskInput)
	}
}

func hasTranscriptToolAction(actions []appviewmodel.TranscriptAction, id string, command string) bool {
	for _, action := range actions {
		if action.ID == id && action.Command == command && action.Enabled {
			return true
		}
	}
	return false
}

func TestProjectCoreSessionEventToTranscriptEvents_ProjectsAutoReviewApproval(t *testing.T) {
	t.Parallel()

	events := ProjectCoreSessionEventToTranscriptEvents(coresession.Event{
		ID:        "approval-1",
		SessionID: "root-session",
		Type:      coresession.EventApproval,
		Tool: &coresession.ToolEvent{
			ID:    "call-1",
			Name:  "RUN_COMMAND",
			Input: map[string]any{"command": "rm -rf tmp"},
		},
		Approval: &coresession.ApprovalEvent{
			ID:     "approval-call-1",
			Status: coresession.ApprovalRejected,
			Reason: "not narrow enough",
		},
		Meta: map[string]any{
			"usage_category": "auto_review",
			"approval_review": map[string]any{
				"outcome":            "deny",
				"risk_level":         "high",
				"user_authorization": "low",
				"rationale":          "not narrow enough",
			},
		},
	})

	if len(events) != 1 {
		t.Fatalf("events = %#v, want one approval review transcript event", events)
	}
	got := events[0]
	if got.Kind != TranscriptEventApproval || got.ApprovalStatus != "denied" || got.ApprovalRisk != "high" || got.ApprovalAuth != "low" {
		t.Fatalf("approval transcript = %#v, want denied high/low auto-review event", got)
	}
	if got.ToolCallID != "call-1" || got.ApprovalTool != "RUN_COMMAND" || !strings.Contains(got.ApprovalCommand, "rm -rf tmp") {
		t.Fatalf("approval tool = %#v, want tool call details", got)
	}
	if !strings.Contains(got.ApprovalText, "Automatic approval review denied") || !strings.Contains(got.ApprovalText, "not narrow enough") {
		t.Fatalf("approval text = %q, want compact auto-review text with rationale", got.ApprovalText)
	}
}

func TestProjectCoreSessionEventToTranscriptEvents_IgnoresTaskLifecycle(t *testing.T) {
	t.Parallel()

	events := ProjectCoreSessionEventToTranscriptEvents(coresession.Event{
		ID:        "task-lifecycle",
		SessionID: "root-session",
		Type:      coresession.EventLifecycle,
		Lifecycle: &coresession.LifecycleEvent{Status: coresession.LifecycleRunning, Reason: "task start task-1 running"},
		Meta: coretool.WithRuntimeTaskMeta(nil, map[string]any{
			"task_id": "task-1",
			"action":  "start",
			"state":   "running",
		}),
	})
	if len(events) != 0 {
		t.Fatalf("events = %#v, want task lifecycle kept out of transcript rendering", events)
	}
}

func TestProjectCoreSessionEventToTranscriptEvents_IgnoresControllerLifecycle(t *testing.T) {
	t.Parallel()

	events := ProjectCoreSessionEventToTranscriptEvents(coresession.Event{
		ID:        "controller-lifecycle",
		SessionID: "root-session",
		Type:      coresession.EventLifecycle,
		Lifecycle: &coresession.LifecycleEvent{Status: coresession.LifecycleRunning, Reason: "controller started"},
		Meta: coresession.WithRuntimeControllerMeta(nil, map[string]any{
			"run_id":        "turn-controller",
			"phase":         "started",
			"controller_id": "reviewer",
		}),
	})
	if len(events) != 0 {
		t.Fatalf("events = %#v, want controller lifecycle kept out of transcript rendering", events)
	}
}

func TestResumeSessionEventReplayTranscriptEventsUsesCoreEvents(t *testing.T) {
	t.Parallel()

	env := appviewmodel.EventEnvelopeFromSession("cursor-1", coresession.Event{
		ID:        "participant-user",
		SessionID: "root-session",
		Type:      coresession.EventUser,
		Scope: &coresession.EventScope{
			Participant: coresession.ParticipantBinding{
				ID:    "codex-1",
				Kind:  coresession.ParticipantACP,
				Label: "@codex",
			},
		},
		Message: &coremodel.Message{
			Role:  coremodel.RoleUser,
			Parts: []coremodel.Part{coremodel.NewTextPart("review this change")},
		},
	})
	events := resumeSessionEventReplayTranscriptEvents([]appviewmodel.SessionEventEnvelope{env})

	if len(events) != 1 {
		t.Fatalf("events = %#v, want one replay transcript event", events)
	}
	if events[0].Scope != ACPProjectionMain || events[0].Text != "User to @codex: review this change" {
		t.Fatalf("resume transcript = %#v, want participant prompt restored in main transcript", events[0])
	}
}
