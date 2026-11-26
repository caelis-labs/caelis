package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Replaces comparison-only command-in-script approval tests with real JS and
// an allowed filesystem effect, keeping the original Runtime approval/Journal.
func TestJSExecYieldApprovalGuardAndTermination(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"allow", "generation", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			sandbox, err := host.New(host.Config{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			defer sandbox.Close()
			write, err := filesystem.NewWrite(sandbox)
			if err != nil {
				t.Fatal(err)
			}
			asked, approve := make(chan struct{}), make(chan struct{})
			var revision sync.RWMutex
			var stale atomic.Bool
			var effects, after, approvals atomic.Int32
			counted := tool.NamedTool{Def: write.Definition(), Invoke: func(ctx context.Context, c tool.Call) (tool.Result, error) {
				if !revision.TryLock() {
					t.Error("configuration guard held during filesystem I/O")
				} else {
					revision.Unlock()
				}
				effects.Add(1)
				return write.Call(ctx, c)
			}}
			later := tool.NamedTool{Def: tool.Definition{Name: "After", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { after.Add(1); return tool.Result{}, nil }}

			registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(_ context.Context, in policy.ToolContext) (policy.Decision, error) {
				if in.Tool.Name == "Write" {
					return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{}}, nil
				}
				return policy.Decision{Action: policy.ActionAllow}, nil
			}}}
			requester := approvalRequesterFunc(func(ctx context.Context, req approval.Request) (approval.Response, error) {
				approvals.Add(1)
				if req.Tool.Name != "Write" || req.Parent == nil || req.Parent.ID != "script" {
					t.Error("approval lost original child identity")
				}
				if !revision.TryLock() {
					t.Error("configuration guard held during approval")
				} else {
					revision.Unlock()
				}
				close(asked)
				select {
				case <-approve:
				case <-ctx.Done():
				}
				// Deliberately race a late approval against invalidation; the original
				// admission chain, not a second approval owner, must still prevent dispatch.
				return approval.Response{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
			})
			var handle string
			driver := &mcpScriptModel{steps: func(i int, r *model.Request) []model.ToolCall {
				switch i {
				case 0:
					return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: `{"code":"await tools.call('Write',{path:'effect.txt',content:'once'});await tools.call('After',{});return {done:true};","yield_time_ms":0}`}}
				case 1:
					select {
					case <-asked:
					case <-time.After(4 * time.Second):
						t.Error("real JS did not reach pending approval")
						return nil
					}
					handle = jsExecTaskHandle(r)
					if handle == "" {
						t.Error("missing original yielded Task")
						return nil
					}

					action := "wait"
					if mode == "cancel" {
						action = "cancel"
					} else {
						if mode == "generation" {
							revision.Lock()
							stale.Store(true)
							revision.Unlock()
						}
						close(approve)
					}
					raw, _ := json.Marshal(map[string]any{"action": action, "handle": handle})
					return []model.ToolCall{{ID: "observe", Name: "Task", Args: string(raw)}}
				}
				return nil
			}}
			tools := []tool.Tool{counted, later, tasktool.New(), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Sandbox: sandbox}}
			spec := agent.AgentSpec{ResolveModelRequest: func(context.Context) (agent.ModelRequestSnapshot, error) {
				return agent.ModelRequestSnapshot{Model: driver, Tools: tools, Validate: func(context.Context) (func(), error) {
					revision.RLock()
					if stale.Load() {
						revision.RUnlock()
						return nil, tool.NewError(tool.ErrorCodeStaleRevision, "generation replaced")
					}
					return revision.RUnlock, nil
				}}, nil
			}}
			_, rt := runCodeMode(t, store, active, spec, registry, requester)
			want := int32(0)
			if mode == "allow" {
				want = 1
			}
			data, fileErr := os.ReadFile(filepath.Join(root, "effect.txt"))
			if effects.Load() != want || after.Load() != want || approvals.Load() != 1 {
				t.Fatalf("effects=%d after=%d approvals=%d", effects.Load(), after.Load(), approvals.Load())
			}
			if want == 1 && (fileErr != nil || string(data) != "once") {
				t.Fatalf("actual filesystem effect=%q err=%v", data, fileErr)
			}
			if want == 0 && !os.IsNotExist(fileErr) {
				t.Fatalf("unexpected filesystem effect: %q %v", data, fileErr)
			}
			entries, _ := rt.tasks.store.ListSession(t.Context(), active.SessionRef)
			if len(entries) != 1 || entries[0].Running {
				t.Fatalf("original Task did not terminate: %+v", entries)
			}
			events, _ := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			starts := 0
			for _, e := range events {
				if e.Type == session.EventTypeToolResult && e.Tool != nil {
					r := e.Tool
					if r.Name == "Write" && r.Status == "completed" {
						starts++
						if r.Parent == nil || r.Parent.CallID != "script" {
							t.Fatal("receipt lost parent")
						}
					}
				}
			}
			if starts != int(want) {
				t.Fatalf("original durable started claims=%d want=%d", starts, want)
			}
		})
	}
}

func TestIndependentJSExecRegistersTaskWithoutShell(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	rt, err := New(Config{Sessions: store, TaskStore: independentFailScriptTerminalStore{Store: base, marker: marker}, AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPolicy: mcpFixturePolicy()}, DefaultPolicyMode: "mcp-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.ReleaseCodeModeEnvironments() })
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
	t.Parallel()
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
			return []model.ToolCall{{ID: "read-running", Name: "Task", Args: `{"action":"wait","yield_time_ms":0,"handle":"` + handle + `"}`}}
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

func TestCodeModeTaskHandleResolvesOnlyAtHandleBoundary(t *testing.T) {
	t.Parallel()
	active := session.SessionRef{SessionID: "session"}
	tasks := newTaskRuntime(nil, nil)
	tasks.codeTasks = map[string]*codeModeTask{
		"task-id": {
			sessionRef: active,
			handle:     "script",
			entry: &taskapi.Entry{
				TaskID: "task-id", Handle: "script", Session: active,
				Kind: taskapi.KindCodeMode, State: taskapi.StateCompleted,
			},
		},
	}
	req := taskapi.ControlRequest{TaskID: "script", Principal: session.ActorKindUser}
	if _, err := tasks.Read(t.Context(), active, req); err == nil {
		t.Fatal("TaskID lookup accepted a public handle")
	}
	identity, err := tasks.resolveTaskHandle(t.Context(), active, "@script")
	if err != nil {
		t.Fatal(err)
	}
	req.TaskID = identity.taskID
	snapshot, err := tasks.Read(t.Context(), active, req)
	if err != nil || snapshot.Ref.TaskID != "task-id" || snapshot.Handle != "script" {
		t.Fatalf("Read(resolved TaskID) = %#v, %v, want original script task", snapshot, err)
	}
}

func TestLegacyScriptReceiptAllowsNextEffectAndModelStep(t *testing.T) {
	t.Parallel()
	store, active, _ := codeModeStore(t)
	_, err := sessionfile.NewTaskStore(store).Put(t.Context(), taskapi.PutRequest{Entry: &taskapi.Entry{
		TaskID: "finished", Handle: "script", Kind: taskapi.KindCodeMode, State: taskapi.StateCompleted,
		Session: active.SessionRef, Result: map[string]any{"tool_result_json": `{"turn_complete":true}`},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var effects atomic.Int32
	sibling := tool.NamedTool{Def: tool.Definition{Name: "Effect", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		effects.Add(1)
		return tool.Result{}, nil
	}}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{
				{ID: "collect", Name: tasktool.ToolName, Args: `{"action":"wait","handle":"script"}`},
				{ID: "later", Name: "Effect", Args: `{}`},
			}
		}
		return nil
	}}
	events, rt := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{tasktool.New(), sibling}}, nil, nil)
	if len(driver.requests) != 2 || effects.Load() != 1 {
		t.Fatalf("model requests = %d, sibling effects = %d", len(driver.requests), effects.Load())
	}
	control, err := rt.tasks.Cancel(t.Context(), active.SessionRef, taskapi.ControlRequest{
		TaskID: "finished", Principal: session.ActorKindController, Source: "control",
	})
	if err != nil || control.Result["tool_result_json"] != `{"turn_complete":true}` {
		t.Fatalf("controller cancel lost the previously visible result: %+v %v", control, err)
	}
	for _, e := range events {
		if e.Type == session.EventTypeToolResult && e.Tool != nil && e.Tool.ID == "collect" && e.Tool.Status == "completed" {
			return
		}
	}
	t.Fatal("missing terminal Task receipt")
}

func TestLiveScriptTaskWaitWithCancelReturnsSelectedResult(t *testing.T) {
	t.Parallel()
	store, active, _ := codeModeStore(t)
	finish, committed, cancelReturned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	markCommitted := sync.OnceFunc(func() { close(committed) })
	const receipt = "selected-terminal-script-receipt"
	terminal := tool.NamedTool{Def: tool.Definition{Name: "Complete"}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{Content: []tool.ContentBlock{tool.Text(receipt)}}, nil
	}}
	script := &codemode.Tool{Name: codemode.ToolName, Executor: codeOutputExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (codemode.Output, error) {
		select {
		case <-finish:
		case <-ctx.Done():
			return codemode.Output{}, ctx.Err()
		}
		reply, err := host.Call(ctx, "Complete", []byte(`{}`))
		return codemode.Output{Reply: true, CallID: reply.CallID}, err
	})}
	var earlier, later atomic.Int32
	effect := tool.NamedTool{Def: tool.Definition{Name: "Effect"}, Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
		if call.ID == "before" {
			earlier.Add(1)
		} else {
			later.Add(1)
		}
		return tool.Result{}, nil
	}}
	registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(ctx context.Context, in policy.ToolContext) (policy.Decision, error) {
		switch in.Call.ID {
		case "cancel":
			if earlier.Load() != 1 {
				t.Error("cancel crossed the preceding effect")
			}
			// Let the producer finish before cancel, then hold wait until
			// cancel has read the completed script's status snapshot.
			close(finish)
			select {
			case <-committed:
			case <-ctx.Done():
				return policy.Decision{}, ctx.Err()
			}
		case "wait":
			select {
			case <-cancelReturned:
			case <-ctx.Done():
				return policy.Decision{}, ctx.Err()
			}
		}
		return policy.Decision{Action: policy.ActionAllow}, nil
	}}}
	observeResult := hook.ToolResult{Name: "observe-cancel-status", Apply: func(_ context.Context, info hook.ToolContext, outcome hook.ToolOutcome) (tool.Result, error) {
		if info.Tool.ID == "cancel" {
			close(cancelReturned)
		}
		return outcome.Result, nil
	}}
	driver := &mcpScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: `{"code":"fixture","yield_time_ms":0}`}}
		case 1:
			handle := jsExecTaskHandle(req)
			if handle == "" {
				t.Error("missing live script Task handle")
				return nil
			}
			return []model.ToolCall{
				{ID: "before", Name: "Effect", Args: `{}`},
				{ID: "wait", Name: tasktool.ToolName, Args: fmt.Sprintf(`{"action":"wait","handle":%q}`, handle)},
				{ID: "cancel", Name: tasktool.ToolName, Args: fmt.Sprintf(`{"action":"cancel","handle":%q}`, handle)},
				{ID: "after", Name: "Effect", Args: `{}`},
			}
		}
		return nil
	}}
	events := runNativeOrdering(t, store, active, driver, []tool.Tool{script, terminal, effect}, Config{
		TaskActivityChanged: func(session.SessionRef) { markCommitted() },
		Hooks:               hook.Set{ToolPolicy: registry, ToolResult: []hook.ToolResult{observeResult}},
	})
	if len(driver.requests) != 3 || earlier.Load() != 1 || later.Load() != 1 {
		t.Fatalf("batch ordering: model requests=%d preceding effects=%d later effects=%d", len(driver.requests), earlier.Load(), later.Load())
	}
	var replies []string
	for _, event := range events {
		if event.Type != session.EventTypeToolResult || event.Tool == nil || event.Tool.Name != tasktool.ToolName {
			continue
		}
		replies = append(replies, event.Tool.ID)
		switch event.Tool.ID {
		case "wait":
			raw, err := json.Marshal(event.Tool.Result)
			if err != nil || !strings.Contains(string(raw), receipt) {
				t.Fatalf("wait lost the selected terminal result: result=%s err=%v", raw, err)
			}
		case "cancel":
			raw, err := json.Marshal(event.Tool.Result)
			if err != nil || strings.Contains(string(raw), receipt) || strings.Contains(string(raw), "tool_result_json") {
				t.Fatalf("cancel exposed the script result: %s, err=%v", raw, err)
			}
		}
	}
	if !slices.Equal(replies, []string{"wait", "cancel"}) {
		t.Fatalf("paired canonical results = %v", replies)
	}
	assertSubagentSagaModelRoundTrip(t, store, active.SessionRef)
}

// Repeated real observations must not manufacture durable revisions, including
// after a nonzero model-output cursor has already committed.
func TestJSExecCanonicalCursorCommitsOnlyAdvancement(t *testing.T) {
	t.Parallel()
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
			// A synchronously completed script returns its business value rather
			// than a waiting snapshot. The Task remains the handle authority.
			if handle == "" {
				handle = entries[0].Handle
			}
			if handle == "" {
				t.Fatal("completed Task lost its handle")
			}
			cursor, _ := taskInt64Value(entries[0].Metadata["model_output_cursor"])
			if cursor <= 0 || entries[0].State != taskapi.StateCompleted {
				t.Fatalf("selected output checkpoint missing: %+v", entries[0])
			}
			revision = entries[0].Revision
			return []model.ToolCall{{ID: "repeat", Name: "Task", Args: `{"action":"wait","yield_time_ms":0,"handle":"` + handle + `"}`}}
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

func TestJSExecCanonicalCursorDoesNotRewriteReceiptMetadata(t *testing.T) {
	t.Parallel()
	store, active, _ := codeModeStore(t)
	tasks := &taskRuntime{store: sessionfile.NewTaskStore(store)}
	meta := map[string]any{"runtime": map[string]any{
		"binding": map[string]any{"task_result": true},
		"task":    map[string]any{"kind": string(taskapi.KindCodeMode)},
	}}
	event := &session.Event{
		Type: session.EventTypeToolResult,
		Tool: &session.EventTool{Name: codemode.ToolName},
		Meta: map[string]any{"caelis": meta},
	}
	if err := tasks.syncCanonicalToolResult(t.Context(), active.SessionRef, event); err != nil {
		t.Fatal(err)
	}
	if _, found := meta["version"]; found {
		t.Fatal("cursor read inserted a version into the original receipt")
	}
}

type failCodeModeInitialStore struct {
	taskapi.Store
	failure error
}

func (s failCodeModeInitialStore) Put(ctx context.Context, req taskapi.PutRequest) (*taskapi.Entry, error) {
	if req.Entry != nil && req.Entry.Kind == taskapi.KindCodeMode && req.ExpectedRevision == 0 {
		return nil, s.failure
	}
	return s.Store.(taskapi.CASStore).Put(ctx, req)
}

func TestJSExecInitialPersistenceFailureDoesNotBindOutput(t *testing.T) {
	t.Parallel()
	store, active, _ := codeModeStore(t)
	base := sessionfile.NewTaskStore(store)
	failure := errors.New("initial-script-persistence-failure")
	trace := &recordingProducerLifecycle{}
	var executed atomic.Bool
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "script-submit", Name: codemode.ToolName, Args: `{"code":"return 42","yield_time_ms":0}`}}
		}
		return nil
	}}
	declaration := &codemode.Tool{Name: codemode.ToolName, Executor: codeExecutorFunc(func(context.Context, codemode.Source, codemode.Host) (json.RawMessage, error) {
		executed.Store(true)
		return []byte(`42`), nil
	})}
	rt, err := New(Config{Sessions: store, TaskStore: failCodeModeInitialStore{Store: base, failure: failure}, TaskOutput: trace, AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPolicy: mcpFixturePolicy()}, DefaultPolicyMode: "mcp-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.ReleaseCodeModeEnvironments() })
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "script submission persistence failure", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{declaration}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	entries, err := base.ListSession(t.Context(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if trace.binding.TaskID != "" || len(trace.events) != 0 || executed.Load() || len(entries) != 0 {
		t.Fatalf("failed submission retained a producer: binding=%+v events=%+v executed=%v entries=%+v", trace.binding, trace.events, executed.Load(), entries)
	}
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == session.EventTypeToolResult && event.Tool != nil && event.Tool.ID == "script-submit" {
			raw, err := json.Marshal(event.Tool)
			if err != nil || event.Tool.Status != "failed" || !strings.Contains(string(raw), failure.Error()) {
				t.Fatalf("submission failure was hidden: tool=%+v err=%v", event.Tool, err)
			}
			return
		}
	}
	t.Fatal("missing failed script submission result")
}

func TestJSExecTerminalCommitFailureReachesProducerAndObservers(t *testing.T) {
	t.Parallel()
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
			return tool.Result{Content: []tool.ContentBlock{tool.Text("original-effect-receipt")}}, nil
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
	rt, err := New(Config{Sessions: store, TaskStore: independentFailScriptTerminalStore{Store: base, marker: marker}, TaskOutput: trace, AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPolicy: mcpFixturePolicy()}, DefaultPolicyMode: "mcp-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.ReleaseCodeModeEnvironments() })
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
		if e.Type == session.EventTypeToolResult && e.Tool != nil {
			r := e.Tool
			if r.Name == "Effect" && r.Status == "completed" {
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
	t.Parallel()
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

func TestTaskCodeModeResultAggregatesStructuredReceipts(t *testing.T) {
	t.Parallel()
	const identifier = `9007199254740993123456789`
	encodeSnapshot := func(handle string, content []tool.ContentBlock) taskapi.Snapshot {
		t.Helper()
		raw, err := json.Marshal(tool.Result{Content: content})
		if err != nil {
			t.Fatal(err)
		}
		return taskapi.Snapshot{Kind: taskapi.KindCodeMode, Handle: handle, State: taskapi.StateCompleted, Result: map[string]any{"tool_result_json": string(raw)}}
	}
	image := tool.Image{Data: []byte("abc"), MIMEType: "image/png"}
	first := encodeSnapshot("one", []tool.ContentBlock{tool.Text("before"), tool.StructuredContent{Value: json.RawMessage(identifier)}, image})
	second := encodeSnapshot("two", []tool.ContentBlock{tool.StructuredContent{Value: json.RawMessage(`null`)}, tool.Text("after")})
	ctx := t.Context()
	single, err := taskControlSnapshotToolResult(ctx, first, "wait", 0)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := taskBatchControlToolResult([]taskBatchControlItem{{Snapshot: first}, {Snapshot: second}}, "wait", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		result    tool.Result
		wantParts int
	}{
		{"single", single, 3},
		{"batch", batch, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, ok := tc.result.Structured()
			if !ok || len(tc.result.Content) != tc.wantParts || tool.ValidateContent(tc.result.Content) != nil || strings.Contains(string(raw), "tool_result_json") {
				t.Fatalf("unexpected Task result: %+v", tc.result)
			}
			var payload struct {
				Result json.RawMessage
				Tasks  []struct{ Result json.RawMessage }
			}
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			if tc.name == "single" && string(payload.Result) != identifier || tc.name == "batch" && (len(payload.Tasks) != 2 || string(payload.Tasks[0].Result) != identifier || string(payload.Tasks[1].Result) != "null") {
				t.Fatalf("Task changed structured values: %s", raw)
			}
			if tc.result.Content[1] != tool.Text("before") || string(tc.result.Content[2].(tool.Image).Data) != "abc" || tc.name == "batch" && tc.result.Content[3] != tool.Text("after") {
				t.Fatalf("Task reordered attachments: %+v", tc.result.Content)
			}
		})
	}
}

func TestTaskResultMetadataKeepsCanonicalHandle(t *testing.T) {
	t.Parallel()
	snapshot := taskapi.Snapshot{
		Ref:      taskapi.Ref{TaskID: "original"},
		Handle:   "original-script",
		Kind:     taskapi.KindCodeMode,
		Metadata: map[string]any{"handle": "stale-display-handle"},
	}
	meta := taskToolMeta(snapshot)
	if got := taskRuntimeMetaString(meta, "task", "handle"); got != "original-script" {
		t.Fatalf("task receipt handle = %q, want original-script", got)
	}
}

func TestCodeModeCommandApprovalUsesOriginalOwner(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"allow", "deny", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			commandText := "printf once > effect.txt"
			if goruntime.GOOS == "windows" {
				commandText = "Set-Content -Path effect.txt -Value once -NoNewline"
			}
			scriptArgs, _ := json.Marshal(map[string]any{"code": "return await RunCommand({command:" + stringJSONForCommandTest(commandText) + "});", "yield_time_ms": 0})
			backend, err := host.New(host.Config{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			command, err := shell.NewRunCommand(shell.RunCommandConfig{Runtime: backend})
			if err != nil {
				t.Fatal(err)
			}
			asked, release := make(chan string, 1), make(chan struct{})
			registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(_ context.Context, in policy.ToolContext) (policy.Decision, error) {
				if in.Tool.Name == "RunCommand" {
					return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{}}, nil
				}
				return policy.Decision{Action: policy.ActionAllow}, nil
			}}}
			requester := approvalRequesterFunc(func(ctx context.Context, req approval.Request) (approval.Response, error) {
				if req.Tool.Name != "RunCommand" || req.Parent == nil || req.Parent.ID != "script" {
					t.Error("approval lost real command target/direct parent")
				}
				asked <- req.OperationTaskID
				select {
				case <-release:
				case <-ctx.Done():
				}
				option := "allow_once"
				if mode == "deny" {
					option = "cancel"
				}
				return approval.Response{Outcome: "selected", OptionID: option, Approved: mode != "deny"}, nil
			})
			var commandHandle string
			driver := &mcpScriptModel{steps: func(i int, r *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: string(scriptArgs)}}
				}
				if i == 1 {
					select {
					case commandID := <-asked:
						if mode == "cancel" {
							entries, err := sessionfile.NewTaskStore(store).ListSession(t.Context(), active.SessionRef)
							if err != nil {
								t.Fatal(err)
							}
							for _, entry := range entries {
								if entry.TaskID == commandID {
									commandHandle = entry.Handle
								}
							}
							if commandHandle == "" {
								t.Fatal("pending approval lost its original command Task")
							}
						}
					case <-time.After(5 * time.Second):
						t.Error("command never reached original approval")
						return nil
					}
					if _, err := os.Stat(filepath.Join(root, "effect.txt")); !os.IsNotExist(err) {
						t.Error("command executed before approval")
					}
					action := "wait"
					if mode == "cancel" {
						action = "cancel"
					} else {
						close(release)
					}
					handle := jsExecTaskHandle(r)
					if mode == "cancel" {
						handle = commandHandle
					}
					raw, _ := json.Marshal(map[string]any{"action": action, "handle": handle})
					return []model.ToolCall{{ID: "observe", Name: "Task", Args: string(raw)}}
				}
				return nil
			}}
			_, rt := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{command, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Sandbox: backend}}}, registry, requester)
			raw, err := os.ReadFile(filepath.Join(root, "effect.txt"))
			if mode == "allow" {
				if err != nil || string(raw) != "once" {
					t.Fatalf("actual command effect=%q %v", raw, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("rejected/cancelled command effect=%q %v", raw, err)
			}
			entries, err := rt.tasks.store.ListSession(t.Context(), active.SessionRef)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Running {
					t.Fatalf("producer did not close: %+v", entry)
				}
			}
		})
	}
}

func stringJSONForCommandTest(s string) string { raw, _ := json.Marshal(s); return string(raw) }

func TestCodeModeCommandApprovalAfterScriptExit(t *testing.T) {
	t.Parallel()
	startErr := errors.New("test sandbox could not start command")
	persistErr := errors.New("test command terminal write failed")
	for _, tc := range []struct {
		name       string
		wantStarts int32
		wantState  taskapi.State
		wantError  string
		exitCode   int
		startErr   error
	}{
		{name: "allow", wantStarts: 1, wantState: taskapi.StateCompleted},
		{name: "reset", wantStarts: 1, wantState: taskapi.StateCompleted},
		{name: "nonzero", wantStarts: 1, wantState: taskapi.StateFailed, exitCode: 7},
		{name: "deny", wantState: taskapi.StateFailed, wantError: "approval_denied"},
		{name: "start-failure", wantStarts: 1, wantState: taskapi.StateFailed, startErr: startErr},
		{name: "stale", wantState: taskapi.StateFailed, wantError: "stale_revision"},
		{name: "cancel", wantState: taskapi.StateCancelled},

		{name: "settlement-failure", wantStarts: 1, startErr: startErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, active, root := codeModeStore(t)
			native, err := host.New(host.Config{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			defer native.Close()
			backend := &codeModeCommandSandbox{Runtime: native, startErr: tc.startErr}
			command, err := shell.NewRunCommand(shell.RunCommandConfig{Runtime: backend})
			if err != nil {
				t.Fatal(err)
			}
			registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(_ context.Context, in policy.ToolContext) (policy.Decision, error) {
				if in.Tool.Name == shell.RunCommandToolName {
					return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{}}, nil
				}
				return policy.Decision{Action: policy.ActionAllow}, nil
			}}}
			approve := make(chan struct{})
			asked := make(chan approval.Request, 1)
			requester := approvalRequesterFunc(func(ctx context.Context, req approval.Request) (approval.Response, error) {
				asked <- req
				if err := req.OnAdmission(ctx); err != nil {
					return approval.Response{}, err
				}
				select {
				case <-approve:
					if tc.name == "deny" {
						return approval.Response{Outcome: "selected", OptionID: "reject_once", Reason: "test approval denied"}, nil
					}
					return approval.Response{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
				case <-ctx.Done():
					return approval.Response{}, ctx.Err()
				}
			})
			var stale atomic.Bool
			var pending struct {
				Handle string
				State  taskapi.State
				Wait   struct {
					Handle string
					State  taskapi.State
				}
			}
			target := filepath.Join(root, "command-effect.txt")
			commandText := shellWriteFileForTest(target, "once")
			if tc.exitCode != 0 {
				commandText = "exit 7"
			}
			script := `let retainedCommandState = 41;
const commandReceipt = (await RunCommand({command:` + stringJSONForCommandTest(commandText) + `,yield_time_ms:0})).data;
const commandWait = (await Task({action:"wait",handle:commandReceipt.handle,yield_time_ms:0})).data;
return {...commandReceipt, wait:commandWait};`
			driver := &mcpScriptModel{steps: func(step int, req *model.Request) []model.ToolCall {
				switch step {
				case 0:
					return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: string(mustJSONRaw(map[string]any{"code": script}))}}
				case 1:
					result := errorDeliveryModelResult(t, req, "script")
					var raw []byte
					if len(result.Content) == 1 {
						raw = result.Content[0].JSONValue()
					}
					if result.IsError || json.Unmarshal(raw, &pending) != nil ||
						pending.State != "in_progress" || pending.Handle == "" || pending.Wait.State != "in_progress" ||
						pending.Wait.Handle != pending.Handle || backend.starts.Load() != 0 {
						t.Errorf("script did not finish with a pending command and no effect: %s", raw)
						close(approve)
						return nil
					}

					args := `{"code":"return retainedCommandState;"}`
					if tc.name == "reset" {
						args = `{"reset":true}`
					}
					return []model.ToolCall{{ID: "next-script", Name: codemode.ToolName, Args: args}}
				case 2:
					result := errorDeliveryModelResult(t, req, "next-script")
					if result.IsError || len(result.Content) != 1 || (tc.name != "reset" && string(result.Content[0].JSONValue()) != "41") {
						t.Errorf("next script did not reuse the idle environment: %+v", result)
					}
					stale.Store(tc.name == "stale")
					action := "wait"
					if tc.name == "cancel" {
						action = "cancel"
					} else {
						close(approve)
					}
					if tc.name != "settlement-failure" {
						return []model.ToolCall{{ID: "observe", Name: "Task", Args: string(mustJSONRaw(map[string]any{"action": action, "handle": pending.Handle}))}}
					}
				case 3:
					result := errorDeliveryModelResult(t, req, "observe")
					var payload struct {
						State    taskapi.State
						Handle   string
						Code     string `json:"error_code"`
						Error    string
						ExitCode int `json:"exit_code"`
					}
					if result.IsError || len(result.Content) != 1 || json.Unmarshal(result.Content[0].JSONValue(), &payload) != nil ||
						payload.Handle != pending.Handle || payload.State != tc.wantState ||
						payload.Code != tc.wantError || payload.ExitCode != tc.exitCode ||
						(tc.name == "deny" && payload.Error != "test approval denied") ||
						(tc.startErr != nil && payload.Error != tc.startErr.Error()) {
						t.Errorf("model did not receive the original command outcome: %+v", payload)
					}
				}
				return nil
			}}

			spec := agent.AgentSpec{ResolveModelRequest: func(context.Context) (agent.ModelRequestSnapshot, error) {
				return agent.ModelRequestSnapshot{Model: driver, Tools: []tool.Tool{command, tasktool.New(),
					&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Sandbox: native}},
					Validate: func(context.Context) (func(), error) {
						if stale.Load() {
							return nil, agent.ErrModelRequestSnapshotStale
						}
						return func() {}, nil
					}}, nil
			}}
			tasks := sessionfile.NewTaskStore(store)
			var taskStore taskapi.Store = tasks
			if tc.name == "settlement-failure" {
				taskStore = &codeModeCommandFailingStore{TaskStore: tasks, err: persistErr}
			}
			rt, err := New(Config{Sessions: store, TaskStore: taskStore, AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPolicy: registry}, DefaultPolicyMode: "mcp-test"})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rt.ReleaseCodeModeEnvironments() }()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "submit a command", AgentSpec: spec, ApprovalRequester: requester})
			if err != nil {
				t.Fatal(err)
			}
			defer run.Handle.Close()
			err = run.Handle.WaitCompletion(ctx)
			if tc.name == "settlement-failure" {
				if !errors.Is(err, persistErr) {
					t.Fatalf("Run completion = %v, want terminal persistence failure", err)
				}
			} else if err != nil {
				t.Fatalf("Run completion = %v, want success", err)
			}
			if starts := backend.starts.Load(); starts != tc.wantStarts {
				t.Errorf("command starts = %d, want %d", starts, tc.wantStarts)
			}
			var request approval.Request
			select {
			case request = <-asked:
			default:
				t.Fatal("command did not reach approval")
			}
			entry, err := tasks.Get(ctx, request.OperationTaskID)
			if err != nil || entry == nil || entry.Handle != pending.Handle || (tc.wantState != "" && entry.State != tc.wantState) {
				t.Fatalf("original durable command = %+v, %v", entry, err)
			}
			data, err := os.ReadFile(target)
			if tc.wantState == taskapi.StateCompleted && (err != nil || string(data) != "once") {
				t.Fatalf("command effect = %q, %v", data, err)
			}
			if tc.wantState != taskapi.StateCompleted && !os.IsNotExist(err) {
				t.Fatalf("unexpected command effect = %q, %v", data, err)
			}
			if tc.name == "start-failure" {
				reopened := newLogicalFileStore(t, sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
				resumed, err := New(Config{Sessions: reopened, AgentFactory: chat.Factory{}})
				if err != nil {
					t.Fatal(err)
				}
				probe := &recoveryCaptureModel{}
				next, err := resumed.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "continue", AgentSpec: agent.AgentSpec{Model: probe}})
				if err != nil {
					t.Fatal(err)
				}
				defer next.Handle.Close()
				if err := next.Handle.WaitCompletion(ctx); err != nil {
					t.Fatal(err)
				}
				live := errorDeliveryModelResult(t, driver.requests[3], "observe")
				replayed := errorDeliveryModelResult(t, &model.Request{Messages: probe.messages}, "observe")
				if !reflect.DeepEqual(live, replayed) {
					t.Fatalf("replayed command failure = %+v, want live model result %+v", replayed, live)
				}
			}
		})
	}
}

type codeModeCommandSandbox struct {
	sandbox.Runtime
	starts   atomic.Int32
	startErr error
}

func (s *codeModeCommandSandbox) Start(ctx context.Context, req sandbox.CommandRequest) (sandbox.Session, error) {
	s.starts.Add(1)
	if s.startErr != nil {
		return nil, s.startErr
	}
	return s.Runtime.Start(ctx, req)
}

type codeModeCommandFailingStore struct {
	*sessionfile.TaskStore
	err error
}

func (s *codeModeCommandFailingStore) Put(ctx context.Context, req taskapi.PutRequest) (*taskapi.Entry, error) {
	if req.Entry.Kind == taskapi.KindCommand && req.Entry.State == taskapi.StateFailed {
		return nil, s.err
	}
	return s.TaskStore.Put(ctx, req)
}

func TestCodeModeRealRunCommand(t *testing.T) {
	t.Parallel()
	short, yielding, long := "printf command-done", "sleep 0.2; printf command-done", "sleep 30"
	if goruntime.GOOS == "windows" {
		short = "Write-Output command-done"
		yielding = "Start-Sleep -Milliseconds 200; Write-Output command-done"
		long = "Start-Sleep -Seconds 30"
	}
	q := func(s string) string { raw, _ := json.Marshal(s); return string(raw) }
	for _, tc := range []struct {
		name, body string
		yield      bool
		want       string
	}{
		{"short", `return await RunCommand({command:` + q(short) + `});`, false, "command-done"},
		{"inner_yield", `
const {data: command} = await RunCommand({command:` + q(yielding) + `, yield_time_ms: 0});
if (!command.handle) throw Error('missing command handle');
const {data: task} = await Task({action: 'wait', handle: command.handle});
if (task.handle !== command.handle || task.state !== 'completed') throw Error('unexpected command Task result');
return {output: task.result};`, false, "command-done"},
		{"outer_yield", `return await RunCommand({command:` + q(yielding) + `});`, true, "command-done"},
		{"cancel_inner", `
const {data: command} = await RunCommand({command:` + q(long) + `, yield_time_ms: 0});
try {
  const {data: task} = await Task({action: 'cancel', handle: command.handle});
  return {state: task.state};
} catch (e) {
  return {state: e.result.json().state};
}`, false, "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			backend, err := host.New(host.Config{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			command, err := shell.NewRunCommand(shell.RunCommandConfig{Runtime: backend})
			if err != nil {
				t.Fatal(err)
			}
			var scriptHandle string
			driver := &mcpScriptModel{steps: func(i int, r *model.Request) []model.ToolCall {
				if i == 0 {
					args := map[string]any{"code": tc.body}
					if tc.yield {
						args["yield_time_ms"] = 0
					}
					raw, _ := json.Marshal(args)
					return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: string(raw)}}
				}
				if i == 1 && tc.yield {
					scriptHandle = jsExecTaskHandle(r)
					raw, _ := json.Marshal(map[string]any{"action": "wait", "handle": scriptHandle})
					return []model.ToolCall{{ID: "wait", Name: "Task", Args: string(raw)}}
				}
				return nil
			}}
			live, rt := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{command, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
			progress := 0
			for _, event := range live {
				if event.Type != session.EventTypeToolResult || event.Tool == nil || event.Tool.Name != shell.RunCommandToolName || !session.IsUIOnly(event) {
					continue
				}
				progress++
				if event.Message != nil || event.Tool.Result != nil || event.Tool.Parent == nil || event.Tool.Parent.CallID != "script" {
					t.Fatalf("nested progress lost its transient shape or parent: %+v", event)
				}
			}
			if progress != 1 {
				t.Fatalf("nested RunCommand progress events = %d, want 1", progress)
			}
			id := "script"
			if tc.yield {
				id = "wait"
			}
			visible := errorDeliveryModelResult(t, driver.requests[len(driver.requests)-1], id)
			raw, _ := json.Marshal(visible.Content)
			if visible.IsError || !bytes.Contains(raw, []byte(tc.want)) {
				t.Fatalf("missing actual command result: %s", raw)
			}
			entries, err := rt.tasks.store.ListSession(t.Context(), active.SessionRef)
			if err != nil {
				t.Fatal(err)
			}
			codeID, commandID := "", ""
			for _, e := range entries {
				if e.Running {
					t.Fatalf("task still live: %+v", e)
				}
				switch e.Kind {
				case taskapi.KindCodeMode:
					codeID = e.TaskID
				case taskapi.KindCommand:
					commandID = e.TaskID
					process, err := backend.OpenSession(taskStringValue(e.Metadata["session_id"]))
					if err != nil {
						t.Fatal(err)
					}
					status, err := process.Status(t.Context())
					if err != nil || status.Running {
						t.Fatalf("native command process/readers not reaped: %+v %v", status, err)
					}
				}
			}
			if codeID == "" || commandID == "" || codeID == commandID {
				t.Fatalf("task identities code=%s command=%s", codeID, commandID)
			}
			events, _ := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			calls, results := 0, 0
			for _, e := range events {
				if e.Type == session.EventTypeToolResult && e.Tool != nil && e.Tool.Name == shell.RunCommandToolName {
					results++
					if e.Visibility != session.VisibilityMirror || e.Tool.Result == nil || e.Tool.Parent == nil || e.Tool.Parent.CallID != "script" {
						t.Fatalf("nested returned result lost its durable mirror shape or parent: %+v", e)
					}
				}
				if e.Type == session.EventTypeToolCall && e.Tool != nil && e.Tool.Name == "RunCommand" && e.Tool.Status == "pending" {
					calls++
					if e.Tool.Parent == nil || e.Tool.Parent.Name != codemode.ToolName {
						t.Fatal("lost direct script parent")
					}
				}
			}
			if calls != 1 || results != 1 {
				t.Fatalf("persisted command claims = %d, results = %d; want one each", calls, results)
			}
			if tc.yield && strings.TrimSpace(scriptHandle) == "" {
				t.Fatalf("outer handle=%q", scriptHandle)
			}
		})
	}
}
