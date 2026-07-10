package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestJournaledToolPersistsLifecycleAndCancellationRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		ctx          func() context.Context
		invoke       func(context.Context, tool.Call) (tool.Result, error)
		wantDurable  []session.ToolExecutionStatus
		wantTerminal session.ToolExecutionStatus
	}{
		{
			name: "success",
			ctx:  context.Background,
			invoke: func(context.Context, tool.Call) (tool.Result, error) {
				return tool.Result{Name: "WRITE", Content: []model.Part{model.NewTextPart("ok")}}, nil
			},
			wantDurable:  []session.ToolExecutionStatus{session.ToolExecutionPrepared, session.ToolExecutionApproved, session.ToolExecutionStarted},
			wantTerminal: session.ToolExecutionSucceeded,
		},
		{
			name: "cancelled",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
				return tool.Result{IsError: true}, ctx.Err()
			},
			wantDurable:  []session.ToolExecutionStatus{session.ToolExecutionPrepared, session.ToolExecutionApproved, session.ToolExecutionStarted, session.ToolExecutionCancelRequested},
			wantTerminal: session.ToolExecutionCancelled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, active := newJournalTestSession(t, "sess-"+tt.name)
			wrapped := journaledTool{
				base:     tool.NamedTool{Def: tool.Definition{Name: "WRITE", EffectClass: tool.EffectNonIdempotent}, Invoke: tt.invoke},
				sessions: service, sessionRef: active.SessionRef, runID: "run-1", turnID: "turn-1", now: func() time.Time { return time.Unix(100, 0) },
			}
			result, _ := wrapped.Call(tt.ctx(), tool.Call{ID: "call-1", Name: "WRITE", Input: []byte(`{"path":"a"}`)})
			loadedEvents, err := service.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatalf("Events() error = %v", err)
			}
			var got []session.ToolExecutionStatus
			for _, event := range loadedEvents {
				if event.Journal != nil && event.Journal.ToolExecution != nil {
					got = append(got, event.Journal.ToolExecution.Status)
				}
			}
			if !reflect.DeepEqual(got, tt.wantDurable) {
				t.Fatalf("journal statuses = %v, want %v", got, tt.wantDurable)
			}
			raw, _ := json.Marshal(result.Metadata[tool.MetadataExecutionJournal])
			var journal session.ExecutionJournalEntry
			_ = json.Unmarshal(raw, &journal)
			if journal.ToolExecution == nil || journal.ToolExecution.Status != tt.wantTerminal {
				t.Fatalf("terminal journal = %#v, want %q", journal, tt.wantTerminal)
			}
		})
	}
}

func TestRuntimeRecoveryMarksStartedExecutionUnknownWithoutCallingTool(t *testing.T) {
	t.Parallel()

	service, active := newJournalTestSession(t, "sess-recover-tool")
	writer := journaledTool{sessions: service, sessionRef: active.SessionRef, now: func() time.Time { return time.Unix(200, 0) }}
	record := session.NormalizeToolExecution(session.ToolExecution{
		Schema:   session.ToolExecutionSchemaVersion,
		Key:      session.ExecutionKey{SessionID: active.SessionID, RunID: "run-old", TurnID: "turn-old", StepID: "call-1", ToolCallID: "call-1"},
		Revision: 1, ToolName: "WRITE", EffectClass: string(tool.EffectNonIdempotent), Status: session.ToolExecutionPrepared,
	})
	if err := writer.appendEntry(context.Background(), session.ToolExecution{}, record, session.ExecutionRecord{}, session.ExecutionRecord{}); err != nil {
		t.Fatalf("append prepared: %v", err)
	}
	for _, status := range []session.ToolExecutionStatus{session.ToolExecutionApproved, session.ToolExecutionStarted} {
		previous := record
		record.Revision++
		record.Status = status
		if err := writer.appendEntry(context.Background(), previous, record, session.ExecutionRecord{}, session.ExecutionRecord{}); err != nil {
			t.Fatalf("append %s: %v", status, err)
		}
	}
	runtime := &Runtime{sessions: service, clock: func() time.Time { return time.Unix(300, 0) }}
	if err := runtime.recoverIncompleteToolExecutions(context.Background(), active.SessionRef); err != nil {
		t.Fatalf("recoverIncompleteToolExecutions() error = %v", err)
	}
	loaded, err := service.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	last := loaded[len(loaded)-1].Journal.ToolExecution
	if last.Status != session.ToolExecutionUnknownOutcome || last.Revision != 4 {
		t.Fatalf("recovered execution = %#v, want UnknownOutcome revision 4", last)
	}
}

func TestJournaledToolPersistsCancelRequestBeforeExecutionTerminates(t *testing.T) {
	t.Parallel()

	service, active := newJournalTestSession(t, "sess-live-cancel")
	started := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	wrapped := journaledTool{
		base: tool.NamedTool{
			Def: tool.Definition{Name: "WRITE", EffectClass: tool.EffectNonIdempotent},
			Invoke: func(context.Context, tool.Call) (tool.Result, error) {
				close(started)
				<-release
				return tool.Result{IsError: true}, context.Canceled
			},
		},
		sessions: service, sessionRef: active.SessionRef, runID: "run-live", turnID: "turn-live", now: func() time.Time { return time.Unix(400, 0) },
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(returned)
		_, _ = wrapped.Call(ctx, tool.Call{ID: "call-live", Name: "WRITE"})
	}()
	<-started
	cancel()

	deadline := time.After(2 * time.Second)
	for {
		events, err := service.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
		if err != nil {
			t.Fatalf("Events() error = %v", err)
		}
		found := false
		for _, event := range events {
			found = found || event.Journal != nil && event.Journal.ToolExecution != nil && event.Journal.ToolExecution.Status == session.ToolExecutionCancelRequested
		}
		if found {
			break
		}
		select {
		case <-deadline:
			t.Fatal("cancel request was not persisted while tool remained active")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	select {
	case <-returned:
		t.Fatal("tool execution terminated before release")
	default:
	}
	close(release)
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("tool execution did not terminate after release")
	}
}

func TestRuntimeCrashWindowRecoversUnknownOutcomeWithoutToolReplay(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	service := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root, SessionIDGenerator: func() string { return "sess-crash-window" }}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	failing := &invalidAppendSessionService{Service: service, failType: session.EventTypeToolResult}
	allow := staticPolicyRegistry{mode: policy.NamedMode{
		ID: "allow",
		Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
			return policy.Decision{Action: policy.ActionAllow}, nil
		},
	}}
	var calls int
	writeTool := tool.NamedTool{
		Def: tool.Definition{Name: "ECHO", EffectClass: tool.EffectNonIdempotent, InputSchema: map[string]any{"type": "object"}},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			calls++
			return tool.Result{ID: call.ID, Name: call.Name, Content: []model.Part{model.NewJSONPart([]byte(`{"value":"written"}`))}}, nil
		},
	}
	first, err := New(Config{
		Sessions: failing, AgentFactory: chat.Factory{}, PolicyRegistry: allow, DefaultPolicyMode: "allow",
		RunIDGenerator: func() string { return "run-crashed" },
	})
	if err != nil {
		t.Fatalf("New(first) error = %v", err)
	}
	run, err := first.Run(context.Background(), agent.RunRequest{
		SessionRef: active.SessionRef, Input: "write once",
		AgentSpec: agent.AgentSpec{Name: "chat", Model: &toolLoopRuntimeModel{}, Tools: []tool.Tool{writeTool}},
	})
	if err != nil {
		t.Fatalf("Run(first) error = %v", err)
	}
	if _, runErr := drainRunnerEvents(t, run.Handle); runErr == nil {
		t.Fatal("first run error = nil, want forced tool-result persistence failure")
	}
	if calls != 1 {
		t.Fatalf("tool calls after crash window = %d, want 1", calls)
	}

	reopened := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root}))
	second, err := New(Config{
		Sessions: reopened, AgentFactory: chat.Factory{},
		RunIDGenerator: func() string { return "run-recovery" },
	})
	if err != nil {
		t.Fatalf("New(second) error = %v", err)
	}
	run, err = second.Run(context.Background(), agent.RunRequest{
		SessionRef: active.SessionRef, Input: "recover",
		AgentSpec: agent.AgentSpec{Name: "chat", Model: staticModel{text: "recovered"}},
	})
	if err != nil {
		t.Fatalf("Run(second) error = %v", err)
	}
	if _, runErr := drainRunnerEvents(t, run.Handle); runErr != nil {
		t.Fatalf("second runner error = %v", runErr)
	}
	if calls != 1 {
		t.Fatalf("tool calls after recovery = %d, want no replay", calls)
	}

	events, err := reopened.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	var toolStatuses []session.ToolExecutionStatus
	var stepStatuses []session.ExecutionStatus
	for _, event := range events {
		if event.Journal == nil {
			continue
		}
		if event.Journal.ToolExecution != nil {
			toolStatuses = append(toolStatuses, event.Journal.ToolExecution.Status)
		}
		if event.Journal.Execution != nil && event.Journal.Execution.Kind == session.JournalKindStep {
			stepStatuses = append(stepStatuses, event.Journal.Execution.Status)
		}
	}
	if want := []session.ToolExecutionStatus{session.ToolExecutionPrepared, session.ToolExecutionApproved, session.ToolExecutionStarted, session.ToolExecutionUnknownOutcome}; !reflect.DeepEqual(toolStatuses, want) {
		t.Fatalf("tool execution journal = %v, want %v", toolStatuses, want)
	}
	if want := []session.ExecutionStatus{session.ExecutionPrepared, session.ExecutionStarted, session.ExecutionUnknownOutcome}; !reflect.DeepEqual(stepStatuses, want) {
		t.Fatalf("step journal = %v, want %v", stepStatuses, want)
	}
}

func newJournalTestSession(t *testing.T, id string) (*inmemory.Service, session.Session) {
	t.Helper()
	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{SessionIDGenerator: func() string { return id }}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	return service, active
}
