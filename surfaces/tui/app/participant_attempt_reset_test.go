package tuiapp

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestScopedParticipantAndSubagentAttemptReset(t *testing.T) {
	const sessionID, turnID, scopeID, spawnID = "session-1", "child-turn", "task-1", "spawn-1"
	const earlierID, failedID = "step", " step "
	const retryNotice = "Retrying model request (1/2, retry in 1s)"
	for _, lane := range []struct {
		name   string
		scope  eventstream.Scope
		status string
		anchor bool
	}{
		{name: "participant", scope: eventstream.ScopeParticipant, status: "waiting_approval"},
		{name: "standalone subagent", scope: eventstream.ScopeSubagent, status: "waiting_approval"},
		{name: "anchored Task stream", scope: eventstream.ScopeSubagent, status: "running", anchor: true},
	} {
		t.Run(lane.name, func(t *testing.T) {
			m := NewModel(Config{NoColor: true, NoAnimation: true})
			m.currentSessionID = sessionID
			m.width, m.height, m.ready = 100, 32, true
			envelope := eventstream.Envelope{
				SessionID: sessionID, TurnID: turnID, Scope: lane.scope, ScopeID: scopeID,
				Actor: "helper", OccurredAt: time.Unix(500, 0),
			}
			if lane.anchor {
				m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{
					Kind: eventstream.KindSessionUpdate, SessionID: sessionID, TurnID: "parent-turn", Scope: eventstream.ScopeMain,
					Update: eventstream.ToolCall{
						SessionUpdate: eventstream.UpdateToolCall, ToolCallID: spawnID, Name: stringPtr(surfaceToolSpawn),
						Kind: "execute", Status: eventstream.ToolStatusInProgress,
					},
				})
				m.subagentOutputOverlay = &subagentOutputOverlayState{callID: spawnID, followTail: true}
				m.taskStreamWanted[scopeID], m.taskStreamTokens[scopeID] = true, 1
				m.taskStreamCallIDsByID[scopeID], m.taskStreamIDsByCallID[spawnID] = spawnID, scopeID
				envelope.ParentTool = &eventstream.ParentToolRelation{ToolCallID: spawnID, ToolName: surfaceToolSpawn}
				envelope.Delivery = &eventstream.Delivery{Mode: eventstream.DeliveryTransient}
			}
			apply := func(env eventstream.Envelope) {
				t.Helper()
				if !lane.anchor {
					m = applyACPEnvelopeForTest(t, m, env)
					return
				}
				next, _ := m.handleTaskStreamBatch(taskStreamBatchMsg{
					sessionID: sessionID, taskID: scopeID, token: 1, events: []eventstream.Envelope{env},
				})
				m = next.(*Model)
			}
			update := func(value eventstream.Update) {
				next := envelope
				next.Kind, next.Update = eventstream.KindSessionUpdate, value
				apply(next)
			}
			narrate := func(kind, id, text string) {
				update(eventstream.ContentChunk{SessionUpdate: kind, MessageID: id, Content: eventstream.TextContent{Type: "text", Text: text}})
			}
			block := func() *ParticipantTurnBlock {
				t.Helper()
				if lane.scope == eventstream.ScopeParticipant {
					b := m.findParticipantTurnBlock(turnID)
					if b == nil {
						t.Fatal("participant turn missing")
					}
					return b
				}
				if lane.anchor {
					return requireSubagentOutputViewForTest(t, m, spawnID).block
				}
				return requireSubagentOutputViewForTest(t, m, "task:"+scopeID).block
			}
			render := func() string {
				if lane.anchor {
					next, _ := m.handleSubagentOutputRenderTick(subagentOutputRenderTickMsg{callID: spawnID})
					m = next.(*Model)
					return ansi.Strip(m.renderSubagentOutputOverlay())
				}
				return joinRenderedPlain(block().Render(m.blockRenderContext(100)))
			}

			narrate(eventstream.UpdateAgentThought, earlierID, "Earlier successful thought")
			narrate(eventstream.UpdateAgentMessage, earlierID, "Earlier successful action")
			update(eventstream.ToolCall{
				SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "tool-1", Name: stringPtr("RunCommand"),
				Kind: "execute", Status: eventstream.ToolStatusCompleted, RawInput: map[string]any{"command": "pwd"},
			})
			status := envelope
			status.Kind, status.Lifecycle = eventstream.KindLifecycle, &eventstream.Lifecycle{State: lane.status}
			apply(status)
			narrate(eventstream.UpdateAgentThought, failedID, "Failed thought")
			narrate(eventstream.UpdateAgentMessage, failedID, "## Failed stable prefix\n\nFailed active tail")
			before := render()
			for _, text := range []string{"Earlier successful thought", "Earlier successful action", "pwd", "Failed thought", "Failed stable prefix", "Failed active tail"} {
				if !strings.Contains(before, text) {
					t.Fatalf("before reset missing %q:\n%s", text, before)
				}
			}

			reset := envelope
			reset.Kind = eventstream.KindLifecycle
			reset.Lifecycle = &eventstream.Lifecycle{State: session.LifecycleStatusModelAttemptReset}
			reset.Meta = session.NewModelAttemptResetForMessage(model.AttemptReset{Attempt: 1, MaxRetries: 2, RetryDelayMillis: 1000, Retrying: true}, failedID).Meta
			reset.ProjectionID = "reset-projection"
			apply(reset)
			if got := block().Status; got != lane.status || !block().EndedAt.IsZero() {
				t.Fatalf("after reset status = %q, ended at = %v, want %q with no end time", got, block().EndedAt, lane.status)
			}
			if lane.status == "waiting_approval" {
				approvalRows := 0
				for _, event := range block().Events {
					if event.Kind == SEApproval {
						approvalRows++
					}
				}
				if approvalRows != 1 {
					t.Fatalf("approval rows = %d, want one", approvalRows)
				}
			}
			waiting := render()
			if strings.Count(waiting, retryNotice) != 1 || strings.Contains(waiting, "Failed") {
				t.Fatalf("after reset should show one retry notice and no failed narrative:\n%s", waiting)
			}

			narrate(eventstream.UpdateAgentMessage, "retry-step", "Fresh retry answer")
			if lane.anchor {
				// Repeated Task delivery may not restore a cleared retry notice.
				apply(reset)
				parent := requireMainACPTurnBlockForTest(t, m)
				if len(parent.Events) != 1 || parent.Events[0].CallID != spawnID || parent.Events[0].Done {
					t.Fatalf("parent Spawn changed across child reset: %#v", parent.Events)
				}
			}
			after := render()
			for _, text := range []string{"Earlier successful thought", "Earlier successful action", "pwd", "Fresh retry answer"} {
				if strings.Count(after, text) != 1 {
					t.Fatalf("after retry should show one copy of %q:\n%s", text, after)
				}
			}
			if strings.Contains(after, "Failed") || strings.Contains(after, retryNotice) {
				t.Fatalf("after retry contains stale display:\n%s", after)
			}
			if lane.scope == eventstream.ScopeParticipant && len(m.doc.Blocks()) != 1 {
				t.Fatalf("participant retry created extra document blocks: %#v", m.doc.Blocks())
			}
		})
	}
}

func TestParticipantAttemptResetPreservesFinalNarrativeAndDropsPendingPrefix(t *testing.T) {
	for _, messageID := range []string{"", " attempt "} {
		t.Run("id="+messageID, func(t *testing.T) {
			block := NewParticipantTurnBlock("turn-1", "@helper")
			block.ReplaceFinalStreamEvent(SEAssistant, "Successful final", newNarrativeSourceIdentity("earlier", "", ""))
			block.UpdateTool("tool-1", "RunCommand", "pwd", "", true, false)
			source := newNarrativeSourceIdentity(messageID, "", "")
			block.AppendStreamEvent(SEReasoning, "Failed thought", source)
			block.AppendStreamEvent(SEAssistant, "    ", source)

			reset := session.NewModelAttemptResetForMessage(model.AttemptReset{}, messageID)
			applyTranscriptEventToParticipantTurn(block, TranscriptEvent{
				Kind: TranscriptEventLifecycle, State: reset.Lifecycle.Status, Meta: reset.Meta,
			}, participantTurnTranscriptPolicy{})
			block.AppendStreamEvent(SEAssistant, "Fresh answer", source)

			if len(block.Events) != 3 || block.Events[0].Text != "Successful final" ||
				block.Events[1].CallID != "tool-1" || !block.Events[1].Done || block.Events[2].Text != "Fresh answer" {
				t.Fatalf("reset should preserve final and tool and remove failed thought and prefix: %#v", block.Events)
			}
		})
	}
}
