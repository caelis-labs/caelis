package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

// The map is deliberately unsynchronized: Runtime owns serialization for this
// default-capability tool. Overlap returns before touching it so a regression
// reports a useful assertion instead of crashing the process with a map write.
type serializationStateTool struct {
	def     tool.Definition
	started chan struct{}
	release <-chan struct{}
	active  atomic.Int32
	maximum atomic.Int32
	calls   atomic.Int32
	values  map[int]int
}

func (t *serializationStateTool) Definition() tool.Definition { return tool.CloneDefinition(t.def) }

func (t *serializationStateTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	active := t.active.Add(1)
	defer t.active.Add(-1)
	for previous := t.maximum.Load(); active > previous; previous = t.maximum.Load() {
		if t.maximum.CompareAndSwap(previous, active) {
			break
		}
	}
	if active != 1 {
		return tool.Result{}, errors.New("default-capability tool called concurrently")
	}
	if t.calls.Add(1) == 1 {
		close(t.started)
		select {
		case <-t.release:
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	}
	var args struct {
		Index int `json:"index"`
	}
	if err := json.Unmarshal(call.Input, &args); err != nil {
		return tool.Result{}, err
	}
	t.values[args.Index]++
	return tool.Result{}, nil
}

func TestJSExecSerializesDefaultTargetPromiseAll(t *testing.T) {
	for _, mcp := range []bool{false, true} {
		t.Run(fmt.Sprint("mcp-", mcp), func(t *testing.T) {
			const count = 24
			store, active, _ := codeModeStore(t)
			release := make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			def := tool.Definition{Name: "State", InputSchema: map[string]any{"type": "object"}}
			if mcp {
				def = fixtureMCPDefinition("serialized-state")
				def.Name = "z__target"
				def.Metadata[tool.MetadataMCPSourceTrusted] = true
			}
			target := &serializationStateTool{def: def, started: make(chan struct{}), release: release, values: make(map[int]int)}
			admitted := make(chan struct{})
			var admissions atomic.Int32
			registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(_ context.Context, in policy.ToolContext) (policy.Decision, error) {
				if in.Tool.Name == def.Name && admissions.Add(1) == count {
					close(admitted)
				}
				return policy.Decision{Action: policy.ActionAllow}, nil
			}}}
			releaseTool := tool.NamedTool{Def: tool.Definition{Name: "Release", InputSchema: map[string]any{"type": "object"}}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
				for _, signal := range []<-chan struct{}{target.started, admitted} {
					select {
					case <-signal:
					case <-ctx.Done():
						return tool.Result{}, ctx.Err()
					}
				}
				unblock()
				return tool.Result{}, nil
			}}
			code := fmt.Sprintf(`await tools.search(%q); const pending = Array.from({length:%d}, (_, index) => tools.call(%q, {index})); await tools.call("Release", {}); await Promise.all(pending); return {count:pending.length};`, def.Name, count, def.Name)
			script := func() []model.ToolCall {
				raw, _ := json.Marshal(map[string]any{"code": code, "yield_time_ms": 60000})
				return []model.ToolCall{{ID: "parallel-state", Name: codemode.ToolName, Args: string(raw)}}
			}
			var spec agent.AgentSpec
			if mcp {
				source := &h1Source{items: []tool.Tool{target}, seen: map[string]bool{}}
				driver := &h1Model{s: source, main: func(i int, _ *model.Request) []model.ToolCall {
					if i == 0 {
						return script()
					}
					return nil
				}}
				spec = h1Spec(source, driver)
				spec.Tools = append(spec.Tools, releaseTool)
			} else {
				driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
					if i == 0 {
						return script()
					}
					return nil
				}}
				spec = agent.AgentSpec{Model: driver, Tools: []tool.Tool{target, releaseTool, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}
			}
			runCodeMode(t, store, active, spec, registry, nil)
			if target.maximum.Load() != 1 || target.calls.Load() != count || len(target.values) != count {
				t.Fatalf("maximum=%d calls=%d state entries=%d; want 1, %d, %d", target.maximum.Load(), target.calls.Load(), len(target.values), count, count)
			}
			for index := range count {
				if target.values[index] != 1 {
					t.Fatalf("state[%d]=%d, want one effect", index, target.values[index])
				}
			}
		})
	}
}

func TestToolCallSerializationCancellationBeforeClaim(t *testing.T) {
	store, active, _ := codeModeStore(t)
	started, approved := make(chan struct{}), make(chan struct{})
	var effects atomic.Int32
	target := tool.NamedTool{Def: tool.Definition{Name: "State", EffectClass: tool.EffectReadOnly, InputSchema: map[string]any{"type": "object"}}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
		if effects.Add(1) == 1 {
			close(started)
		}
		<-ctx.Done()
		return tool.Result{}, ctx.Err()
	}}
	registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(_ context.Context, in policy.ToolContext) (policy.Decision, error) {
		if in.Tool.Name == "State" && string(in.Call.Input) == `{"queued":true}` {
			return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{}}, nil
		}
		return policy.Decision{Action: policy.ActionAllow}, nil
	}}}
	requester := approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		close(approved)
		return agent.ApprovalResponse{Approved: true, Outcome: "selected", OptionID: "allow_once"}, nil
	})
	var queuedErr error
	declaration := &codemode.Tool{Name: codemode.ToolName, Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		firstDone := make(chan struct{})
		go func() {
			defer close(firstDone)
			_, _ = host.Call(ctx, "State", []byte(`{}`))
		}()
		select {
		case <-started:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		queuedCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		queuedDone := make(chan error, 1)
		go func() {
			_, err := host.Call(queuedCtx, "State", []byte(`{"queued":true}`))
			queuedDone <- err
		}()
		select {
		case <-approved:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		cancel()
		select {
		case queuedErr = <-queuedDone:
		case <-time.After(5 * time.Second):
			return nil, errors.New("queued cancellation did not return while the first call was active")
		}
		// A rejected unclaimed host call closes its producer and cancels the
		// running read; both calls must drain before the executor returns.
		select {
		case <-firstDone:
		case <-ctx.Done():
			<-firstDone
		}
		return []byte(`{}`), nil
	})}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return h1Script("fixture")
		}
		return nil
	}}
	runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{target, declaration}}, registry, requester)
	if !errors.Is(queuedErr, context.Canceled) || effects.Load() != 1 {
		t.Fatalf("queued error=%v effects=%d; want cancellation and one effect", queuedErr, effects.Load())
	}
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	claimed := false
	for _, event := range events {
		if event.Journal == nil || event.Journal.ToolExecution == nil || event.Journal.ToolExecution.ToolName != "State" {
			continue
		}
		record := event.Journal.ToolExecution
		if string(record.Input) == `{"queued":true}` {
			t.Fatalf("cancelled queued call wrote a durable execution record: %+v", record)
		}
		claimed = claimed || record.Status == session.ToolExecutionStarted
	}
	if !claimed {
		t.Fatal("the first call never durably claimed execution")
	}
}

func TestToolCallSerializationSharedAcrossYieldedSnapshotsAndNative(t *testing.T) {
	store, active, _ := codeModeStore(t)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	target := &serializationStateTool{def: tool.Definition{Name: "State", InputSchema: map[string]any{"type": "object"}}, started: make(chan struct{}), release: release, values: make(map[int]int)}
	var admissions atomic.Int32
	registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(_ context.Context, in policy.ToolContext) (policy.Decision, error) {
		if in.Tool.Name == "State" && admissions.Add(1) == 3 {
			unblock()
		}
		return policy.Decision{Action: policy.ActionAllow}, nil
	}}}
	driver := &mcpScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return []model.ToolCall{{ID: "script-one", Name: codemode.ToolName, Args: `{"code":"return await tools.call('State',{index:1})","yield_time_ms":0}`}}
		case 1:
			select {
			case <-target.started:
			case <-time.After(5 * time.Second):
				t.Error("first yielded script did not enter its tool")
				return nil
			}
			return []model.ToolCall{
				{ID: "script-two", Name: codemode.ToolName, Args: `{"code":"return await tools.call('State',{index:2})","yield_time_ms":0}`},
				{ID: "native-state", Name: "State", Args: `{"index":3}`},
			}
		case 2:
			// The final safe point cancels unfinished scripts. Wait on their
			// original handles so this test observes completion before ending the Turn.
			var waits []model.ToolCall
			for _, message := range req.Messages {
				for _, part := range message.Parts {
					if part.ToolResult == nil || part.ToolResult.Name != codemode.ToolName {
						continue
					}
					for _, content := range part.ToolResult.Content {
						if content.JSON == nil {
							continue
						}
						var snapshot struct {
							Handle string `json:"handle"`
						}
						if err := json.Unmarshal(content.JSON.Value, &snapshot); err != nil || snapshot.Handle == "" {
							continue
						}
						raw, _ := json.Marshal(map[string]string{"action": "wait", "handle": snapshot.Handle})
						waits = append(waits, model.ToolCall{ID: "observe-" + part.ToolResult.ToolUseID, Name: tasktool.ToolName, Args: string(raw)})
					}
				}
			}
			return waits
		}
		return nil
	}}
	tools := []tool.Tool{target, tasktool.New(), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}
	var snapshots atomic.Int32
	spec := agent.AgentSpec{Model: driver, Tools: tools, ResolveModelRequest: func(context.Context) (agent.ModelRequestSnapshot, error) {
		return agent.ModelRequestSnapshot{Model: driver, Tools: tools, Revision: fmt.Sprint(snapshots.Add(1)), Validate: func(context.Context) (func(), error) { return func() {}, nil }}, nil
	}}
	_, rt := runCodeMode(t, store, active, spec, registry, nil)
	if snapshots.Load() < 2 || target.maximum.Load() != 1 || target.calls.Load() != 3 || len(target.values) != 3 {
		t.Fatalf("snapshots=%d maximum=%d calls=%d state=%v", snapshots.Load(), target.maximum.Load(), target.calls.Load(), target.values)
	}
	entries, err := rt.tasks.store.ListSession(t.Context(), active.SessionRef)
	if err != nil || len(entries) != 2 {
		t.Fatalf("script tasks=%d err=%v", len(entries), err)
	}
	for _, entry := range entries {
		if entry.Running || entry.State != "completed" {
			t.Fatalf("yielded script did not complete: %+v", entry)
		}
	}
}

type serializationJournalStore struct {
	session.Service
	started func(*session.ToolExecution)
}

func (s serializationJournalStore) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	event, err := s.Service.AppendEvent(ctx, req)
	if err == nil && req.Event != nil && req.Event.Journal != nil && req.Event.Journal.ToolExecution != nil {
		s.started(req.Event.Journal.ToolExecution)
	}
	return event, err
}

func TestToolCallSerializationTaskWaitAllowsNestedTaskRead(t *testing.T) {
	files, active, _ := codeModeStore(t)
	waitStarted := make(chan struct{})
	markWait := sync.OnceFunc(func() { close(waitStarted) })
	store := serializationJournalStore{Service: files, started: func(record *session.ToolExecution) {
		if record.ToolName == tasktool.ToolName && record.Parent == nil && record.Status == session.ToolExecutionStarted {
			markWait()
		}
	}}
	handles := make(chan string, 1)
	target := tool.NamedTool{Def: tool.Definition{Name: "WaitForNativeTaskWait", InputSchema: map[string]any{"type": "object"}}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
		select {
		case <-waitStarted:
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
		raw, err := json.Marshal(map[string]any{"handle": <-handles})
		return tool.Result{Content: []model.Part{model.NewJSONPart(raw)}}, err
	}}
	driver := &mcpScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return []model.ToolCall{{ID: "yielded", Name: codemode.ToolName, Args: `{"code":"const reply=await tools.call('WaitForNativeTaskWait',{}); const value=reply.parts.find(p=>p.kind==='json').json; await tools.call('Task',{action:'read',handle:value.handle}); return {done:true}","yield_time_ms":0}`}}
		case 1:
			handle := jsExecTaskHandle(req)
			handles <- handle
			raw, _ := json.Marshal(map[string]any{"action": "wait", "handle": handle})
			return []model.ToolCall{{ID: "native-wait", Name: tasktool.ToolName, Args: string(raw)}}
		}
		return nil
	}}
	rt, err := New(Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(files), AgentFactory: chat.Factory{}, PolicyRegistry: mcpFixturePolicy(), DefaultPolicyMode: "mcp-test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "observe a yielded script", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{target, tasktool.New(), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(ctx); err != nil {
		cancel()
		_ = run.Handle.WaitCompletion(t.Context())
		t.Fatalf("Task.wait prevented the yielded script's nested Task.read: %v", err)
	}
	events, err := files.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	var read, wait bool
	for _, event := range events {
		if event.Journal == nil || event.Journal.ToolExecution == nil {
			continue
		}
		record := event.Journal.ToolExecution
		if record.ToolName == tasktool.ToolName && record.Status == session.ToolExecutionSucceeded {
			if record.Parent == nil {
				wait = true
			} else {
				read = true
			}
		}
	}
	if !read || !wait {
		t.Fatalf("successful Task executions: native wait=%v nested read=%v", wait, read)
	}
}
