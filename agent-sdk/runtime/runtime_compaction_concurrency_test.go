package runtime

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
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
	serviceA := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir: root, SessionIDGenerator: func() string { return "sess-two-runtime-compact" },
	}))
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
	serviceB := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root}))
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
	checkpointMessage := model.NewTextMessage(model.RoleUser, "CONTEXT CHECKPOINT\nsummary through Seq 10")
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
