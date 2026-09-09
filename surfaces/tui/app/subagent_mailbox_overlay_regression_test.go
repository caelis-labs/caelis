package tuiapp

import (
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestRegressionSubagentOverlayRendersParentMailboxAsAgentCommunication(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.currentSessionID = "session-1"
	model.taskStreamWanted["task-1"] = true
	model.taskStreamTokens["task-1"] = 7
	model.taskStreamCallIDsByID["task-1"] = "spawn-1"
	model.taskStreamIDsByCallID["spawn-1"] = "task-1"
	view := model.ensureSubagentOutputView("spawn-1")
	view.taskHandle = "zuri"
	view.actor = "zuri[breeze]"

	next, _ := model.handleTaskStreamBatch(taskStreamBatchMsg{
		sessionID: "session-1", taskID: "task-1", token: 7,
		events: []eventstream.Envelope{
			subagentMailboxParentInput(t, "task-1:2", time.Unix(120, 0), "continue from parent"),
		},
	})
	model = next.(*Model)
	view.prepareVisibleRender()
	plain := strings.Join(renderedPlainRows(model.subagentOutputRows(view, 96, 20)), "\n")
	if !strings.Contains(plain, "• parent: continue from parent") {
		t.Fatalf("overlay omitted Agent communication styling:\n%s", plain)
	}
	for _, leaked := range []string{`"id"`, `"from"`, `"to"`, `"message"`, "> continue from parent"} {
		if strings.Contains(plain, leaked) {
			t.Fatalf("overlay leaked mailbox JSON or user-input chrome %q:\n%s", leaked, plain)
		}
	}
	if len(view.block.Events) != 1 || view.block.Events[0].Kind != SEAgentCommunication ||
		view.block.Events[0].Text != "continue from parent" {
		t.Fatalf("overlay events = %#v, want typed Agent communication", view.block.Events)
	}

	jsonBody := `{"id":"mail-1","from":"parent","to":"zuri","message":"continue from parent"}`
	view.resetForReplacement()
	_, _ = model.handleTaskStreamBatch(taskStreamBatchMsg{
		sessionID: "session-1", taskID: "task-1", token: 7,
		events: []eventstream.Envelope{subagentMailboxEnvelope(t, "task-1:2", time.Unix(120, 0), eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateUserMessage, MessageID: "mail-1",
			Content: eventstream.TextContent{Type: "text", Text: jsonBody},
		})},
	})
	if len(view.block.Events) != 1 || view.block.Events[0].Kind != SEUserInput || view.block.Events[0].Text != jsonBody {
		t.Fatalf("untyped JSON became Agent communication: %#v", view.block.Events)
	}
}

func TestRegressionSubagentOverlayRendersCanonicalDisplayInputBatchOnSharedTurn(t *testing.T) {
	t.Parallel()

	startedAt := time.Unix(120, 0)
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.currentSessionID = "session-1"
	model.taskStreamWanted["task-1"] = true
	model.taskStreamTokens["task-1"] = 7
	model.taskStreamCallIDsByID["task-1"] = "spawn-1"
	model.taskStreamIDsByCallID["spawn-1"] = "task-1"
	view := model.ensureSubagentOutputView("spawn-1")
	view.taskHandle = "zuri"
	view.actor = "zuri[breeze]"

	first := subagentMailboxParentInput(t, "activity-2", startedAt, "first")
	first.Update = eventstream.ContentChunk{
		SessionUpdate: eventstream.UpdateUserMessage, MessageID: "mail-1",
		Content: eventstream.TextContent{Type: "text", Text: "first"},
	}
	second := subagentMailboxParentInput(t, "activity-2", startedAt.Add(time.Millisecond), "second")
	second.AgentCommunicationSource = &eventstream.ActorIdentity{Kind: "participant", ID: "sibling", Name: "sibling"}
	second.Actor = "sibling"
	second.Update = eventstream.ContentChunk{
		SessionUpdate: eventstream.UpdateUserMessage, MessageID: "mail-2",
		Content: eventstream.TextContent{Type: "text", Text: "second"},
	}
	next, _ := model.handleTaskStreamBatch(taskStreamBatchMsg{
		sessionID: "session-1", taskID: "task-1", token: 7,
		events: []eventstream.Envelope{
			first, second,
			subagentMailboxEnvelope(t, "activity-2", startedAt.Add(8*time.Second), eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "final-2",
				Content: eventstream.TextContent{Type: "text", Text: "batch complete"},
			}),
			subagentMailboxLifecycle(t, "activity-2", startedAt.Add(8*time.Second), eventstream.LifecycleStateCompleted),
		},
	})
	model = next.(*Model)
	view.prepareVisibleRender()
	plain := strings.Join(renderedPlainRows(model.subagentOutputRows(view, 96, 24)), "\n")
	parentAt := strings.Index(plain, "• parent: first")
	siblingAt := strings.Index(plain, "• sibling: second")
	if parentAt < 0 || siblingAt <= parentAt || !strings.Contains(plain, "batch complete") {
		t.Fatalf("canonical batch overlay lost order or display text:\n%s", plain)
	}
	if strings.Contains(plain, `"id"`) || strings.Contains(plain, `"from"`) || strings.Contains(plain, "mail-1") {
		t.Fatalf("canonical batch overlay leaked model-only JSON:\n%s", plain)
	}
	if len(view.turnBlocks) != 1 {
		t.Fatalf("shared activity Turn groups = %d, want one", len(view.turnBlocks))
	}
}

func TestRegressionSubagentOverlayMailboxTurnGetsOwnElapsedDivider(t *testing.T) {
	t.Parallel()

	startedAt := time.Unix(100, 0)
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.currentSessionID = "session-1"
	model.taskStreamWanted["task-1"] = true
	model.taskStreamTokens["task-1"] = 7
	model.taskStreamCallIDsByID["task-1"] = "spawn-1"
	model.taskStreamIDsByCallID["spawn-1"] = "task-1"
	view := model.ensureSubagentOutputView("spawn-1")
	view.taskHandle = "zuri"
	view.actor = "zuri[breeze]"

	next, _ := model.handleTaskStreamBatch(taskStreamBatchMsg{
		sessionID: "session-1", taskID: "task-1", token: 7,
		events: []eventstream.Envelope{
			subagentMailboxEnvelope(t, "task-1:1", startedAt, eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentThought, MessageID: "thought-1",
				Content: eventstream.TextContent{Type: "text", Text: "first turn reasoning"},
			}),
			subagentMailboxEnvelope(t, "task-1:1", startedAt.Add(3*time.Second), eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "final-1",
				Content: eventstream.TextContent{Type: "text", Text: "first turn complete"},
			}),
			subagentMailboxLifecycle(t, "task-1:1", startedAt.Add(4*time.Second), eventstream.LifecycleStateCompleted),
			subagentMailboxParentInput(t, "task-1:2", startedAt.Add(10*time.Second), "check the follow-up"),
			subagentMailboxEnvelope(t, "task-1:2", startedAt.Add(10*time.Second), eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentThought, MessageID: "thought-2",
				Content: eventstream.TextContent{Type: "text", Text: "second turn reasoning"},
			}),
			subagentMailboxEnvelope(t, "task-1:2", startedAt.Add(17*time.Second), eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "final-2",
				Content: eventstream.TextContent{Type: "text", Text: "second turn complete"},
			}),
			subagentMailboxLifecycle(t, "task-1:2", startedAt.Add(18*time.Second), eventstream.LifecycleStateCompleted),
		},
	})
	model = next.(*Model)
	view.prepareVisibleRender()
	plain := strings.Join(renderedPlainRows(model.subagentOutputRows(view, 96, 40)), "\n")
	for _, want := range []string{
		"first turn reasoning",
		"first turn complete",
		"• parent: check the follow-up",
		"second turn reasoning",
		"second turn complete",
		"4.0s",
		"8.0s",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("mailbox Turn overlay omitted %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, `"id"`) || strings.Contains(plain, `"from"`) {
		t.Fatalf("mailbox Turn overlay leaked raw JSON:\n%s", plain)
	}
	firstFooter := strings.Index(plain, "4.0s")
	parentMessage := strings.Index(plain, "• parent: check the follow-up")
	secondFooter := strings.Index(plain, "8.0s")
	if firstFooter < 0 || parentMessage <= firstFooter || secondFooter <= parentMessage {
		t.Fatalf("mailbox follow-up stayed inside the completed Turn divider:\n%s", plain)
	}
	if len(view.turnBlocks) != 2 {
		t.Fatalf("internal Turn groups = %d, want two", len(view.turnBlocks))
	}
}

func TestRegressionSubagentOverlayRunningFollowUpElapsedIncreases(t *testing.T) {
	t.Parallel()

	now := time.Now()
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.currentSessionID = "session-1"
	model.taskStreamWanted["task-1"] = true
	model.taskStreamTokens["task-1"] = 7
	model.taskStreamCallIDsByID["task-1"] = "spawn-1"
	model.taskStreamIDsByCallID["spawn-1"] = "task-1"
	view := model.ensureSubagentOutputView("spawn-1")
	view.taskHandle = "zuri"
	view.actor = "zuri[breeze]"

	next, _ := model.handleTaskStreamBatch(taskStreamBatchMsg{
		sessionID: "session-1", taskID: "task-1", token: 7,
		events: []eventstream.Envelope{
			subagentMailboxEnvelope(t, "activity-1", now.Add(-20*time.Second), eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "final-1",
				Content: eventstream.TextContent{Type: "text", Text: "first turn complete"},
			}),
			subagentMailboxLifecycle(t, "activity-1", now.Add(-16*time.Second), eventstream.LifecycleStateCompleted),
			subagentMailboxParentInput(t, "activity-2", now.Add(-5*time.Second), "check the follow-up"),
			subagentMailboxEnvelope(t, "activity-2", now.Add(-4*time.Second), eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "live-2",
				Content: eventstream.TextContent{Type: "text", Text: "second turn running"},
			}),
		},
	})
	model = next.(*Model)
	view.prepareVisibleRender()
	plain := strings.Join(renderedPlainRows(model.subagentOutputRows(view, 96, 40)), "\n")
	if !strings.Contains(plain, "4.0s") || strings.Contains(plain, "16.0s") ||
		strings.Index(plain, "4.0s") >= strings.Index(plain, "• parent: check the follow-up") {
		t.Fatalf("running follow-up stayed inside the completed Turn divider:\n%s", plain)
	}
	if participantTurnIsTerminal(view.block.Status) || !view.block.EndedAt.IsZero() {
		t.Fatalf("running follow-up block settled early: %#v", view.block)
	}
	ctx := model.blockRenderContext(96)
	ctx.Now = now
	view.block.StartedAt = now.Add(-2 * time.Second)
	early := strings.Join(renderedPlainRows(view.block.Render(ctx)), "\n")
	view.block.StartedAt = now.Add(-8 * time.Second)
	later := strings.Join(renderedPlainRows(view.block.Render(ctx)), "\n")
	if !strings.Contains(early, "2.0s") || strings.Contains(early, "8.0s") ||
		!strings.Contains(later, "8.0s") || strings.Contains(later, "2.0s") {
		t.Fatalf("running elapsed did not increase:\n early=%q\n later=%q", early, later)
	}
}

func TestRegressionSubagentOverlayRecordLocalActivityTurnsStayDistinct(t *testing.T) {
	t.Parallel()

	startedAt := time.Unix(100, 0)
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.currentSessionID = "session-1"
	model.taskStreamWanted["task-1"] = true
	model.taskStreamTokens["task-1"] = 7
	model.taskStreamCallIDsByID["task-1"] = "spawn-1"
	model.taskStreamIDsByCallID["spawn-1"] = "task-1"
	view := model.ensureSubagentOutputView("spawn-1")
	view.taskHandle = "zuri"
	view.actor = "zuri[breeze]"

	apply := func() {
		t.Helper()
		next, _ := model.handleTaskStreamBatch(taskStreamBatchMsg{
			sessionID: "session-1", taskID: "task-1", token: 7, replacement: true,
			events: []eventstream.Envelope{
				subagentMailboxEnvelope(t, "activity-1", startedAt, eventstream.ContentChunk{
					SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "final-1",
					Content: eventstream.TextContent{Type: "text", Text: "first turn complete"},
				}),
				subagentMailboxLifecycle(t, "activity-1", startedAt.Add(4*time.Second), eventstream.LifecycleStateCompleted),
				subagentMailboxParentInput(t, "activity-2", startedAt.Add(10*time.Second), "check the follow-up"),
				subagentMailboxEnvelope(t, "activity-2", startedAt.Add(17*time.Second), eventstream.ContentChunk{
					SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "final-2",
					Content: eventstream.TextContent{Type: "text", Text: "second turn complete"},
				}),
				subagentMailboxLifecycle(t, "activity-2", startedAt.Add(18*time.Second), eventstream.LifecycleStateCompleted),
			},
		})
		model = next.(*Model)
	}
	apply()
	apply()
	view.prepareVisibleRender()
	plain := strings.Join(renderedPlainRows(model.subagentOutputRows(view, 96, 40)), "\n")
	if !strings.Contains(plain, "• parent: check the follow-up") || strings.Count(plain, "4.0s") != 1 ||
		strings.Count(plain, "8.0s") != 1 || len(view.turnBlocks) != 2 {
		t.Fatalf("replayed record-local Turns collapsed:\n%s", plain)
	}
}

func subagentMailboxParentInput(t *testing.T, turnID string, at time.Time, text string) eventstream.Envelope {
	t.Helper()
	env := subagentMailboxEnvelope(t, turnID, at, eventstream.ContentChunk{
		SessionUpdate: eventstream.UpdateUserMessage, MessageID: "mail-1",
		Content: eventstream.TextContent{Type: "text", Text: text},
	})
	env.Actor = "parent"
	env.AgentCommunicationSource = &eventstream.ActorIdentity{Kind: "controller", ID: "parent", Name: "parent"}
	return env
}

func subagentMailboxEnvelope(t *testing.T, turnID string, at time.Time, update eventstream.Update) eventstream.Envelope {
	t.Helper()
	return eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: turnID,
		Scope: eventstream.ScopeSubagent, ScopeID: "task-1", OccurredAt: at,
		ParentTool: &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "StartThread"},
		Update:     update,
	}
}

func subagentMailboxLifecycle(t *testing.T, turnID string, at time.Time, state string) eventstream.Envelope {
	t.Helper()
	return eventstream.Envelope{
		Kind: eventstream.KindLifecycle, SessionID: "session-1", TurnID: turnID,
		Scope: eventstream.ScopeSubagent, ScopeID: "task-1", OccurredAt: at,
		ParentTool: &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "StartThread"},
		Lifecycle:  &eventstream.Lifecycle{State: state},
	}
}
