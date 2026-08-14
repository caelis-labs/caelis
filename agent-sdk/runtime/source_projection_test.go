package runtime

import (
	"iter"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestLiveContentOwnershipUsesMessageIdentityWithoutInspectingText(t *testing.T) {
	tracker := liveContentOwnership{}
	delta := model.NewTextMessage(model.RoleAssistant, "prefix")
	if tracker.observe(session.MarkUIOnly(&session.Event{
		Type: session.EventTypeAssistant, MessageID: "message-1", Message: &delta,
	})) != 0 {
		t.Fatal("live delta was classified as a canonical materialization")
	}
	final := model.NewTextMessage(model.RoleAssistant, "unrelated complete value")
	if !tracker.observe(&session.Event{
		Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical,
		MessageID: "message-1", Message: &final,
	}).Has(agent.PublishedAssistantMessage) {
		t.Fatal("canonical value with an owned message id was not classified as a live materialization")
	}
}

func TestLiveContentOwnershipSeparatesEqualMessageIDsByScope(t *testing.T) {
	tracker := liveContentOwnership{}
	childA := &session.EventScope{Participant: session.ParticipantRef{ID: "child-a"}}
	childB := &session.EventScope{Participant: session.ParticipantRef{ID: "child-b"}}
	delta := model.NewTextMessage(model.RoleAssistant, "delta")
	tracker.observe(session.MarkUIOnly(&session.Event{
		Type: session.EventTypeAssistant, MessageID: "shared", Message: &delta, Scope: childA,
	}))
	final := model.NewTextMessage(model.RoleAssistant, "final")
	if tracker.observe(&session.Event{
		Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical,
		MessageID: "shared", Message: &final, Scope: childB,
	}) != 0 {
		t.Fatal("one child source claimed another child's canonical content")
	}
	if !tracker.observe(&session.Event{
		Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical,
		MessageID: "shared", Message: &final, Scope: childA,
	}).Has(agent.PublishedAssistantMessage) {
		t.Fatal("matching child source did not claim its canonical materialization")
	}
}

func TestLiveContentOwnershipDoesNotGuessAnonymousNarrativeOwnership(t *testing.T) {
	tracker := liveContentOwnership{}
	delta := model.NewTextMessage(model.RoleAssistant, "delta")
	tracker.observe(session.MarkUIOnly(&session.Event{Type: session.EventTypeAssistant, Message: &delta}))
	final := model.NewTextMessage(model.RoleAssistant, "final")
	if tracker.observe(&session.Event{
		Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical, Message: &final,
	}) != 0 {
		t.Fatal("anonymous narrative was associated without a stable message identity")
	}
}

func TestLiveContentOwnershipAssignsTaskBackedRunCommandToTaskStream(t *testing.T) {
	event := &session.Event{
		Type:       session.EventTypeToolResult,
		Visibility: session.VisibilityCanonical,
		Tool: &session.EventTool{
			Name:   "RUN_COMMAND",
			Status: "completed",
		},
		Meta: map[string]any{"caelis": map[string]any{
			"runtime": map[string]any{
				"task": map[string]any{"task_id": "task-1"},
			},
		}},
	}
	if !(new(liveContentOwnership)).observe(event).Has(agent.PublishedTerminal) {
		t.Fatal("task-backed RunCommand canonical output was not assigned to the Task stream")
	}
}

func TestLiveContentOwnershipAssignsCommandTaskObservationToTaskStream(t *testing.T) {
	event := &session.Event{
		Type:       session.EventTypeToolResult,
		Visibility: session.VisibilityCanonical,
		Tool: &session.EventTool{
			Name:   "TASK",
			Status: "completed",
		},
		Meta: map[string]any{"caelis": map[string]any{
			"runtime": map[string]any{
				"tool": map[string]any{"target_kind": "command"},
				"task": map[string]any{"task_id": "task-1", "output_delta": "ok\n"},
			},
		}},
	}
	if !(new(liveContentOwnership)).observe(event).Has(agent.PublishedTerminal) {
		t.Fatal("command Task observation was not assigned to its Task stream")
	}

	event.Meta["caelis"].(map[string]any)["runtime"].(map[string]any)["tool"] = map[string]any{"target_kind": "subagent"}
	if (new(liveContentOwnership)).observe(event).Has(agent.PublishedTerminal) {
		t.Fatal("subagent Task observation was assigned command terminal ownership")
	}
}

func TestLiveContentOwnershipKeepsUnpublishedAnswerWithSharedThoughtMessageID(t *testing.T) {
	tracker := liveContentOwnership{}
	thought := model.NewReasoningMessage(model.RoleAssistant, "thinking", model.ReasoningVisibilityVisible)
	tracker.observe(session.MarkUIOnly(&session.Event{
		Type: session.EventTypeAssistant, MessageID: "message-1", Message: &thought,
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeAgentThought), MessageID: "message-1",
		}},
	}))
	final := model.NewMessage(
		model.RoleAssistant,
		model.NewReasoningPart("thinking", model.ReasoningVisibilityVisible),
		model.NewTextPart("answer"),
	)
	published := tracker.observe(&session.Event{
		Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical,
		MessageID: "message-1", Message: &final,
	})
	if !published.Has(agent.PublishedAssistantThought) || published.Has(agent.PublishedAssistantMessage) {
		t.Fatalf("published content = %03b, want thought only", published)
	}
}

func TestSourceEventsAdaptsEventsOnlyRunnerWithTerminalOwnership(t *testing.T) {
	event := &session.Event{
		Type: session.EventTypeToolResult, Visibility: session.VisibilityCanonical,
		Tool: &session.EventTool{Name: "RUN_COMMAND", Status: "completed"},
		Meta: map[string]any{"caelis": map[string]any{"runtime": map[string]any{
			"task": map[string]any{"task_id": "task-1"},
		}}},
	}
	var got []agent.SourceEvent
	for sourceEvent, err := range SourceEvents(eventsOnlyRunner{events: []*session.Event{event}}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, sourceEvent)
	}
	if len(got) != 1 || !got[0].CanonicalContentAlreadyPublished.Has(agent.PublishedTerminal) {
		t.Fatalf("SourceEvents() = %#v, want terminal ownership", got)
	}
}

type eventsOnlyRunner struct {
	events []*session.Event
}

func (eventsOnlyRunner) RunID() string { return "legacy-run" }

func (r eventsOnlyRunner) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for _, event := range r.events {
			if !yield(event, nil) {
				return
			}
		}
	}
}

func (eventsOnlyRunner) Submit(agent.Submission) error { return nil }
func (eventsOnlyRunner) Cancel() agent.CancelResult    { return agent.CancelResult{} }
func (eventsOnlyRunner) Close() error                  { return nil }
