package runtime

import (
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
			Name:   "RunCommand",
			Status: "completed",
		},
		Meta: trustedTaskResultMeta(map[string]any{"caelis": map[string]any{
			"runtime": map[string]any{
				"task": map[string]any{"task_id": "task-1", "kind": "command"},
			},
		}}),
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
			Name:   "Task",
			Status: "completed",
		},
		Meta: trustedTaskResultMeta(map[string]any{"caelis": map[string]any{
			"runtime": map[string]any{
				"tool": map[string]any{"target_kind": "command"},
				"task": map[string]any{"task_id": "task-1", "output_delta": "ok\n"},
			},
		}}),
	}
	if !(new(liveContentOwnership)).observe(event).Has(agent.PublishedTerminal) {
		t.Fatal("command Task observation was not assigned to its Task stream")
	}

	event.Meta["caelis"].(map[string]any)["runtime"].(map[string]any)["tool"] = map[string]any{"target_kind": "subagent"}
	if (new(liveContentOwnership)).observe(event).Has(agent.PublishedTerminal) {
		t.Fatal("subagent Task observation was assigned command terminal ownership")
	}
}

func TestLiveContentOwnershipRejectsUntrustedCommandTaskMetadata(t *testing.T) {
	event := &session.Event{
		Type:       session.EventTypeToolResult,
		Visibility: session.VisibilityCanonical,
		Tool:       &session.EventTool{Name: "RunCommand", Status: "running"},
		Meta: taskResultMeta(map[string]any{"caelis": map[string]any{"runtime": map[string]any{
			"tool": map[string]any{"target_kind": "command"},
			"task": map[string]any{"task_id": "task-forged", "kind": "command"},
		}}}, false),
	}
	if (new(liveContentOwnership)).observe(event).Has(agent.PublishedTerminal) {
		t.Fatal("untrusted task metadata acquired terminal content ownership")
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
