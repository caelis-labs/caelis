package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestIndependentJSExecRegistersTaskWithoutShell(t *testing.T) {
	store, active, _ := codeModeStore(t)
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "sdk-script", Name: codemode.ToolName, Args: `{"code":"return 42","yield_time_ms":0}`}}
		}
		return nil
	}}
	runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
	for _, req := range driver.requests {
		for _, def := range req.Tools {
			if def.Function != nil && def.Function.Name == tasktool.ToolName {
				return
			}
		}
	}
	t.Fatal("SDK js_exec yields an original Task handle but no Task tool was registered without RunCommand")
}

type independentFailScriptTerminalStore struct {
	taskapi.Store
	marker error
}

func (s independentFailScriptTerminalStore) Put(ctx context.Context, req taskapi.PutRequest) (*taskapi.Entry, error) {
	if req.Entry != nil && req.Entry.Kind == taskapi.KindCodeMode && !req.Entry.Running {
		return nil, s.marker
	}
	return s.Store.(taskapi.CASStore).Put(ctx, req)
}

func TestIndependentJSExecYieldDoesNotHideTerminalPersistenceFailure(t *testing.T) {
	store, active, _ := codeModeStore(t)
	marker := errors.New("injected-script-terminal-commit-failure")
	start, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	hold := tool.NamedTool{Def: tool.Definition{Name: "Hold", InputSchema: map[string]any{"type": "object"}}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
		close(start)
		select {
		case <-release:
			return tool.Result{}, nil
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	}}
	driver := &mcpScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return []model.ToolCall{{ID: "yielded", Name: codemode.ToolName, Args: `{"code":"await tools.call('Hold',{});return {done:true}","yield_time_ms":0}`}}
		case 1:
			select {
			case <-start:
			case <-time.After(3 * time.Second):
				t.Error("worker did not reach original Host")
				return nil
			}
			once.Do(func() { close(release) })
			raw, _ := json.Marshal(map[string]any{"action": "wait", "handle": jsExecTaskHandle(req)})
			return []model.ToolCall{{ID: "observe-terminal", Name: "Task", Args: string(raw)}}
		}
		return nil
	}}
	base := sessionfile.NewTaskStore(store)
	rt, err := New(Config{Sessions: store, TaskStore: independentFailScriptTerminalStore{Store: base, marker: marker}, AgentFactory: chat.Factory{}, PolicyRegistry: mcpFixturePolicy(), DefaultPolicyMode: "mcp-test"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "terminal persistence fault fixture", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{hold, tasktool.New(), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}})
	if err != nil {
		t.Fatal(err)
	}
	runErr := run.Handle.WaitCompletion(t.Context())
	entries, err := base.ListSession(t.Context(), active.SessionRef)
	if err != nil || len(entries) != 1 {
		t.Fatalf("durable entries=%d err=%v", len(entries), err)
	}
	snapshot, readErr := rt.tasks.Read(t.Context(), active.SessionRef, taskapi.ControlRequest{TaskID: entries[0].TaskID, Principal: session.ActorKindTool, Source: "agent_tool"})
	events, _ := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	status := "absent"
	for _, event := range events {
		if event.Tool != nil && event.Tool.ID == "observe-terminal" && event.Type == session.EventTypeToolResult {
			status = event.Tool.Status
		}
	}
	t.Logf("runErr=%v readErr=%v Task.wait=%s memory=%s/%v durable=%s/%v", runErr, readErr, status, snapshot.State, snapshot.Running, entries[0].State, entries[0].Running)
	if runErr == nil && readErr == nil && status == "completed" && snapshot.State == taskapi.StateCompleted && entries[0].Running {
		t.Fatal("failed terminal commit was hidden: original Run and Task.wait both reported success while durable Task remained running")
	}
}

func TestIndependentJSExecReadWhileRunningKeepsTerminalCAS(t *testing.T) {
	store, active, _ := codeModeStore(t)
	start, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	hold := tool.NamedTool{Def: tool.Definition{Name: "Hold", InputSchema: map[string]any{"type": "object"}}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
		close(start)
		select {
		case <-release:
			return tool.Result{}, nil
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	}}
	var handle string
	driver := &mcpScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return []model.ToolCall{{ID: "real-poll", Name: codemode.ToolName, Args: `{"code":"await tools.call('Hold',{});return 42","yield_time_ms":0}`}}
		case 1:
			select {
			case <-start:
			case <-time.After(3 * time.Second):
				t.Error("worker did not reach original Host")
				return nil
			}
			handle = jsExecTaskHandle(req)
			return []model.ToolCall{{ID: "read-running", Name: "Task", Args: `{"action":"read","handle":"` + handle + `"}`}}
		case 2:
			once.Do(func() { close(release) })
			return []model.ToolCall{{ID: "wait-terminal", Name: "Task", Args: `{"action":"wait","handle":"` + handle + `"}`}}
		}
		return nil
	}}
	_, rt := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{hold, tasktool.New(), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
	entries, err := rt.tasks.store.ListSession(t.Context(), active.SessionRef)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
	rt.tasks.mu.RLock()
	task := rt.tasks.codeTasks[entries[0].TaskID]
	rt.tasks.mu.RUnlock()
	task.mu.Lock()
	state, terminalErr := task.entry.State, task.err
	task.mu.Unlock()
	t.Logf("memory=%s durable=%s/%v revision=%d terminalErr=%v", state, entries[0].State, entries[0].Running, entries[0].Revision, terminalErr)
	if entries[0].Running || entries[0].State != taskapi.StateCompleted || terminalErr != nil {
		t.Fatal("ordinary Task.read while script was running invalidated its final CAS; no storage fault was injected")
	}
}
