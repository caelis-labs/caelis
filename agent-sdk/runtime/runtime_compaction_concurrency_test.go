package runtime

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	agentmessage "github.com/caelis-labs/caelis/agent-sdk/message"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

type blockingRevisionCompactor struct {
	started chan compact.Request
	release chan struct{}
}

func (c *blockingRevisionCompactor) Prepare(_ context.Context, req compact.Request) (compact.Result, error) {
	return compact.Result{PromptEvents: compact.PromptEventsFromLatestCompact(req.Events)}, nil
}

func (*blockingRevisionCompactor) CompactOnOverflow(context.Context, compact.Request, error) (compact.Result, error) {
	return compact.Result{}, nil
}

func (c *blockingRevisionCompactor) Force(_ context.Context, req compact.Request, trigger string) (compact.Result, error) {
	c.started <- req
	<-c.release
	covered := session.LastEventSeq(req.Events)
	event := buildCompactEvent(req.Session, "CONTEXT CHECKPOINT\nsummary through source revision", compact.CompactEventData{
		ContractVersion:      compact.CompactContractVersion,
		SummarizedThroughSeq: covered,
		SummarizedThroughID:  lastEventID(req.Events),
		SourceEventCount:     len(req.Events),
		Trigger:              trigger,
		Generator:            "blocking_revision_test",
	})
	return compact.Result{Compacted: true, CompactEvent: event}, nil
}

type capturingContextModel struct {
	messages chan []model.Message
}

type blockingAgentMessageService struct {
	session.Service
	started chan struct{}
	release chan struct{}
}

func (s *blockingAgentMessageService) AppendEventWithOutcome(ctx context.Context, req session.AppendEventRequest) (session.AppendEventResult, error) {
	if req.Event != nil && session.EventTypeOf(req.Event) == session.EventTypeContext {
		close(s.started)
		select {
		case <-ctx.Done():
			return session.AppendEventResult{}, ctx.Err()
		case <-s.release:
		}
	}
	return session.AppendEventWithOutcome(ctx, s.Service, req)
}

func completionAgentMessage(id string) agentmessage.Request {
	return agentmessage.Request{
		MessageID: id,
		To:        agentmessage.Parent,
		Text:      "Subagent @reviewer is completed. Use Task read for its full result.",
		From: session.ActorRef{
			Kind: session.ActorKindParticipant,
			ID:   "reviewer-agent",
			Name: "@reviewer",
		},
	}
}

func TestCompactionAndAgentCompletionCommitFIFOWhenCompactionArrivesFirst(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "compact-completion-fifo-compact-first")
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("source before compact"))
	compactor := &blockingRevisionCompactor{started: make(chan compact.Request, 1), release: make(chan struct{})}
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Compactor: compactor})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	compactDone := make(chan error, 1)
	go func() {
		_, _, compactErr := runtime.compactAndNotify(context.Background(), activeSession.SessionRef, "turn-1", nil, func(
			ctx context.Context,
			current session.Session,
			events []*session.Event,
		) (compact.Result, error) {
			return compactor.Force(ctx, compact.Request{
				Session: current, SessionRef: activeSession.SessionRef, Events: events,
			}, "compact and completion FIFO")
		})
		compactDone <- compactErr
	}()
	<-compactor.started
	compactTicket := sessionWriteTailForTest(&runtime.sessionWrites, activeSession.SessionRef)

	messageDone := make(chan error, 1)
	go func() {
		_, messageErr := runtime.deliverAgentMessageToMain(context.Background(), activeSession.SessionRef, completionAgentMessage("compact-first"))
		messageDone <- messageErr
	}()
	waitForSessionWriteTailChange(t, &runtime.sessionWrites, activeSession.SessionRef, compactTicket)
	early := false
	select {
	case err := <-messageDone:
		early = true
		messageDone <- err
	default:
	}
	close(compactor.release)
	if err := <-compactDone; err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if err := <-messageDone; err != nil {
		t.Fatalf("deliverAgentMessageToMain() error = %v", err)
	}
	if early {
		t.Fatal("Agent completion committed before the earlier compaction transaction completed")
	}

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if len(loaded.Events) != 3 || !compact.IsCompactEvent(loaded.Events[1]) || session.EventTypeOf(loaded.Events[2]) != session.EventTypeContext {
		t.Fatalf("events = %#v, want source, compact, completion Context", loaded.Events)
	}
	checkpointMessage, ok := session.ModelMessageOf(loaded.Events[1])
	if !ok {
		t.Fatalf("compact event = %#v, want model checkpoint", loaded.Events[1])
	}
	probe := &capturingContextModel{messages: make(chan []model.Message, 1)}
	run, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "continue after compact and completion",
		AgentSpec:  agent.AgentSpec{Name: "chat", Model: probe},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, run.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}
	want := []model.Message{
		checkpointMessage,
		model.NewTextMessage(model.RoleUser, "Agent message from @reviewer: "+session.EventText(loaded.Events[2])),
		model.NewTextMessage(model.RoleUser, "continue after compact and completion"),
	}
	got := <-probe.messages
	if len(got) != len(want) {
		t.Fatalf("rebuilt model context = %#v, want checkpoint then completion %#v", got, want)
	}
	for index := range want {
		if got[index].Role != want[index].Role || got[index].TextContent() != want[index].TextContent() {
			t.Fatalf("rebuilt model context[%d] = %q/%q, want %q/%q", index, got[index].Role, got[index].TextContent(), want[index].Role, want[index].TextContent())
		}
	}
}

func TestCompactionAndAgentCompletionCommitFIFOWhenCompletionArrivesFirst(t *testing.T) {
	t.Parallel()

	base, activeSession := newTestSessionService(t, "compact-completion-fifo-completion-first")
	appendTestEvent(t, base, activeSession.SessionRef, userTextEvent("source before completion"))
	sessions := &blockingAgentMessageService{Service: base, started: make(chan struct{}), release: make(chan struct{})}
	compactor := &blockingRevisionCompactor{started: make(chan compact.Request, 1), release: make(chan struct{})}
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Compactor: compactor})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	messageDone := make(chan error, 1)
	go func() {
		_, messageErr := runtime.deliverAgentMessageToMain(context.Background(), activeSession.SessionRef, completionAgentMessage("completion-first"))
		messageDone <- messageErr
	}()
	<-sessions.started
	messageTicket := sessionWriteTailForTest(&runtime.sessionWrites, activeSession.SessionRef)

	compactDone := make(chan error, 1)
	go func() {
		_, compactErr := runtime.Compact(context.Background(), CompactRequest{
			SessionRef: activeSession.SessionRef,
			Trigger:    "completion and compact FIFO",
		})
		compactDone <- compactErr
	}()
	waitForSessionWriteTailChange(t, &runtime.sessionWrites, activeSession.SessionRef, messageTicket)
	compactorStartedEarly := false
	var source compact.Request
	select {
	case source = <-compactor.started:
		compactorStartedEarly = true
	default:
	}
	close(sessions.release)
	if err := <-messageDone; err != nil {
		t.Fatalf("deliverAgentMessageToMain() error = %v", err)
	}
	if !compactorStartedEarly {
		source = <-compactor.started
	}
	close(compactor.release)
	if err := <-compactDone; err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if compactorStartedEarly {
		t.Fatal("Compaction read its source before the earlier Agent completion committed")
	}
	if got := session.EventTypeOf(source.Events[len(source.Events)-1]); got != session.EventTypeContext {
		t.Fatalf("compaction source last event type = %q, want completion Context", got)
	}

	loaded, err := base.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if len(loaded.Events) != 3 || session.EventTypeOf(loaded.Events[1]) != session.EventTypeContext || !compact.IsCompactEvent(loaded.Events[2]) {
		t.Fatalf("events = %#v, want source, completion Context, compact", loaded.Events)
	}
}

func (*capturingContextModel) Name() string { return "capturing-context" }

func (m *capturingContextModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	messages := make([]model.Message, len(req.Messages))
	for index := range req.Messages {
		messages[index] = model.CloneMessage(req.Messages[index])
	}
	m.messages <- messages
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{Type: model.StreamEventTurnDone, Response: &model.Response{
			Message: model.NewTextMessage(model.RoleAssistant, "probe complete"), TurnComplete: true, StepComplete: true,
			Status: model.ResponseStatusCompleted, FinishReason: model.FinishReasonStop,
		}}, nil)
	}
}

func TestTwoRuntimesRejectStaleCompactionAndRebuildWholeModelContext(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	serviceA := sessionfile.NewStore(sessionfile.Config{
		RootDir: root, SessionIDGenerator: func() string { return "sess-two-runtime-compact" },
	})
	activeSession, err := serviceA.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	want := make([]model.Message, 0, 13)
	for index := 1; index <= 10; index++ {
		text := "source fact " + string(rune('A'-1+index))
		message := model.NewTextMessage(model.RoleUser, text)
		want = append(want, message)
		if _, err := serviceA.AppendEvent(context.Background(), session.AppendEventRequest{
			SessionRef: activeSession.SessionRef,
			Event:      &session.Event{Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Message: &message},
		}); err != nil {
			t.Fatalf("AppendEvent(source %d) error = %v", index, err)
		}
	}

	compactor := &blockingRevisionCompactor{started: make(chan compact.Request, 1), release: make(chan struct{})}
	runtimeA, err := New(Config{Sessions: serviceA, AgentFactory: chat.Factory{}, Compactor: compactor})
	if err != nil {
		t.Fatalf("New(runtimeA) error = %v", err)
	}
	serviceB := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	runtimeB, err := New(Config{Sessions: serviceB, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New(runtimeB) error = %v", err)
	}

	compactResult := make(chan error, 1)
	go func() {
		_, compactErr := runtimeA.Compact(context.Background(), CompactRequest{SessionRef: activeSession.SessionRef, Trigger: "test interleaving"})
		compactResult <- compactErr
	}()
	source := <-compactor.started
	if source.Session.Revision != 10 || session.LastEventSeq(source.Events) != 10 {
		t.Fatalf("compaction source = revision %d through Seq %d, want revision/Seq 10", source.Session.Revision, session.LastEventSeq(source.Events))
	}

	runB, err := runtimeB.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "concurrent unsummarized fact",
		AgentSpec:  agent.AgentSpec{Name: "chat", Model: staticModel{text: "runtime B reply"}},
	})
	if err != nil {
		t.Fatalf("runtimeB.Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, runB.Handle); err != nil {
		t.Fatalf("runtimeB runner error = %v", err)
	}
	want = append(want,
		model.NewTextMessage(model.RoleUser, "concurrent unsummarized fact"),
		model.NewTextMessage(model.RoleAssistant, "runtime B reply"),
	)
	close(compactor.release)
	if err := <-compactResult; !errors.Is(err, session.ErrRevisionConflict) {
		t.Fatalf("runtimeA.Compact() error = %v, want source revision conflict", err)
	}

	loaded, err := serviceA.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	for _, event := range loaded.Events {
		if compact.IsCompactEvent(event) {
			t.Fatalf("stale checkpoint persisted despite revision conflict: %+v", event)
		}
	}

	probe := &capturingContextModel{messages: make(chan []model.Message, 1)}
	runProbe, err := runtimeA.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "context probe",
		AgentSpec:  agent.AgentSpec{Name: "chat", Model: probe},
	})
	if err != nil {
		t.Fatalf("runtimeA.Run(probe) error = %v", err)
	}
	want = append(want, model.NewTextMessage(model.RoleUser, "context probe"))
	if _, err := drainRunnerEvents(t, runProbe.Handle); err != nil {
		t.Fatalf("probe runner error = %v", err)
	}
	if got := <-probe.messages; !reflect.DeepEqual(got, want) {
		t.Fatalf("rebuilt model context = %#v, want runtime-produced context %#v", got, want)
	}
}

func TestCompactionReplayRoundTripKeepsConcurrentCoveredSequenceSuccessor(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "compact-covered-seq-round-trip")
	for index := 1; index <= 10; index++ {
		message := model.NewTextMessage(model.RoleUser, "summarized source "+string(rune('A'-1+index)))
		if _, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{
			SessionRef: activeSession.SessionRef,
			Event:      &session.Event{Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Message: &message},
		}); err != nil {
			t.Fatalf("AppendEvent(source %d) error = %v", index, err)
		}
	}
	concurrentMessage := model.NewTextMessage(model.RoleUser, "concurrent Seq 11 model fact")
	concurrent, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: activeSession.SessionRef,
		Event:      &session.Event{Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Message: &concurrentMessage},
	})
	if err != nil || concurrent.Seq != 11 {
		t.Fatalf("AppendEvent(concurrent) = %+v, %v; want Seq 11", concurrent, err)
	}
	current, err := sessions.Session(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	checkpointMessage := model.NewTextMessage(model.RoleUser, normalizeCompactMarkdown("CONTEXT CHECKPOINT\nsummary through Seq 10"))
	checkpoint := buildCompactEvent(current, checkpointMessage.TextContent(), compact.CompactEventData{
		ContractVersion:      compact.CompactContractVersion,
		SummarizedThroughSeq: 10,
		SourceEventCount:     10,
		Generator:            "covered_seq_round_trip_test",
	})
	persistedCheckpoint, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: activeSession.SessionRef,
		Event:      checkpoint,
	})
	if err != nil || persistedCheckpoint.Seq != 12 {
		t.Fatalf("AppendEvent(checkpoint) = %+v, %v; want Seq 12", persistedCheckpoint, err)
	}

	probe := &capturingContextModel{messages: make(chan []model.Message, 1)}
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	run, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "round-trip probe",
		AgentSpec:  agent.AgentSpec{Name: "chat", Model: probe},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, run.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}
	want := []model.Message{
		checkpointMessage,
		concurrentMessage,
		model.NewTextMessage(model.RoleUser, "round-trip probe"),
	}
	if got := <-probe.messages; !reflect.DeepEqual(got, want) {
		t.Fatalf("rebuilt model context = %#v, want runtime-produced context %#v", got, want)
	}
}
