package tuiapp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestApprovalPayloadFromACPEventRestoresPromptFields(t *testing.T) {
	status := schema.ToolStatusPending
	kind := "RUN_COMMAND"
	req := approvalPayloadFromACPEvent(eventstream.Envelope{
		Kind: eventstream.KindRequestPermission,
		Permission: &schema.RequestPermissionRequest{
			SessionID: "session-1",
			ToolCall: schema.ToolCallUpdate{
				SessionUpdate: schema.UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Kind:          &kind,
				Status:        &status,
				RawInput: map[string]any{
					"command":             "go test ./...",
					"approval_reason":     "needs execution",
					"justification":       "requested by user",
					"sandbox_permissions": "host",
				},
			},
			Options: []schema.PermissionOption{{
				OptionID: "allow_once",
				Name:     "Allow once",
				Kind:     "allow_once",
			}},
		},
	})
	if req == nil {
		t.Fatal("approvalPayloadFromACPEvent() = nil, want payload")
		return
	}
	if req.ToolCallID != "call-1" || req.ToolName != "RUN_COMMAND" {
		t.Fatalf("tool = (%q, %q), want call-1 RUN_COMMAND", req.ToolCallID, req.ToolName)
	}
	if req.Reason != "needs execution" || req.Justification != "requested by user" || req.SandboxPermissions != "host" {
		t.Fatalf("prompt fields = (%q, %q, %q), want restored fields", req.Reason, req.Justification, req.SandboxPermissions)
	}
	if len(req.Options) != 1 || req.Options[0].ID != "allow_once" {
		t.Fatalf("options = %#v, want allow_once", req.Options)
	}
}

func TestForwardTurnEventStreamShowsSubagentPermissionPrompt(t *testing.T) {
	status := schema.ToolStatusPending
	kind := "RUN_COMMAND"
	events := make(chan eventstream.Envelope, 1)
	events <- eventstream.Envelope{
		Kind:    eventstream.KindRequestPermission,
		Scope:   eventstream.ScopeSubagent,
		ScopeID: "task-cora",
		Actor:   "cora",
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
		Permission: &schema.RequestPermissionRequest{
			SessionID: "child-session",
			ToolCall: schema.ToolCallUpdate{
				SessionUpdate: schema.UpdateToolCallInfo,
				ToolCallID:    "perm-1",
				Kind:          &kind,
				Status:        &status,
				RawInput: map[string]any{
					"command":             "git status",
					"approval_reason":     "VCS check requested by user",
					"sandbox_permissions": "require_escalated",
				},
			},
			Options: []schema.PermissionOption{{
				OptionID: "allow_once",
				Name:     "Allow once",
				Kind:     "allow_once",
			}},
		},
	}
	close(events)
	turn := &eventStreamApprovalTurn{
		events:    events,
		decisions: make(chan control.ApprovalDecision, 1),
	}
	sent := make(chan any, 4)
	sender := &ProgramSender{Send: func(msg tea.Msg) {
		sent <- msg
	}}

	forwardTurnEventStream(context.Background(), nil, turn, sender)

	var prompt PromptRequestMsg
	found := false
	for len(sent) > 0 {
		msg := <-sent
		next, ok := msg.(PromptRequestMsg)
		if !ok {
			continue
		}
		prompt = next
		found = true
		break
	}
	if !found {
		t.Fatal("forwardTurnEventStream() did not send PromptRequestMsg for subagent request_permission")
	}
	if prompt.Title != "Approval Required" || prompt.Prompt != "Ran" {
		t.Fatalf("prompt = (%q, %q), want approval modal for RUN_COMMAND", prompt.Title, prompt.Prompt)
	}
	if !hasPromptDetail(prompt.Details, PromptDetail{Label: "Command", Value: "command: git status", Emphasis: true}) {
		t.Fatalf("prompt details = %#v, want command detail", prompt.Details)
	}
	if !hasPromptDetail(prompt.Details, PromptDetail{Label: "Sandbox", Value: "require_escalated"}) {
		t.Fatalf("prompt details = %#v, want sandbox detail", prompt.Details)
	}
	prompt.Response <- PromptResponse{Line: "allow_once"}
	select {
	case decision := <-turn.decisions:
		if decision.OptionID != "allow_once" || !decision.Approved {
			t.Fatalf("approval decision = %#v, want allow_once approved", decision)
		}
	case <-time.After(time.Second):
		t.Fatal("approval decision was not submitted back to the turn")
	}
}

func TestForwardTurnEventStreamCloseWithoutTerminalQueuesFallbackLifecycle(t *testing.T) {
	events := make(chan eventstream.Envelope)
	close(events)
	turn := &rawEventStreamTurn{events: events}
	var msgs []tea.Msg

	result := forwardTurnEventStream(context.Background(), nil, turn, &ProgramSender{Send: func(msg tea.Msg) {
		msgs = append(msgs, msg)
	}})

	if !result.queued || result.completion != (TaskResultMsg{}) {
		t.Fatalf("forwardTurnEventStream() result = %#v, want queued fallback lifecycle", result)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %#v, want fallback lifecycle only", msgs)
	}
	requireTerminalLifecycle(t, msgs[0], eventstream.LifecycleStateCompleted)
}

func TestForwardTurnEventStreamTerminalLifecycleReturnsBeforeChannelClose(t *testing.T) {
	events := make(chan eventstream.Envelope, 1)
	events <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Now())
	turn := &rawEventStreamTurn{events: events}
	var msgs []tea.Msg

	resultCh := make(chan executeLineResult, 1)
	go func() {
		resultCh <- forwardTurnEventStream(context.Background(), nil, turn, &ProgramSender{Send: func(msg tea.Msg) {
			msgs = append(msgs, msg)
		}})
	}()

	select {
	case result := <-resultCh:
		close(events)
		if !result.queued || result.completion != (TaskResultMsg{}) {
			t.Fatalf("forwardTurnEventStream() result = %#v, want queued terminal lifecycle", result)
		}
	case <-time.After(500 * time.Millisecond):
		close(events)
		t.Fatal("forwardTurnEventStream() waited for events channel close after terminal lifecycle")
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %#v, want terminal lifecycle only", msgs)
	}
	requireTerminalLifecycle(t, msgs[0], eventstream.LifecycleStateCompleted)
}

func TestForwardTurnEventStreamErrorCloseWithoutTerminalQueuesFailedLifecycle(t *testing.T) {
	events := make(chan eventstream.Envelope, 1)
	events <- eventstream.Error(errors.New("provider failed"))
	close(events)
	turn := &rawEventStreamTurn{events: events}
	var msgs []tea.Msg

	result := forwardTurnEventStream(context.Background(), nil, turn, &ProgramSender{Send: func(msg tea.Msg) {
		msgs = append(msgs, msg)
	}})

	if !result.queued || result.completion != (TaskResultMsg{}) {
		t.Fatalf("forwardTurnEventStream() result = %#v, want queued failure lifecycle", result)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %#v, want error event plus failure lifecycle", msgs)
	}
	failed := requireTerminalLifecycle(t, msgs[1], eventstream.LifecycleStateFailed)
	if failed.Lifecycle == nil || failed.Lifecycle.Reason != "provider failed" {
		t.Fatalf("lifecycle = %#v, want provider failure reason", failed.Lifecycle)
	}
}

func TestSubagentAutomaticApprovalReviewShowsHint(t *testing.T) {
	model := newGatewayEventTestModel()
	updated, _ := model.Update(eventstream.Envelope{
		Kind:    eventstream.KindApprovalReview,
		Scope:   eventstream.ScopeSubagent,
		ScopeID: "task-cora",
		Actor:   "cora",
		ApprovalReview: &eventstream.ApprovalReview{
			ToolCallID: "perm-1",
			ToolName:   "RUN_COMMAND",
			RawInput: map[string]any{
				"command": "git status",
			},
			Status: "in_progress",
		},
	})
	model = updated.(*Model)
	if !strings.Contains(model.approvalReviewHint, "command: git status") {
		t.Fatalf("approvalReviewHint = %q, want subagent command hint", model.approvalReviewHint)
	}
}

type eventStreamApprovalTurn struct {
	events    <-chan eventstream.Envelope
	decisions chan control.ApprovalDecision
}

func (t *eventStreamApprovalTurn) HandleID() string { return "handle-1" }
func (t *eventStreamApprovalTurn) RunID() string    { return "run-1" }
func (t *eventStreamApprovalTurn) TurnID() string   { return "turn-1" }
func (t *eventStreamApprovalTurn) Events() <-chan eventstream.Envelope {
	return eventstream.EnsureTerminalLifecycle(t.events, t.HandleID(), t.RunID(), t.TurnID())
}
func (t *eventStreamApprovalTurn) SubmitApproval(_ context.Context, decision control.ApprovalDecision) error {
	t.decisions <- decision
	return nil
}
func (t *eventStreamApprovalTurn) Cancel()      {}
func (t *eventStreamApprovalTurn) Close() error { return nil }

type rawEventStreamTurn struct {
	events <-chan eventstream.Envelope
}

func (t *rawEventStreamTurn) HandleID() string { return "handle-1" }
func (t *rawEventStreamTurn) RunID() string    { return "run-1" }
func (t *rawEventStreamTurn) TurnID() string   { return "turn-1" }
func (t *rawEventStreamTurn) Events() <-chan eventstream.Envelope {
	return t.events
}
func (t *rawEventStreamTurn) SubmitApproval(context.Context, control.ApprovalDecision) error {
	return nil
}
func (t *rawEventStreamTurn) Cancel()      {}
func (t *rawEventStreamTurn) Close() error { return nil }
