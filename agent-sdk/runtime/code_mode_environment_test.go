package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestCodeModeEnvironmentAcrossRunsResetAndRelease(t *testing.T) {
	store, active, root := codeModeStore(t)
	rt, err := New(Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store), AgentFactory: chat.Factory{}, PolicyRegistry: mcpFixturePolicy(), DefaultPolicyMode: "mcp-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.ReleaseCodeModeEnvironments() })
	tools := []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}
	invocation := 0
	execute := func(ref session.SessionRef, source string, reset bool) string {
		t.Helper()
		invocation++
		id := fmt.Sprintf("script-%d", invocation)
		args, _ := json.Marshal(map[string]any{"code": source})
		if reset {
			args = []byte(`{"reset":true}`)
		}
		driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
			if i == 0 {
				return []model.ToolCall{{ID: id, Name: codemode.ToolName, Args: string(args)}}
			}
			return nil
		}}
		run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: ref, Input: "fixture", AgentSpec: agent.AgentSpec{Model: driver, Tools: tools}})
		if err != nil {
			t.Fatal(err)
		}
		if err = run.Handle.WaitCompletion(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(driver.requests) != 2 {
			t.Fatalf("model requests=%d", len(driver.requests))
		}
		result := errorDeliveryModelResult(t, driver.requests[1], id)
		if result.IsError {
			t.Fatalf("script failed: %s", errorDeliveryJSON(t, result.Content))
		}
		return string(errorDeliveryJSON(t, result.Content))
	}
	firstState := execute(active.SessionRef, `let retained=40;return runtime.state();`, false)
	if out := execute(active.SessionRef, `retained+=2;return retained;`, false); !strings.Contains(out, "42") {
		t.Fatal(out)
	}
	other, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "fixture", Workspace: session.WorkspaceRef{Key: "fixture", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	if out := execute(other.SessionRef, `return typeof retained;`, false); !strings.Contains(out, "undefined") {
		t.Fatal(out)
	}
	before := execute(active.SessionRef, `return runtime.state();`, false)
	execute(active.SessionRef, "", true)
	after := execute(active.SessionRef, `return {state:runtime.state(),value:typeof retained};`, false)
	if !strings.Contains(after, "undefined") || generationOf(t, before) != generationOf(t, firstState) || generationOf(t, before) == generationOf(t, after) {
		t.Fatalf("reset did not replace environment: %s %s", before, after)
	}
	execute(active.SessionRef, `let resumed=1;return resumed;`, false)
	if err := rt.ReleaseCodeModeEnvironments(); err != nil {
		t.Fatal(err)
	}
	if out := execute(active.SessionRef, `return typeof resumed;`, false); !strings.Contains(out, "undefined") {
		t.Fatal(out)
	}
}

func TestCodeModeResetDoesNotWaitForOldHostOrAdmitOldReply(t *testing.T) {
	store, active, _ := codeModeStore(t)
	entered, release := make(chan struct{}), make(chan struct{})
	old := tool.NamedTool{Def: tool.Definition{Name: "Old", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		close(entered)
		<-release
		return tool.Result{Content: []tool.ContentBlock{tool.Text("old-generation-result")}}, nil
	}}
	var oldHandle string
	driver := &mcpScriptModel{steps: func(i int, request *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return []model.ToolCall{{ID: "old", Name: codemode.ToolName, Args: `{"code":"let oldBody=await Old({});return oldBody;","yield_time_ms":0}`}}
		case 1:
			oldHandle = jsExecTaskHandle(request)
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Error("old VM never entered Host")
			}
			return []model.ToolCall{{ID: "busy", Name: codemode.ToolName, Args: `{"code":"return 1;"}`}}
		case 2:
			return []model.ToolCall{{ID: "reset", Name: codemode.ToolName, Args: `{"reset":true}`}}
		case 3:
			return []model.ToolCall{{ID: "new", Name: codemode.ToolName, Args: `{"code":"let newMarker=42;return {state:runtime.state(),old:typeof oldBody};"}`}}
		case 4:
			close(release)
			raw, _ := json.Marshal(map[string]any{"action": "wait", "handle": oldHandle})
			return []model.ToolCall{{ID: "wait-old", Name: "Task", Args: string(raw)}}
		case 5:
			return []model.ToolCall{{ID: "after-old", Name: codemode.ToolName, Args: `{"code":"return {marker:newMarker,old:typeof oldBody};"}`}}
		}
		return nil
	}}
	runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{old, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
	if len(driver.requests) != 7 {
		t.Fatalf("requests=%d", len(driver.requests))
	}
	busy := errorDeliveryModelResult(t, driver.requests[2], "busy")
	if !busy.IsError || !strings.Contains(string(errorDeliveryJSON(t, busy.Content)), "busy") || !strings.Contains(string(errorDeliveryJSON(t, busy.Content)), "reset:true") {
		t.Fatalf("overlap not rejected: %+v; payload=%s", busy, errorDeliveryJSON(t, busy.Content))
	}
	result := errorDeliveryModelResult(t, driver.requests[4], "new")
	raw := string(errorDeliveryJSON(t, result.Content))
	if result.IsError || !strings.Contains(raw, "undefined") || strings.Contains(raw, "old-generation-result") {
		t.Fatalf("new environment waited for or received old callback: %s", raw)
	}
	final := errorDeliveryModelResult(t, driver.requests[6], "after-old")
	if final.IsError || !strings.Contains(string(errorDeliveryJSON(t, final.Content)), `"marker":42`) {
		t.Fatalf("late old callback invalidated new environment: %+v", final)
	}

}

func TestCodeModeRetentionBudgetAccumulatesAcrossScriptsAndReset(t *testing.T) {
	r := &Runtime{}
	def := tool.Definition{}
	_, first, err := r.acquireCodeMode(t.Context(), session.SessionRef{SessionID: "one"}, &gojaexec.Executor{})
	if err != nil {
		t.Fatal(err)
	}
	limits := codemode.DefaultLimits()
	limits.RetainedBytes = 6
	h := &codeModeHost{limits: limits, codeModeResults: first.environment.results}
	if _, err = h.project("one", tool.Result{Content: []tool.ContentBlock{tool.Text("1234")}}, codemode.OutcomeSucceeded, def); err != nil {
		t.Fatal(err)
	}
	first.release()
	_, second, err := r.acquireCodeMode(t.Context(), session.SessionRef{SessionID: "one"}, &gojaexec.Executor{})
	if err != nil {
		t.Fatal(err)
	}
	h = &codeModeHost{limits: limits, codeModeResults: second.environment.results}
	if _, err = h.project("two", tool.Result{Content: []tool.ContentBlock{tool.Text("12")}}, codemode.OutcomeSucceeded, def); err != nil {
		t.Fatal(err)
	}
	if _, err = h.project("overflow", tool.Result{Content: []tool.ContentBlock{tool.Text("1")}}, codemode.OutcomeSucceeded, def); err == nil {
		t.Fatal("cumulative budget bypassed")
	}
	second.release()
	r.resetCodeMode(session.SessionRef{SessionID: "one"}, &gojaexec.Executor{})
	_, third, err := r.acquireCodeMode(t.Context(), session.SessionRef{SessionID: "one"}, &gojaexec.Executor{})
	if err != nil {
		t.Fatal(err)
	}
	if third.environment.results.retained != 0 || len(third.environment.results.replies) != 0 {
		t.Fatal("reset retained old result storage")
	}
	third.release()
	_ = r.ReleaseCodeModeEnvironments()
}

func TestCodeModeResetInterruptsUnresponsiveVM(t *testing.T) {
	for _, code := range []string{`await Entered({});while(true){}`, `await Entered({});return {get stuck(){while(true){}}};`} {
		t.Run(code, func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			entered := make(chan struct{})
			signal := tool.NamedTool{Def: tool.Definition{Name: "Entered", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { close(entered); return tool.Result{}, nil }}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				switch i {
				case 0:
					raw, _ := json.Marshal(map[string]any{"code": code, "yield_time_ms": 0})
					return []model.ToolCall{{ID: "stuck", Name: codemode.ToolName, Args: string(raw)}}
				case 1:
					select {
					case <-entered:
					case <-time.After(5 * time.Second):
						t.Error("VM never entered script")
					}
					return []model.ToolCall{{ID: "reset", Name: codemode.ToolName, Args: `{"reset":true}`}}
				case 2:
					return []model.ToolCall{{ID: "replacement", Name: codemode.ToolName, Args: `{"code":"let healthy=42;return {healthy,state:runtime.state()};"}`}}
				}
				return nil
			}}
			_, rt := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{signal, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
			result := errorDeliveryModelResult(t, driver.requests[len(driver.requests)-1], "replacement")
			if result.IsError || !strings.Contains(string(errorDeliveryJSON(t, result.Content)), `"healthy":42`) {
				t.Fatalf("replacement VM unavailable: %+v", result)
			}
			entries, err := rt.tasks.store.ListSession(t.Context(), active.SessionRef)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 2 {
				t.Fatalf("Task count=%d; reset must not replay/create a script Task", len(entries))
			}
			for _, entry := range entries {
				if entry.Running {
					t.Fatalf("old/replacement Task still running: %+v", entry)
				}
			}
		})
	}
}

func generationOf(t *testing.T, raw string) string {
	t.Helper()
	var parts []model.Part
	if err := json.Unmarshal([]byte(raw), &parts); err != nil {
		t.Fatal(err)
	}
	for _, part := range parts {
		if part.JSON == nil {
			continue
		}
		var value struct {
			Generation string                  `json:"generation"`
			State      codemode.ExecutionState `json:"state"`
		}
		if err := json.Unmarshal(part.JSON.Value, &value); err != nil {
			continue
		}
		if value.Generation != "" {
			return value.Generation
		}
		if value.State.Generation != "" {
			return value.State.Generation
		}
	}
	t.Fatalf("generation missing: %s", raw)
	return ""
}

func TestCodeModeRealRetentionAcrossScriptsAndFailedProjections(t *testing.T) {
	store, active, _ := codeModeStore(t)
	var calls int
	bytesTool := tool.NamedTool{Def: tool.Definition{Name: "Bytes", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"size": map[string]any{"type": "integer"}}}}, Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
		var args struct {
			Size int `json:"size"`
		}
		_ = json.Unmarshal(call.Input, &args)
		calls++
		return tool.Result{Content: []tool.ContentBlock{tool.Text(strings.Repeat("x", args.Size))}}, nil
	}}
	sources := []string{`let firstBody=await Bytes({size:4});return firstBody.text;`, `return await Bytes({size:2});`, `let rejected=0;for(let i=0;i<5;i++){try{await Bytes({size:1})}catch(e){rejected++;}}return rejected;`, `return firstBody;`, "", `return await Bytes({size:6});`}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i >= len(sources) {
			return nil
		}
		raw, _ := json.Marshal(map[string]any{"code": sources[i]})
		if i == 4 {
			raw = []byte(`{"reset":true}`)
		}
		return []model.ToolCall{{ID: fmt.Sprintf("retention-%d", i), Name: codemode.ToolName, Args: string(raw)}}
	}}
	_, rt := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{bytesTool, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Limits: codemode.Limits{RetainedBytes: 6}}}}, nil, nil)
	for index, want := range map[int]string{0: "xxxx", 1: "xx", 2: "5", 3: "xxxx", 5: "xxxxxx"} {
		result := errorDeliveryModelResult(t, driver.requests[index+1], fmt.Sprintf("retention-%d", index))
		if result.IsError != (index == 2) || !strings.Contains(string(errorDeliveryJSON(t, result.Content)), want) {
			t.Fatalf("script %d: %+v", index, result)
		}
	}
	if calls != 8 {
		t.Fatalf("actual tool effects=%d; want 8 without replay", calls)
	}
	rt.codeModeMu.Lock()
	env := rt.codeModeEnvironments[session.NormalizeSessionRef(active.SessionRef)]
	rt.codeModeMu.Unlock()
	env.results.mu.Lock()
	defer env.results.mu.Unlock()
	if env.results.retained != 6 || len(env.results.replies) != 1 || len(env.results.handles) != 0 {
		t.Fatalf("reset generation retained bytes=%d replies=%d handles=%d", env.results.retained, len(env.results.replies), len(env.results.handles))
	}
}
