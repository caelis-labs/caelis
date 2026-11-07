package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

// Repeated real observations must not manufacture durable revisions, including
// after a nonzero model-output cursor has already committed.
func TestJSExecCanonicalCursorCommitsOnlyAdvancement(t *testing.T) {
	store, active, _ := codeModeStore(t)
	tasks := sessionfile.NewTaskStore(store)
	var effects atomic.Int32
	effect := tool.NamedTool{Def: tool.Definition{Name: "Effect", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { effects.Add(1); return tool.Result{}, nil }}
	var handle string
	var revision uint64
	driver := &mcpScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: `{"code":"await tools.call('Effect',{});return 42"}`}}
		case 1:
			handle = jsExecTaskHandle(req)
			entries, err := tasks.ListSession(t.Context(), active.SessionRef)
			if err != nil || len(entries) != 1 {
				t.Fatalf("entries=%d %v", len(entries), err)
			}
			cursor, _ := taskInt64Value(entries[0].Metadata["model_output_cursor"])
			if cursor <= 0 || entries[0].State != taskapi.StateCompleted {
				t.Fatalf("selected output checkpoint missing: %+v", entries[0])
			}
			revision = entries[0].Revision
			return []model.ToolCall{{ID: "repeat", Name: "Task", Args: `{"action":"read","handle":"` + handle + `"}`}}
		case 2:
			entries, err := tasks.ListSession(t.Context(), active.SessionRef)
			if err != nil || entries[0].Revision != revision {
				t.Fatalf("unchanged nonzero cursor wrote revision: %+v %v", entries, err)
			}
		}
		return nil
	}}
	_, rt := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{effect, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
	entries, _ := tasks.ListSession(t.Context(), active.SessionRef)
	rt.tasks.mu.RLock()
	live := rt.tasks.codeTasks[entries[0].TaskID]
	rt.tasks.mu.RUnlock()
	live.mu.Lock()
	memory := taskapi.CloneEntry(live.entry)
	live.mu.Unlock()
	memoryCursor, _ := taskInt64Value(memory.Metadata["model_output_cursor"])
	durableCursor, _ := taskInt64Value(entries[0].Metadata["model_output_cursor"])
	if effects.Load() != 1 || memory.Revision != entries[0].Revision || memoryCursor != durableCursor {
		t.Fatalf("effect/revision/frontier mismatch: effects=%d memory=%+v durable=%+v", effects.Load(), memory, entries[0])
	}
	assertSubagentSagaModelRoundTrip(t, store, active.SessionRef)
}

func TestJSExecTerminalCommitFailureReachesProducerAndObservers(t *testing.T) {
	store, active, _ := codeModeStore(t)
	base := sessionfile.NewTaskStore(store)
	marker := errors.New("terminal-durability-failure")
	var effects atomic.Int32
	start, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	effect := tool.NamedTool{Def: tool.Definition{Name: "Effect", InputSchema: map[string]any{"type": "object"}}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
		effects.Add(1)
		close(start)
		select {
		case <-release:
			return tool.Result{Content: []model.Part{model.NewTextPart("original-effect-receipt")}}, nil
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	}}
	trace := &recordingProducerLifecycle{}
	driver := &mcpScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: `{"code":"await tools.call('Effect',{});return 42","yield_time_ms":0}`}}
		}
		if i == 1 {
			select {
			case <-start:
			case <-time.After(3 * time.Second):
				t.Fatal("real JS effect did not start")
			}
			once.Do(func() { close(release) })
			args, _ := json.Marshal(map[string]any{"action": "wait", "handle": jsExecTaskHandle(req)})
			return []model.ToolCall{{ID: "wait", Name: "Task", Args: string(args)}}
		}
		return nil
	}}
	rt, err := New(Config{Sessions: store, TaskStore: independentFailScriptTerminalStore{Store: base, marker: marker}, TaskOutput: trace, AgentFactory: chat.Factory{}, PolicyRegistry: mcpFixturePolicy(), DefaultPolicyMode: "mcp-test"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "commit failure", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{effect, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = run.Handle.WaitCompletion(t.Context()); !errors.Is(err, marker) {
		t.Fatalf("Run hid original commit failure: %v", err)
	}
	entries, err := base.ListSession(t.Context(), active.SessionRef)
	if err != nil || len(entries) != 1 {
		t.Fatalf("durable entries=%+v %v", entries, err)
	}
	snapshot, readErr := rt.tasks.Read(t.Context(), active.SessionRef, taskapi.ControlRequest{TaskID: entries[0].TaskID, Principal: session.ActorKindTool, Source: "agent_tool"})
	if !errors.Is(readErr, marker) || snapshot.State == taskapi.StateCompleted || !entries[0].Running {
		t.Fatalf("observation hid failed durability: %+v %v durable=%+v", snapshot, readErr, entries[0])
	}
	if len(trace.events) != 1 || !trace.events[0].ProducerClosed || trace.events[0].State != string(taskapi.StateUnknownOutcome) || !strings.Contains(trace.events[0].Text, marker.Error()) {
		t.Fatalf("producer output falsely completed: %+v", trace.events)
	}
	events, _ := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	receipts := 0
	failedWait := false
	for _, e := range events {
		if e.Tool != nil && e.Tool.ID == "wait" && e.Type == session.EventTypeToolResult {
			failedWait = e.Tool.Status == "failed"
		}
		if e.Journal != nil && e.Journal.ToolExecution != nil {
			r := e.Journal.ToolExecution
			if r.ToolName == "Effect" && r.Status == session.ToolExecutionSucceeded {
				receipts++
			}
		}
	}
	if effects.Load() != 1 || receipts != 1 || !failedWait {
		t.Fatalf("effect replay/receipt/Task.wait mismatch: effects=%d receipts=%d failedWait=%v", effects.Load(), receipts, failedWait)
	}
	assertSubagentSagaModelRoundTrip(t, store, active.SessionRef)
	// A fresh Runtime observes the unresolved durable producer; it never reruns JS.
	restarted, err := New(Config{Sessions: store, TaskStore: base, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = restarted.tasks.Read(t.Context(), active.SessionRef, taskapi.ControlRequest{TaskID: entries[0].TaskID, Principal: session.ActorKindController, Source: "control"})
	if err == nil || effects.Load() != 1 {
		t.Fatalf("unresolved producer was resumed or presented complete: %v effects=%d", err, effects.Load())
	}
}

func TestJSExecSDKOnlyDeclarationCanWaitOriginalTask(t *testing.T) {
	store, active, _ := codeModeStore(t)
	var original string
	driver := &mcpScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "sdk-only", Name: codemode.ToolName, Args: `{"code":"return 42","yield_time_ms":0}`}}
		}
		if i == 1 {
			original = jsExecTaskHandle(req)
			if original == "" {
				t.Fatal("SDK yielded no original handle")
			}
			args, _ := json.Marshal(map[string]any{"action": "wait", "handle": original})
			return []model.ToolCall{{ID: "sdk-wait", Name: "Task", Args: string(args)}}
		}
		return nil
	}}
	events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
	for _, req := range driver.requests {
		for _, def := range req.Tools {
			if def.Function != nil && def.Function.Name != "Task" && def.Function.Name != codemode.ToolName {
				t.Fatalf("SDK injected extra authority: %s", def.Function.Name)
			}
		}
	}
	for _, event := range events {
		if event.Type == session.EventTypeToolResult && event.Tool != nil && event.Tool.ID == "sdk-wait" {
			if event.Tool.Status != "completed" || event.Tool.Output["state"] != "completed" || event.Tool.Output["handle"] != original {
				t.Fatalf("SDK Task wait lost completion/identity: %+v", event.Tool)
			}
			return
		}
	}
	t.Fatal("SDK Task.wait did not complete through Runtime")
}
