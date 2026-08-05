package taskstream

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

var taskStreamTestSecret = []byte("0123456789abcdef0123456789abcdef")

func TestServiceListsOnlyOwningSessionAndRejectsCrossSessionTask(t *testing.T) {
	store := newTaskStreamTestStore(
		taskStreamTestEntry("session-1", "task-1", task.KindSubagent),
		taskStreamTestEntry("session-2", "task-2", task.KindCommand),
	)
	streams := &taskStreamTestRuntime{snapshots: map[string]stream.Snapshot{
		"task-1": taskStreamTestSnapshot("session-1", "task-1", "turn-1", "one"),
		"task-2": taskStreamTestSnapshot("session-2", "task-2", "terminal-2", "two"),
	}}
	service := newTaskStreamTestService(t, store, streams, "generation-1")

	listed, err := service.List(context.Background(), Principal{ID: "owner"}, ListRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tasks) != 1 || listed.Tasks[0].TaskID != "task-1" || listed.Tasks[0].Handle != "handle-task-1" || listed.Tasks[0].AgentHandle != "orbit" || listed.Tasks[0].SessionID != "session-1" {
		t.Fatalf("List() = %#v, want only session-1/task-1", listed)
	}
	_, err = service.Events(context.Background(), Principal{ID: "owner"}, ReadRequest{SessionID: "session-1", TaskID: "task-2"})
	if !errorcode.Is(err, errorcode.PermissionDenied) {
		t.Fatalf("cross-session Events() error = %v, want permission_denied", err)
	}
}

func TestColdTerminalSubagentReplaysAssistantOnlySessionHistoryWithoutRuntime(t *testing.T) {
	t.Parallel()

	entry := taskStreamTestEntry("session-parent", "task-1", task.KindSubagent)
	entry.Running = false
	entry.State = task.StateCompleted
	entry.UpdatedAt = time.Unix(130, 0)
	entry.Spec = map[string]any{}
	entry.Spec["session_id"] = "session-child"
	entry.Metadata["agent_id"] = "participant-1"
	entry.Metadata["stream_event_cursor"] = float64(12)
	runtimeSubscribeStarted := make(chan struct{})
	runtime := &taskStreamTestRuntime{
		readSnapshot: func(stream.ReadRequest) (stream.Snapshot, error) {
			return stream.Snapshot{}, errorcode.New(errorcode.Unavailable, "Runtime is not activated")
		},
		subscribeStarted: runtimeSubscribeStarted,
	}
	loadCalls := 0
	loader := taskStreamTestSessionLoader{loaded: session.LoadedSession{Events: []*session.Event{
		{
			ID: "assistant-1", MessageID: "assistant-1", Type: session.EventTypeAssistant,
			Time: time.Unix(100, 0), Scope: &session.EventScope{TurnID: "child-turn-1"}, Text: "first durable answer",
			Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage)}},
		},
		{
			ID: "reasoning-2", MessageID: "reasoning-2", Type: session.EventTypeAssistant,
			Time: time.Unix(110, 0), Scope: &session.EventScope{TurnID: "child-turn-2"}, Text: "reasoning must stay out",
			Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeAgentThought)}},
		},
		{
			ID: "assistant-2", MessageID: "assistant-2", Type: session.EventTypeAssistant,
			Time: time.Unix(120, 0), Scope: &session.EventScope{TurnID: "child-turn-2"}, Text: "second durable answer",
			Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage)}},
		},
	}}, calls: &loadCalls}
	entries := []*task.Entry{entry}
	for index := 2; index <= 64; index++ {
		sibling := taskStreamTestEntry("session-parent", fmt.Sprintf("task-%d", index), task.KindSubagent)
		sibling.Running = false
		sibling.State = task.StateCompleted
		sibling.Spec = map[string]any{}
		sibling.Spec["session_id"] = fmt.Sprintf("session-child-%d", index)
		entries = append(entries, sibling)
	}
	service, err := New(Config{
		Tasks: newTaskStreamTestStore(entries...), Streams: func() stream.Service { return runtime }, Sessions: loader,
		Authorizer: taskStreamTestAuthorizer{}, Secret: taskStreamTestSecret, Generation: "generation-cold",
	})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := service.List(context.Background(), Principal{ID: "owner"}, ListRequest{SessionID: "session-parent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tasks) != len(entries) || loadCalls != 0 {
		t.Fatalf("directory listing = %d Tasks, child Session loads = %d; want %d metadata rows and zero loads", len(listed.Tasks), loadCalls, len(entries))
	}

	result, err := service.Subscribe(context.Background(), Principal{ID: "owner"}, SubscribeRequest{
		SessionID: "session-parent", TaskID: "task-1", Follow: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()

	var records []Record
	for {
		select {
		case record, open := <-result.Subscription.Records():
			if !open {
				if err := result.Subscription.Err(); err != nil {
					t.Fatalf("historical subscription error = %v", err)
				}
				goto drained
			}
			records = append(records, record)
		case <-time.After(time.Second):
			t.Fatal("timed out draining historical subagent replay")
		}
	}

drained:
	texts := make([]string, 0, 2)
	turns := make([]string, 0, 2)
	for _, record := range records {
		if record.Frame == nil || record.Frame.Event == nil {
			continue
		}
		texts = append(texts, session.EventText(record.Frame.Event))
		turns = append(turns, record.Frame.Ref.TerminalID)
	}
	if fmt.Sprint(texts) != "[first durable answer second durable answer]" {
		t.Fatalf("assistant-only replay = %#v", texts)
	}
	if fmt.Sprint(turns) != "[task-1:1 task-1:2]" {
		t.Fatalf("multi-Turn replay boundaries = %#v", turns)
	}
	if loadCalls != 1 {
		t.Fatalf("selected child Session loads = %d, want exactly 1", loadCalls)
	}
	if len(records) == 0 || records[len(records)-1].Frame == nil || !records[len(records)-1].Frame.Closed {
		t.Fatalf("historical replay did not end with terminal lifecycle: %#v", records)
	}
	select {
	case <-runtimeSubscribeStarted:
		t.Fatal("historical terminal replay activated the unavailable Runtime subscription")
	default:
	}
}

func TestTaskCursorIsBoundToSessionAndTask(t *testing.T) {
	store := newTaskStreamTestStore(
		taskStreamTestEntry("session-1", "task-1", task.KindCommand),
		taskStreamTestEntry("session-1", "task-2", task.KindCommand),
	)
	streams := &taskStreamTestRuntime{snapshots: map[string]stream.Snapshot{
		"task-1": taskStreamTestSnapshot("session-1", "task-1", "terminal-shared", "same"),
		"task-2": taskStreamTestSnapshot("session-1", "task-2", "terminal-shared", "same"),
	}}
	service := newTaskStreamTestService(t, store, streams, "generation-1")

	first, err := service.Events(context.Background(), Principal{ID: "owner"}, ReadRequest{SessionID: "session-1", TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Records) == 0 || first.Records[0].Task.TaskID != "task-1" {
		t.Fatalf("task-1 records = %#v", first.Records)
	}
	_, err = service.Events(context.Background(), Principal{ID: "owner"}, ReadRequest{
		SessionID: "session-1", TaskID: "task-2", Cursor: first.BoundaryCursor,
	})
	if !errorcode.Is(err, errorcode.InvalidArgument) {
		t.Fatalf("cross-task cursor error = %v, want invalid_argument", err)
	}
}

func TestEventsReportsEvictedPrefixAndProjectsRetainedSuffix(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindCommand)
	store := newTaskStreamTestStore(entry)
	snapshot := taskStreamTestSnapshot("session-1", "task-1", "terminal-1", "retained")
	snapshot.EventsTruncatedBefore = 5
	snapshot.TruncatedBefore = 9
	snapshot.Cursor = stream.Cursor{Events: 7, Output: 17}
	snapshot.Frames[0].Cursor = stream.Cursor{Events: 6, Output: 17}
	snapshot.Frames[0].EventsTruncatedBefore = 5
	snapshot.Frames[0].TruncatedBefore = 9
	snapshot.Frames[1].Cursor = snapshot.Cursor
	service := newTaskStreamTestService(t, store, &taskStreamTestRuntime{snapshots: map[string]stream.Snapshot{"task-1": snapshot}}, "generation-1")

	batch, err := service.Events(context.Background(), Principal{ID: "owner"}, ReadRequest{SessionID: "session-1", TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}
	if batch.ResumeMode != ResumeModeCurrentState || !batch.TransientGap || len(batch.Records) != 3 {
		t.Fatalf("evicted read = %#v, want gap plus two retained frames", batch)
	}
	if batch.Records[0].Gap == nil || batch.Records[0].Gap.TaskID != "task-1" {
		t.Fatalf("gap record = %#v", batch.Records[0])
	}
}

func TestEventsProjectsOversizedFrameMarkerAsGapWithoutBody(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindSubagent)
	entry.State = task.StateRunning
	entry.Running = true
	snapshot := stream.Snapshot{
		Ref:    stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-1"},
		Cursor: stream.Cursor{Events: 1, Output: 5 * 1024 * 1024}, State: string(task.StateRunning), Running: true,
		Frames: []stream.Frame{{
			Ref:    stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-1"},
			Cursor: stream.Cursor{Events: 1, Output: 5 * 1024 * 1024}, EventsTruncatedBefore: 1, Running: true,
		}},
	}
	service := newTaskStreamTestService(t, newTaskStreamTestStore(entry), &taskStreamTestRuntime{
		snapshots: map[string]stream.Snapshot{"task-1": snapshot},
	}, "generation-1")

	batch, err := service.Events(context.Background(), Principal{ID: "owner"}, ReadRequest{SessionID: "session-1", TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}
	if batch.ResumeMode != ResumeModeCurrentState || !batch.TransientGap || len(batch.Records) != 2 || batch.Records[0].Gap == nil {
		t.Fatalf("oversized-frame batch = %#v, want gap plus cursor marker", batch)
	}
	if batch.Records[1].Frame == nil || batch.Records[1].Frame.Text != "" || batch.Records[1].Frame.Event != nil {
		t.Fatalf("oversized-frame marker retained a body: %#v", batch.Records[1])
	}
}

func TestGenerationChangeFallsBackToCurrentTaskState(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindCommand)
	store := newTaskStreamTestStore(entry)
	oldRuntime := &taskStreamTestRuntime{snapshots: map[string]stream.Snapshot{
		"task-1": taskStreamTestSnapshot("session-1", "task-1", "terminal-1", "old output"),
	}}
	oldService := newTaskStreamTestService(t, store, oldRuntime, "old-generation")
	oldBatch, err := oldService.Events(context.Background(), Principal{ID: "owner"}, ReadRequest{SessionID: "session-1", TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}

	current := taskStreamTestSnapshot("session-1", "task-1", "terminal-1", "must not replay")
	newService := newTaskStreamTestService(t, store, &taskStreamTestRuntime{snapshots: map[string]stream.Snapshot{"task-1": current}}, "new-generation")
	resumed, err := newService.Events(context.Background(), Principal{ID: "owner"}, ReadRequest{
		SessionID: "session-1", TaskID: "task-1", Cursor: oldBatch.BoundaryCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ResumeMode != ResumeModeCurrentState || !resumed.TransientGap || len(resumed.Records) != 2 {
		t.Fatalf("generation fallback = %#v, want gap plus current terminal", resumed)
	}
	for _, record := range resumed.Records {
		if record.Frame != nil && strings.Contains(record.Frame.Text, "must not replay") {
			t.Fatalf("generation fallback replayed transient body: %#v", resumed.Records)
		}
	}
}

func TestGenerationChangeReplaysSubagentLatestFinalCurrentState(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindSubagent)
	store := newTaskStreamTestStore(entry)
	oldRuntime := &taskStreamTestRuntime{snapshots: map[string]stream.Snapshot{
		"task-1": taskStreamTestSnapshot("session-1", "task-1", "turn-1", "old transient output"),
	}}
	oldService := newTaskStreamTestService(t, store, oldRuntime, "old-generation")
	oldBatch, err := oldService.Events(context.Background(), Principal{ID: "owner"}, ReadRequest{SessionID: "session-1", TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}

	final := &session.Event{
		ID: "final-1", MessageID: "final-1", Type: session.EventTypeAssistant,
		Text: "latest exact Final", Scope: &session.EventScope{TurnID: "turn-1", Source: "subagent_result"},
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
			MessageID:     "final-1", Content: session.ProtocolTextContent("latest exact Final"),
		}},
	}
	current := stream.Snapshot{
		Ref:    stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-1"},
		Cursor: stream.Cursor{Events: 6}, EventsTruncatedBefore: 5,
		State: string(task.StateCompleted), TerminalFramed: true,
		Frames: []stream.Frame{
			{Ref: stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-1"}, Cursor: stream.Cursor{Events: 6}, EventsTruncatedBefore: 5, Event: final},
			{Ref: stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-1"}, Cursor: stream.Cursor{Events: 6}, EventsTruncatedBefore: 5, State: string(task.StateCompleted), Closed: true},
		},
	}
	newService := newTaskStreamTestService(t, store, &taskStreamTestRuntime{snapshots: map[string]stream.Snapshot{"task-1": current}}, "new-generation")
	resumed, err := newService.Events(context.Background(), Principal{ID: "owner"}, ReadRequest{
		SessionID: "session-1", TaskID: "task-1", Cursor: oldBatch.BoundaryCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ResumeMode != ResumeModeCurrentState || !resumed.TransientGap || len(resumed.Records) != 3 {
		t.Fatalf("subagent generation fallback = %#v, want gap plus Final and terminal", resumed)
	}
	if resumed.Records[0].Gap == nil || resumed.Records[1].Frame == nil || session.EventText(resumed.Records[1].Frame.Event) != "latest exact Final" {
		t.Fatalf("subagent current state = %#v", resumed.Records)
	}
}

func TestMultiTurnCurrentStateKeepsRunningDescriptorAndCarriesHistoricalDisplayBoundary(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindSubagent)
	entry.State = task.StateRunning
	entry.Running = true
	historicalFinal := &session.Event{
		ID: "final-turn-1", MessageID: "final-turn-1", Type: session.EventTypeAssistant,
		Text: "turn one final", Scope: &session.EventScope{TurnID: "turn-1", Source: "subagent_result"},
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
			MessageID:     "final-turn-1", Content: session.ProtocolTextContent("turn one final"),
		}},
	}
	currentThought := &session.Event{
		ID: "thought-turn-2", MessageID: "thought-turn-2", Type: session.EventTypeAssistant,
		Text: "working", Scope: &session.EventScope{TurnID: "turn-2"},
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeAgentThought),
			MessageID:     "thought-turn-2", Content: session.ProtocolTextContent("working"),
		}},
	}
	historicalBoundary := &session.Event{
		ID: "boundary-turn-1", Type: session.EventTypeLifecycle, Visibility: session.VisibilityUIOnly,
		Time: time.Unix(105, 0),
		Scope: &session.EventScope{
			TurnID: "turn-1", Source: "task_stream_turn_boundary",
			Participant: session.ParticipantRef{
				ID: "task-1", Kind: session.ParticipantKindSubagent, DelegationID: "task-1",
			},
		},
		Lifecycle: &session.EventLifecycle{Status: string(task.StateCompleted)},
	}
	current := stream.Snapshot{
		Ref:    stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-2"},
		Cursor: stream.Cursor{Events: 200}, EventsTruncatedBefore: 72,
		State: string(task.StateRunning), Running: true,
		Frames: []stream.Frame{
			{Ref: stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-1"}, Cursor: stream.Cursor{Events: 200}, EventsTruncatedBefore: 72, Event: historicalFinal},
			{Ref: stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-1"}, Cursor: stream.Cursor{Events: 200}, EventsTruncatedBefore: 72, UpdatedAt: historicalBoundary.Time, Event: historicalBoundary},
			{Ref: stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-2"}, Cursor: stream.Cursor{Events: 200}, EventsTruncatedBefore: 72, Running: true, Event: currentThought},
		},
	}
	service := newTaskStreamTestService(t, newTaskStreamTestStore(entry), &taskStreamTestRuntime{
		snapshots: map[string]stream.Snapshot{"task-1": current},
	}, "generation-1")
	batch, err := service.Events(context.Background(), Principal{ID: "owner"}, ReadRequest{
		SessionID: "session-1", TaskID: "task-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if batch.ResumeMode != ResumeModeCurrentState || !batch.TransientGap || len(batch.Records) != 4 {
		t.Fatalf("multi-turn current state = %#v, want gap plus Final, boundary, and current reasoning", batch)
	}
	foundBoundary := false
	for _, record := range batch.Records {
		if record.Task.State != task.StateRunning || !record.Task.Running {
			t.Fatalf("current-state descriptor regressed from running: %#v", record)
		}
		if record.Frame != nil && record.Frame.Ref.TerminalID == "turn-1" &&
			(record.Frame.Closed || stream.IsTerminalState(record.Frame.State)) {
			t.Fatalf("current state projected historical terminal: %#v", record.Frame)
		}
		if record.Frame != nil && record.Frame.Event != nil &&
			session.EventTypeOf(record.Frame.Event) == session.EventTypeLifecycle {
			foundBoundary = record.Frame.Event.Lifecycle != nil &&
				record.Frame.Event.Lifecycle.Status == string(task.StateCompleted)
		}
	}
	if !foundBoundary {
		t.Fatalf("current state omitted historical display boundary: %#v", batch.Records)
	}
}

func TestCurrentStatePartialCursorReplaysWholeSemanticBatch(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindSubagent)
	entry.State = task.StateRunning
	entry.Running = true
	final := &session.Event{
		ID: "final-turn-1", MessageID: "final-turn-1", Type: session.EventTypeAssistant,
		Text: "turn one final", Scope: &session.EventScope{TurnID: "turn-1", Source: "subagent_result"},
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
			MessageID:     "final-turn-1", Content: session.ProtocolTextContent("turn one final"),
		}},
	}
	thought := &session.Event{
		ID: "thought-turn-2", MessageID: "thought-turn-2", Type: session.EventTypeAssistant,
		Text: "working", Scope: &session.EventScope{TurnID: "turn-2"},
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeAgentThought),
			MessageID:     "thought-turn-2", Content: session.ProtocolTextContent("working"),
		}},
	}
	current := stream.Snapshot{
		Ref:    stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-2"},
		Cursor: stream.Cursor{Events: 200}, EventsTruncatedBefore: 72,
		State: string(task.StateRunning), Running: true,
		Frames: []stream.Frame{
			{Ref: stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-1"}, Cursor: stream.Cursor{Events: 200}, EventsTruncatedBefore: 72, Event: final},
			{Ref: stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-2"}, Cursor: stream.Cursor{Events: 200}, EventsTruncatedBefore: 72, Running: true, Event: thought},
		},
	}
	runtime := &taskStreamTestRuntime{readSnapshot: func(req stream.ReadRequest) (stream.Snapshot, error) {
		if req.Cursor.Events >= current.Cursor.Events {
			return stream.Snapshot{Ref: current.Ref, Cursor: current.Cursor, State: current.State, Running: true}, nil
		}
		return stream.CloneSnapshot(current), nil
	}}
	api := newTaskStreamTestService(t, newTaskStreamTestStore(entry), runtime, "generation-1")

	first, err := api.Events(context.Background(), Principal{ID: "owner"}, ReadRequest{
		SessionID: "session-1", TaskID: "task-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Records) != 3 || first.Records[0].Gap == nil {
		t.Fatalf("first current state = %#v, want gap plus two semantic frames", first)
	}
	service := api.(*service)
	partial, sameGeneration, err := service.cursors.decode("session-1", "task-1", first.Records[1].Cursor)
	if err != nil || !sameGeneration || partial.Cursor != (stream.Cursor{}) {
		t.Fatalf("partial current-state cursor = %#v same=%v err=%v, want pre-gap anchor", partial, sameGeneration, err)
	}
	boundary, _, err := service.cursors.decode("session-1", "task-1", first.Records[len(first.Records)-1].Cursor)
	if err != nil || boundary.Cursor != current.Cursor {
		t.Fatalf("final current-state cursor = %#v err=%v, want boundary %#v", boundary, err, current.Cursor)
	}

	resumed, err := api.Events(context.Background(), Principal{ID: "owner"}, ReadRequest{
		SessionID: "session-1", TaskID: "task-1", Cursor: first.Records[1].Cursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.Records) != 3 || resumed.Records[0].Gap == nil ||
		resumed.Records[2].Frame == nil || session.EventText(resumed.Records[2].Frame.Event) != "working" {
		t.Fatalf("resumed current state = %#v, want whole semantic batch replay", resumed)
	}
}

func TestLiveGapCatchupUsesSnapshotDescriptorAndBoundedDrain(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindSubagent)
	entry.State = task.StateCompleted
	entry.Running = false
	initial := stream.Snapshot{
		Ref:    stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-1"},
		Cursor: stream.Cursor{Events: 10}, State: string(task.StateCompleted), TerminalFramed: true,
	}
	frames := make([]stream.Frame, 0, subscriberEventCap+2)
	for index := 0; index < subscriberEventCap+2; index++ {
		turnID := "turn-2"
		running := true
		if index == 0 {
			turnID = "turn-1"
			running = false
		}
		frames = append(frames, stream.Frame{
			Ref:  stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: turnID},
			Text: fmt.Sprintf("semantic-%03d", index), Cursor: stream.Cursor{Events: 300},
			EventsTruncatedBefore: 100, Running: running,
		})
	}
	current := stream.Snapshot{
		Ref:    stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-2"},
		Cursor: stream.Cursor{Events: 300}, EventsTruncatedBefore: 100,
		State: string(task.StateRunning), Running: true, Frames: frames,
	}
	runtime := &taskStreamTestRuntime{
		readSnapshots:         []stream.Snapshot{initial, current},
		subscribeFramesByCall: [][]stream.Frame{frames},
	}
	api := newTaskStreamTestService(t, newTaskStreamTestStore(entry), runtime, "generation-1")
	result, err := api.Subscribe(context.Background(), Principal{ID: "owner"}, SubscribeRequest{
		SessionID: "session-1", TaskID: "task-1", Follow: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()

	// Let the catch-up exceed the live queue capacity before consuming it. A
	// fail-fast live path would close here; bounded catch-up must wait instead.
	time.Sleep(20 * time.Millisecond)
	records := make([]Record, 0, len(frames)+1)
	for len(records) < len(frames)+1 {
		select {
		case record, ok := <-result.Subscription.Records():
			if !ok {
				t.Fatalf("live recovery closed after %d records: %v", len(records), result.Subscription.Err())
			}
			records = append(records, record)
		case <-time.After(time.Second):
			t.Fatalf("timed out after %d live recovery records", len(records))
		}
	}
	if records[0].Gap == nil || records[0].Task.State != task.StateRunning || !records[0].Task.Running || records[0].Task.CurrentTurnID != "turn-2" {
		t.Fatalf("live recovery gap descriptor = %#v, want running turn-2 snapshot", records[0])
	}
	for _, record := range records[1:] {
		if record.Task.State != task.StateRunning || !record.Task.Running || record.Task.CurrentTurnID != "turn-2" {
			t.Fatalf("live recovery frame descriptor = %#v, want running turn-2 snapshot", record.Task)
		}
	}
	if err := result.Subscription.Err(); err != nil {
		t.Fatalf("live recovery error = %v", err)
	}
}

func TestCloseSubscriptionCancelsOnlyDelivery(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindSubagent)
	entry.State = task.StateRunning
	entry.Running = true
	runtime := &taskStreamTestRuntime{
		snapshots: map[string]stream.Snapshot{"task-1": {
			Ref:   stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-1"},
			State: string(task.StateRunning), Running: true,
		}},
		subscribeStarted: make(chan struct{}), subscribeStopped: make(chan struct{}),
	}
	service := newTaskStreamTestService(t, newTaskStreamTestStore(entry), runtime, "generation-1")
	result, err := service.Subscribe(context.Background(), Principal{ID: "owner"}, SubscribeRequest{
		SessionID: "session-1", TaskID: "task-1", Follow: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitTaskStreamSignal(t, runtime.subscribeStarted, "runtime subscription start")
	runtime.mu.Lock()
	request := runtime.lastSubscribeRequest
	runtime.mu.Unlock()
	if !request.Follow {
		t.Fatalf("Runtime Subscribe request = %#v, want Follow", request)
	}
	if err := result.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	waitTaskStreamSignal(t, runtime.subscribeStopped, "runtime subscription stop")
	if entry.State != task.StateRunning || !entry.Running {
		t.Fatalf("closing delivery changed Task state: %#v", entry)
	}
}

func TestSubscribeRefreshesTaskDescriptorWithinOneActivityPeriod(t *testing.T) {
	t.Parallel()

	entry := taskStreamTestEntry("session-1", "task-1", task.KindSubagent)
	entry.State = task.StateRunning
	entry.Running = true
	entry.Metadata["turn_id"] = "turn-2"
	initial := stream.Snapshot{
		Ref:     stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-2"},
		Cursor:  stream.Cursor{Events: 1},
		State:   string(task.StateRunning),
		Running: true,
	}
	runtime := &taskStreamTestRuntime{
		snapshots: map[string]stream.Snapshot{"task-1": initial},
		subscribeFrames: []stream.Frame{
			{
				Ref:  stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-2"},
				Text: "continued", State: string(task.StateRunning), Running: true,
				Cursor: stream.Cursor{Events: 2, Output: int64(len("continued"))}, UpdatedAt: time.Unix(200, 0),
			},
			{
				Ref:   stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-2"},
				State: string(task.StateCompleted), Cursor: stream.Cursor{Events: 3, Output: int64(len("continued"))},
				Closed: true, UpdatedAt: time.Unix(300, 0),
			},
		},
	}
	service := newTaskStreamTestService(t, newTaskStreamTestStore(entry), runtime, "generation-1")
	result, err := service.Subscribe(context.Background(), Principal{ID: "owner"}, SubscribeRequest{
		SessionID: "session-1", TaskID: "task-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()

	var continued []Record
	for record := range result.Subscription.Records() {
		if record.Frame != nil && record.Frame.Ref.TerminalID == "turn-2" {
			continued = append(continued, record)
		}
	}
	if len(continued) != 2 {
		t.Fatalf("continued records = %#v, want running and terminal turn-2 frames", continued)
	}
	if got := continued[0].Task; got.CurrentTurnID != "turn-2" || got.State != task.StateRunning || !got.Running || got.SupportsInput {
		t.Fatalf("running descriptor = %#v, want live turn-2 state", got)
	}
	if got := continued[1].Task; got.CurrentTurnID != "turn-2" || got.State != task.StateCompleted || got.Running || got.SupportsInput {
		t.Fatalf("terminal descriptor = %#v, want completed turn-2 without Task input", got)
	}
}

func TestTaskDescriptorRejectsTerminalRunningContradiction(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindSubagent)
	entry.State = task.StateRunning
	entry.Running = true

	fromFrame := descriptorForFrame(entry, stream.Frame{
		State: string(task.StateCompleted), Running: true, Closed: true,
	})
	if fromFrame.State != task.StateCompleted || fromFrame.Running {
		t.Fatalf("terminal frame descriptor = %#v, want completed and not running", fromFrame)
	}
	fromSnapshot := descriptorForSnapshot(entry, stream.Snapshot{
		State: string(task.StateCompleted), Running: true, TerminalFramed: true,
	})
	if fromSnapshot.State != task.StateCompleted || fromSnapshot.Running {
		t.Fatalf("terminal snapshot descriptor = %#v, want completed and not running", fromSnapshot)
	}
}

func TestInitialCatchupDoesNotUseLiveSlowConsumerBudget(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindCommand)
	frames := make([]stream.Frame, 0, subscriberEventCap+2)
	for index := 1; index <= subscriberEventCap+2; index++ {
		frames = append(frames, stream.Frame{
			Ref:  stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "terminal-1"},
			Text: "chunk", Running: true, Cursor: stream.Cursor{Events: int64(index), Output: int64(index * 5)},
		})
	}
	runtime := &taskStreamTestRuntime{snapshots: map[string]stream.Snapshot{"task-1": {
		Ref:    stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "terminal-1"},
		Cursor: stream.Cursor{Events: int64(len(frames)), Output: int64(len(frames) * 5)},
		State:  string(task.StateRunning), Running: true, Frames: frames,
	}}}
	service := newTaskStreamTestService(t, newTaskStreamTestStore(entry), runtime, "generation-1")
	result, err := service.Subscribe(context.Background(), Principal{ID: "owner"}, SubscribeRequest{SessionID: "session-1", TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	for index := range frames {
		select {
		case record, ok := <-result.Subscription.Records():
			if !ok {
				t.Fatalf("initial catch-up closed after %d records: %v", index, result.Subscription.Err())
			}
			if record.Frame == nil || record.Frame.Cursor.Events != int64(index+1) {
				t.Fatalf("initial record %d = %#v", index, record)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out receiving initial record %d", index)
		}
	}
	if errors.Is(result.Subscription.Err(), ErrSlowConsumer) {
		t.Fatalf("initial catch-up used live slow-consumer failure: %v", result.Subscription.Err())
	}
}

func TestSubscribeDrainsInitialCatchupBeforeStartingLiveDelivery(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindSubagent)
	entry.State = task.StateRunning
	entry.Running = true
	frames := make([]stream.Frame, 0, subscriberEventCap)
	for index := 1; index <= subscriberEventCap; index++ {
		frames = append(frames, stream.Frame{
			Ref:  stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-1"},
			Text: "initial", Running: true, Cursor: stream.Cursor{Events: int64(index)},
		})
	}
	runtime := &taskStreamTestRuntime{
		snapshots: map[string]stream.Snapshot{"task-1": {
			Ref:    stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-1"},
			Cursor: stream.Cursor{Events: subscriberEventCap}, State: string(task.StateRunning), Running: true,
			Frames: frames,
		}},
		subscribeFrames: []stream.Frame{
			{Ref: stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-1"}, Text: "live-1", Running: true, Cursor: stream.Cursor{Events: subscriberEventCap + 1}},
			{Ref: stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-1"}, Text: "live-2", Running: true, Cursor: stream.Cursor{Events: subscriberEventCap + 2}},
		},
		subscribeStarted: make(chan struct{}),
	}
	service := newTaskStreamTestService(t, newTaskStreamTestStore(entry), runtime, "generation-1")
	result, err := service.Subscribe(context.Background(), Principal{ID: "owner"}, SubscribeRequest{
		SessionID: "session-1", TaskID: "task-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()

	select {
	case <-runtime.subscribeStarted:
		// The old implementation reaches live delivery while initial records are
		// still queued. Continue draining so the assertion below captures the
		// resulting slow-consumer disconnect instead of deadlocking the test.
	case <-time.After(20 * time.Millisecond):
	}
	for index := 1; index <= subscriberEventCap+2; index++ {
		select {
		case record, ok := <-result.Subscription.Records():
			if !ok {
				t.Fatalf("subscription closed after %d records: %v", index-1, result.Subscription.Err())
			}
			if record.Frame == nil || record.Frame.Cursor.Events != int64(index) {
				t.Fatalf("record %d = %#v", index, record)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out receiving record %d", index)
		}
	}
	if err := result.Subscription.Err(); err != nil {
		t.Fatalf("initial-to-live transition failed: %v", err)
	}
}

func newTaskStreamTestService(t *testing.T, store task.Store, runtime stream.Service, generation string) Service {
	t.Helper()
	service, err := New(Config{
		Tasks: store, Streams: func() stream.Service { return runtime }, Authorizer: taskStreamTestAuthorizer{},
		Secret: taskStreamTestSecret, Generation: generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func taskStreamTestEntry(sessionID, taskID string, kind task.Kind) *task.Entry {
	return &task.Entry{
		TaskID: taskID, Handle: "handle-" + taskID, Session: session.SessionRef{SessionID: sessionID}, Kind: kind,
		Title: "Task " + taskID, State: task.StateCompleted, SupportsInput: false,
		SupportsCancel: true, Metadata: map[string]any{"parent_call": "parent-" + taskID, "turn_id": "turn-1", "agent": "orbit"},
	}
}

func taskStreamTestSnapshot(sessionID, taskID, terminalID, text string) stream.Snapshot {
	ref := stream.Ref{SessionID: sessionID, TaskID: taskID, TerminalID: terminalID}
	return stream.Snapshot{
		Ref: ref, Cursor: stream.Cursor{Events: 2, Output: int64(len(text))}, State: string(task.StateCompleted),
		TerminalFramed: true,
		Frames: []stream.Frame{
			{Ref: ref, Text: text, Cursor: stream.Cursor{Events: 1, Output: int64(len(text))}},
			{Ref: ref, State: string(task.StateCompleted), Cursor: stream.Cursor{Events: 2, Output: int64(len(text))}, Closed: true},
		},
	}
}

func waitTaskStreamSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

type taskStreamTestAuthorizer struct{}

func (taskStreamTestAuthorizer) AuthorizeTaskStream(_ context.Context, principal Principal, _ string) error {
	if strings.TrimSpace(principal.ID) == "" {
		return errorcode.New(errorcode.Unauthenticated, "missing principal")
	}
	return nil
}

type taskStreamTestSessionLoader struct {
	loaded session.LoadedSession
	err    error
	calls  *int
}

func (l taskStreamTestSessionLoader) LoadSession(_ context.Context, req session.LoadSessionRequest) (session.LoadedSession, error) {
	if l.calls != nil {
		(*l.calls)++
	}
	if l.err != nil {
		return session.LoadedSession{}, l.err
	}
	loaded := l.loaded
	loaded.Session.SessionRef = req.SessionRef
	return loaded, nil
}

type taskStreamTestStore struct {
	entries map[string]*task.Entry
}

func newTaskStreamTestStore(entries ...*task.Entry) *taskStreamTestStore {
	store := &taskStreamTestStore{entries: map[string]*task.Entry{}}
	for _, entry := range entries {
		store.entries[entry.TaskID] = task.CloneEntry(entry)
	}
	return store
}

func (s *taskStreamTestStore) Upsert(_ context.Context, entry *task.Entry) error {
	s.entries[entry.TaskID] = task.CloneEntry(entry)
	return nil
}

func (s *taskStreamTestStore) Get(_ context.Context, taskID string) (*task.Entry, error) {
	entry := s.entries[taskID]
	if entry == nil {
		return nil, errors.New("task not found")
	}
	return task.CloneEntry(entry), nil
}

func (s *taskStreamTestStore) ListSession(_ context.Context, ref session.SessionRef) ([]*task.Entry, error) {
	var entries []*task.Entry
	for _, entry := range s.entries {
		if entry.Session.SessionID == ref.SessionID {
			entries = append(entries, task.CloneEntry(entry))
		}
	}
	return entries, nil
}

func (s *taskStreamTestStore) GetSessionTaskByHandle(_ context.Context, ref session.SessionRef, handle string) (*task.Entry, error) {
	for _, entry := range s.entries {
		if entry.Session.SessionID == ref.SessionID && task.NormalizeHandle(firstString(entry.Handle, mapString(entry.Metadata, "handle"))) == task.NormalizeHandle(handle) {
			return task.CloneEntry(entry), nil
		}
	}
	return nil, errors.New("task not found")
}

type taskStreamTestRuntime struct {
	mu                    sync.Mutex
	snapshots             map[string]stream.Snapshot
	readSnapshot          func(stream.ReadRequest) (stream.Snapshot, error)
	readSnapshots         []stream.Snapshot
	readCalls             int
	subscribeFrames       []stream.Frame
	subscribeFramesByCall [][]stream.Frame
	subscribeCalls        int
	lastSubscribeRequest  stream.SubscribeRequest
	subscribeStarted      chan struct{}
	subscribeStopped      chan struct{}
}

func (r *taskStreamTestRuntime) Read(_ context.Context, req stream.ReadRequest) (stream.Snapshot, error) {
	r.mu.Lock()
	if r.readSnapshot != nil {
		read := r.readSnapshot
		r.mu.Unlock()
		return read(req)
	}
	if len(r.readSnapshots) > 0 {
		index := min(r.readCalls, len(r.readSnapshots)-1)
		r.readCalls++
		snapshot := stream.CloneSnapshot(r.readSnapshots[index])
		r.mu.Unlock()
		return snapshot, nil
	}
	snapshot, ok := r.snapshots[req.Ref.TaskID]
	r.mu.Unlock()
	if !ok {
		return stream.Snapshot{}, errors.New("stream not found")
	}
	return stream.CloneSnapshot(snapshot), nil
}

func (r *taskStreamTestRuntime) Subscribe(ctx context.Context, req stream.SubscribeRequest) iter.Seq2[*stream.Frame, error] {
	return func(yield func(*stream.Frame, error) bool) {
		r.mu.Lock()
		r.lastSubscribeRequest = req
		frames := append([]stream.Frame(nil), r.subscribeFrames...)
		if len(r.subscribeFramesByCall) > 0 {
			if r.subscribeCalls < len(r.subscribeFramesByCall) {
				frames = append([]stream.Frame(nil), r.subscribeFramesByCall[r.subscribeCalls]...)
			} else {
				frames = nil
			}
			r.subscribeCalls++
		}
		r.mu.Unlock()
		if r.subscribeStarted != nil {
			close(r.subscribeStarted)
		}
		for _, frame := range frames {
			cloned := stream.CloneFrame(frame)
			if !yield(&cloned, nil) {
				return
			}
		}
		if len(frames) > 0 {
			return
		}
		<-ctx.Done()
		if r.subscribeStopped != nil {
			close(r.subscribeStopped)
		}
		yield(nil, ctx.Err())
	}
}

func (r *taskStreamTestRuntime) Wait(ctx context.Context, ref stream.Ref) (stream.Snapshot, error) {
	return r.Read(ctx, stream.ReadRequest{Ref: ref})
}

var _ task.Store = (*taskStreamTestStore)(nil)
var _ stream.Service = (*taskStreamTestRuntime)(nil)
