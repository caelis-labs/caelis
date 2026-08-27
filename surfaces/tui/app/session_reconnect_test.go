package tuiapp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	protocoltaskstream "github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestSessionReconnectMessageInstallsSessionBeforeSubagentBackfill(t *testing.T) {
	t.Parallel()

	descriptors := []protocoltaskstream.TaskDescriptor{
		{
			SessionID: "session-old", TaskID: "task-kira", Handle: "kira", Kind: task.KindSubagent,
			State: task.StateCompleted, Running: false, UpdatedAt: time.Unix(103, 0),
			ParentTool: protocoltaskstream.ParentTool{ToolCallID: "spawn-kira", ToolName: "Spawn"},
		},
		{
			SessionID: "session-old", TaskID: "task-wen", Handle: "wen", Kind: task.KindSubagent,
			State: task.StateCompleted, Running: false, UpdatedAt: time.Unix(102, 0),
			ParentTool: protocoltaskstream.ParentTool{ToolCallID: "spawn-wen", ToolName: "Spawn"},
		},
		{
			SessionID: "session-old", TaskID: "task-yara", Handle: "yara", Kind: task.KindSubagent,
			State: task.StateCompleted, Running: false, UpdatedAt: time.Unix(101, 0),
			ParentTool: protocoltaskstream.ParentTool{ToolCallID: "spawn-yara", ToolName: "Spawn"},
		},
	}
	service := &subagentRosterTestTaskStreamService{
		list: protocoltaskstream.ListResult{Tasks: descriptors},
	}
	model := NewModel(Config{
		NoColor: true, NoAnimation: true,
		TaskStreams: bindTaskStreamTestClient(t, service),
	})

	next, _ := model.Update(SessionReconnectMsg{State: appserver.SessionState{SessionID: "session-old"}})
	model = next.(*Model)
	if model.currentSessionID != "session-old" {
		t.Fatalf("reconnect Session = %q, want session-old", model.currentSessionID)
	}

	events := make([]TranscriptEvent, 0, len(descriptors))
	for _, descriptor := range descriptors {
		events = append(events, TranscriptEvent{
			Kind: TranscriptEventTool, Scope: ACPProjectionMain,
			ToolCallID: descriptor.ParentTool.ToolCallID, ToolName: "Spawn", ToolTaskHandle: descriptor.Handle,
			ToolArgs: descriptor.Handle + "[self]: historical task", OccurredAt: time.Unix(90, 0),
		})
	}
	next, _ = model.Update(TranscriptEventsMsg{Events: events})
	model = next.(*Model)
	applySubagentDirectorySnapshotForTest(model, 1, descriptors)

	if got := len(model.subagentRosterTasks); got != len(descriptors) {
		t.Fatalf("reconnected Task directory rows = %d, want %d", got, len(descriptors))
	}
	if got := model.subagentRosterRunningCount(); got != 0 {
		t.Fatalf("cold reconnected running count = %d, want completed Task directory state", got)
	}
}

func TestColdSessionResumeKeepsCompletedExplorationCollapsed(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	next, _ := model.Update(SessionReconnectMsg{State: appserver.SessionState{SessionID: "session-resume"}})
	model = next.(*Model)

	completed := liveExplorationLifecycleEnvelope(eventstream.LifecycleStateCompleted)
	envelopes := []eventstream.Envelope{
		liveExplorationToolStartEnvelope("read-1", "Read", "read", "a.go"),
		liveExplorationToolCompleteEnvelope("read-1", "Read", "read", "a.go"),
		liveExplorationToolStartEnvelope("search-1", "Grep", "search", "needle"),
		liveExplorationToolCompleteEnvelope("search-1", "Grep", "search", "needle"),
		completed,
	}
	var replay []TranscriptEvent
	for _, envelope := range envelopes {
		envelope.SessionID = "session-resume"
		envelope.ScopeID = "session-resume"
		replay = append(replay, projectResumeReplayEvents([]eventstream.Envelope{envelope})...)
	}
	next, _ = model.Update(TranscriptEventsMsg{Events: replay, ReconnectReplay: true})
	model = next.(*Model)

	block := requireMainACPTurnBlockForTest(t, model)
	plain := joinRenderedPlain(block.Render(model.blockRenderContext(100)))
	if got := countExactTrimmedLine(plain, "• Explored"); got != 1 {
		t.Fatalf("cold resume exploration groups = %d, want one:\n%s", got, plain)
	}
	if headers := standaloneExplorationHeaders(plain); len(headers) != 0 {
		t.Fatalf("cold resume expanded completed exploration as %q:\n%s", headers, plain)
	}
}

func TestColdSessionResumeKeepsActiveCompletedExplorationCollapsed(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	next, _ := model.Update(SessionReconnectMsg{State: appserver.SessionState{
		SessionID: "session-resume",
		Run:       appserver.RunState{Active: true, TurnID: "turn-live-exploration"},
	}})
	model = next.(*Model)

	envelopes := []eventstream.Envelope{
		liveExplorationToolStartEnvelope("read-1", "Read", "read", "a.go"),
		liveExplorationToolCompleteEnvelope("read-1", "Read", "read", "a.go"),
		liveExplorationToolStartEnvelope("search-1", "Grep", "search", "needle"),
		liveExplorationToolCompleteEnvelope("search-1", "Grep", "search", "needle"),
	}
	var replay []TranscriptEvent
	for _, envelope := range envelopes {
		envelope.SessionID = "session-resume"
		envelope.ScopeID = "session-resume"
		replay = append(replay, projectResumeReplayEvents([]eventstream.Envelope{envelope})...)
	}
	next, _ = model.Update(TranscriptEventsMsg{Events: replay, ReconnectReplay: true})
	model = next.(*Model)

	block := requireMainACPTurnBlockForTest(t, model)
	if block.Status != "running" {
		t.Fatalf("resumed active block status = %q, want running", block.Status)
	}
	plain := joinRenderedPlain(block.Render(model.blockRenderContext(100)))
	if got := countExactTrimmedLine(plain, "• Explored"); got != 1 {
		t.Fatalf("active cold resume exploration groups = %d, want one:\n%s", got, plain)
	}
	if headers := standaloneExplorationHeaders(plain); len(headers) != 0 {
		t.Fatalf("active cold resume expanded completed exploration as %q:\n%s", headers, plain)
	}
}

func TestExecuteReconnectTreatsHistoryAsTranscriptAndRestoresApproval(t *testing.T) {
	backfill := make(chan eventstream.Envelope, 1)
	backfill <- eventstream.TurnCompleted("old-handle", "old-run", "old-turn", time.Unix(10, 0))
	close(backfill)
	live := make(chan eventstream.Envelope, 1)
	live <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(20, 0))
	close(live)
	reconnect := &tuiReconnect{
		state: appserver.SessionState{
			SessionID: "session-1", ResumeMode: appserver.ResumeModeExact,
			Run: appserver.RunState{Active: true, HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"},
			Approval: appserver.ApprovalState{Active: &appserver.ActiveApproval{
				RequestID:  "approval-original",
				Permission: &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "call-1", Name: "Bash"}},
			}},
		},
		backfill: backfill,
		live:     live,
		bootstrap: []eventstream.Envelope{{
			Kind: eventstream.KindRequestPermission, SessionID: "session-1",
			HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1",
			ApprovalRequestID: "approval-original",
			Permission: &schema.RequestPermissionRequest{
				SessionID: "session-1", ToolCall: schema.ToolCallUpdate{ToolCallID: "call-1"},
				Options: []schema.PermissionOption{{OptionID: "allow_once", Name: "Allow once", Kind: "allow_once"}},
			},
		}},
	}
	var mu sync.Mutex
	var messages []any
	sender := &ProgramSender{Send: func(message tea.Msg) {
		mu.Lock()
		messages = append(messages, message)
		mu.Unlock()
		if prompt, ok := message.(PromptRequestMsg); ok {
			prompt.Response <- PromptResponse{Line: "allow_once"}
		}
	}}
	result := executeControlPromptResult(context.Background(), nil, sender, controlprompt.Result{
		Handled: true, ClearHistory: true, Reconnect: reconnect, SuppressTurnDivider: true,
	})
	if !result.queued {
		t.Fatalf("execute reconnect result = %#v, want live terminal queued", result)
	}
	deadline := time.Now().Add(time.Second)
	for {
		reconnect.mu.Lock()
		decisions := append([]controlprompt.ApprovalDecision(nil), reconnect.decisions...)
		reconnect.mu.Unlock()
		if len(decisions) > 0 {
			if decisions[0].RequestID != "approval-original" || decisions[0].OptionID != "allow_once" {
				t.Fatalf("approval decisions = %#v", decisions)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for resumed approval submission")
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(messages) == 0 {
		t.Fatal("no reconnect messages were forwarded")
	}
	if _, ok := messages[0].(SessionReconnectMsg); !ok {
		t.Fatalf("first reconnect message = %T, want atomic SessionReconnectMsg", messages[0])
	}
}

func TestForwardSessionReconnectPreservesFeedGapAndDoesNotCompleteTurn(t *testing.T) {
	t.Parallel()

	live := make(chan eventstream.Envelope)
	close(live)
	gap := &appserver.FeedGapError{
		Cause:        appserver.ErrSlowConsumer,
		RetryCursor:  "retry-cursor",
		Mode:         appserver.ResumeModeDurableFallback,
		TransientGap: true,
	}
	reconnect := &tuiReconnect{
		state: appserver.SessionState{
			SessionID: "session-1",
			Run: appserver.RunState{
				Active: true, HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1",
			},
		},
		live: live,
		err:  gap,
	}
	var messages []tea.Msg
	result := forwardSessionReconnectEventStream(context.Background(), reconnect, &ProgramSender{Send: func(message tea.Msg) {
		messages = append(messages, message)
	}})
	if !result.queued || len(messages) != 1 {
		t.Fatalf("result = %#v, messages = %#v, want one queued interrupted terminal", result, messages)
	}
	terminal, ok := messages[0].(eventstream.Envelope)
	if !ok || !eventstream.IsTerminalLifecycle(terminal) || terminal.Lifecycle.State != eventstream.LifecycleStateInterrupted {
		t.Fatalf("terminal = %#v, want interrupted lifecycle", messages[0])
	}
	var gotGap *appserver.FeedGapError
	if !errors.As(terminal.Err, &gotGap) || gotGap.RetryCursor != gap.RetryCursor || gotGap.Mode != gap.Mode || !gotGap.TransientGap {
		t.Fatalf("terminal error = %#v, want typed feed gap with retry cursor", terminal.Err)
	}
}

func TestStreamReconnectBackfillCarriesNormalizedObservedSpawnResult(t *testing.T) {
	t.Parallel()

	events := canonicalOutputFidelityEvents()
	backfill := make(chan eventstream.Envelope, len(events))
	for _, event := range events {
		event = roundTripCanonicalOutputFidelityEvent(t, event)
		base := acpprojector.EnvelopeBaseFromSessionEvent(
			session.SessionRef{SessionID: "session-1"},
			event,
			acpprojector.SessionEventTransport{},
		)
		projected := acpprojector.ProjectSessionEventEnvelope(base, event)
		if len(projected) != 1 {
			t.Fatalf("projection for %s = %#v, want one envelope", event.ID, projected)
		}
		backfill <- projected[0]
	}
	close(backfill)

	var messages []TranscriptEventsMsg
	err := streamReconnectBackfill(
		context.Background(),
		&tuiReconnect{backfill: backfill},
		func(message tea.Msg) {
			transcript, ok := message.(TranscriptEventsMsg)
			if !ok {
				t.Fatalf("backfill message = %T, want TranscriptEventsMsg", message)
			}
			messages = append(messages, transcript)
		},
	)
	if err != nil {
		t.Fatalf("stream reconnect backfill: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("backfill messages = %#v, want one ordered terminal-observation batch", messages)
	}
	message := messages[0]
	if !message.ReconnectReplay {
		t.Fatal("backfill batch is not marked as reconnect replay")
	}
	if len(message.Events) != 2 {
		t.Fatalf("transcript events = %#v, want Spawn call plus hidden Task observation", message.Events)
	}
	if len(message.OwnerRepairs.Spawns) != 1 {
		t.Fatalf("observed Spawn results = %#v, want one normalized terminal child", message.OwnerRepairs.Spawns)
	}
	result := message.OwnerRepairs.Spawns[0]
	if result.ParentCallID != "spawn-call-1" ||
		result.Status != schema.ToolStatusCompleted ||
		result.RawOutput["final_message"] != structuredFinalMessageForFidelityTest {
		t.Fatalf("observed Spawn result = %#v, want exact durable terminal child payload", result)
	}
}

func TestApplySessionReconnectStateAtomicallyResetsTaskStreamSession(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name              string
		reconnectSession  string
		staleBatchSession string
	}{
		{name: "same session", reconnectSession: "session-old", staleBatchSession: "session-old"},
		{name: "different session", reconnectSession: "session-new", staleBatchSession: "session-old"},
	} {
		t.Run(test.name, func(t *testing.T) {
			subscription := newTUIProtocolTaskSubscription()
			model := NewModel(Config{NoColor: true, NoAnimation: true})
			model.currentSessionID = "session-old"
			model.taskStreamWanted["task-old"] = true
			model.taskStreamTokens["task-old"] = 7
			model.taskStreamSubscriptions["task-old"] = subscription
			model.taskStreamCursors["task-old"] = "cursor-old"
			model.taskStreamHandlesByID["task-old"] = "handle-old"
			model.taskStreamIDsByCallID["call-old"] = "task-old"
			model.taskStreamCallIDsByID["task-old"] = "call-old"
			model.taskStreamNextToken = 7
			model.runningHintTracker.observeOwner("handle-old", runningActivityOwner{
				Key: "owner-old", CallID: "call-old", Target: runningTargetSubagent,
			})

			model.applySessionReconnectState(appserver.SessionState{SessionID: test.reconnectSession})

			if model.currentSessionID != test.reconnectSession {
				t.Fatalf("current Session = %q, want %q", model.currentSessionID, test.reconnectSession)
			}
			if _, open := <-subscription.events; open {
				t.Fatal("old Task-stream subscription remains open after reconnect")
			}
			if len(model.taskStreamWanted) != 0 ||
				len(model.taskStreamTokens) != 0 ||
				len(model.taskStreamSubscriptions) != 0 ||
				len(model.taskStreamCursors) != 0 ||
				len(model.taskStreamHandlesByID) != 0 ||
				len(model.taskStreamIDsByCallID) != 0 ||
				len(model.taskStreamCallIDsByID) != 0 {
				t.Fatalf("Task-stream state survived reconnect: wanted=%v tokens=%v subscriptions=%v cursors=%v handles=%v calls=%v taskCalls=%v",
					model.taskStreamWanted,
					model.taskStreamTokens,
					model.taskStreamSubscriptions,
					model.taskStreamCursors,
					model.taskStreamHandlesByID,
					model.taskStreamIDsByCallID,
					model.taskStreamCallIDsByID,
				)
			}
			if model.taskStreamNextToken != 7 {
				t.Fatalf("Task-stream token generation = %d, want monotonic 7", model.taskStreamNextToken)
			}
			if len(model.runningHintTracker.ownersByHandle) != 0 ||
				len(model.runningHintTracker.ownersByCallID) != 0 {
				t.Fatalf("old activity owner index survived reconnect: handles=%v calls=%v",
					model.runningHintTracker.ownersByHandle,
					model.runningHintTracker.ownersByCallID,
				)
			}

			next, _ := model.handleTaskStreamBatch(taskStreamBatchMsg{
				sessionID: test.staleBatchSession,
				taskID:    "task-old",
				token:     7,
				events: []eventstream.Envelope{{
					Kind: eventstream.KindSessionUpdate, SessionID: test.staleBatchSession,
					Update: schema.ContentChunk{SessionUpdate: schema.UpdateAgentMessage},
				}},
			})
			model = next.(*Model)
			if len(model.doc.Blocks()) != 0 {
				t.Fatalf("stale Task-stream batch rendered %d blocks after reconnect", len(model.doc.Blocks()))
			}

			model.runningHintTracker.observeOwner("handle-backfill", runningActivityOwner{
				Key: "owner-backfill", CallID: "call-backfill", Target: runningTargetSubagent,
			})
			model.observeTaskStreamSession(eventstream.Envelope{
				SessionID: test.reconnectSession,
				Scope:     eventstream.ScopeMain,
			})
			if got := model.runningHintTracker.ownersByHandle["handle-backfill"].Key; got != "owner-backfill" {
				t.Fatalf("first live Envelope reset backfill activity owner = %q, want owner-backfill", got)
			}
		})
	}
}

type tuiReconnect struct {
	mu        sync.Mutex
	state     appserver.SessionState
	backfill  <-chan eventstream.Envelope
	live      <-chan eventstream.Envelope
	bootstrap []eventstream.Envelope
	decisions []controlprompt.ApprovalDecision
	err       error
}

func (r *tuiReconnect) State() appserver.SessionState { return r.state }
func (r *tuiReconnect) HandleID() string              { return r.state.Run.HandleID }
func (r *tuiReconnect) RunID() string                 { return r.state.Run.RunID }
func (r *tuiReconnect) TurnID() string                { return r.state.Run.TurnID }
func (r *tuiReconnect) Backfill() <-chan eventstream.Envelope {
	return r.backfill
}
func (r *tuiReconnect) Events() <-chan eventstream.Envelope { return r.live }
func (r *tuiReconnect) BackfillDone() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
func (r *tuiReconnect) BootstrapEvents() []eventstream.Envelope {
	return eventstream.CloneEnvelopes(r.bootstrap)
}
func (r *tuiReconnect) SubmitApproval(_ context.Context, decision controlprompt.ApprovalDecision) error {
	r.mu.Lock()
	r.decisions = append(r.decisions, decision)
	r.mu.Unlock()
	return nil
}
func (*tuiReconnect) Cancel()      {}
func (*tuiReconnect) Close() error { return nil }
func (r *tuiReconnect) Err() error { return r.err }
