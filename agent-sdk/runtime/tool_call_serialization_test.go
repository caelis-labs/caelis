package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

func TestCodeModeCancelsPendingApprovalWithoutEffect(t *testing.T) {
	store, active, _ := codeModeStore(t)
	started, approvalStarted := make(chan struct{}), make(chan struct{})
	var effects atomic.Int32
	target := tool.NamedTool{Def: tool.Definition{Name: "State", InputSchema: map[string]any{"type": "object"}}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
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
	requester := approvalRequesterFunc(func(ctx context.Context, _ agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		close(approvalStarted)
		<-ctx.Done()
		return agent.ApprovalResponse{}, ctx.Err()
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
		case <-approvalStarted:
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
	var succeeded, interrupted bool
	for _, event := range events {
		if event.Type != session.EventTypeToolResult || event.Tool == nil || event.Tool.Name != "State" {
			continue
		}
		if string(factJSON(event.Tool.Input)) == `{"queued":true}` {
			interrupted = event.Tool.Outcome == session.ToolOutcomeUnknown && event.Tool.Result != nil
		} else {
			succeeded = event.Tool.Outcome == session.ToolOutcomeUnknown
		}
	}
	if !succeeded || !interrupted {
		t.Fatal("accepted calls lost their complete outcomes")
	}

}

func TestCodeModeYieldedCallOverlapsSameTargetFromNativeSnapshot(t *testing.T) {
	store, active, _ := codeModeStore(t)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	started := make(chan struct{})
	var effects sync.Map
	target := tool.NamedTool{Def: tool.Definition{Name: "State", InputSchema: map[string]any{"type": "object"}}, Invoke: func(ctx context.Context, call tool.Call) (tool.Result, error) {
		var args struct {
			Index int `json:"index"`
		}
		if err := json.Unmarshal(call.Input, &args); err != nil {
			return tool.Result{}, err
		}
		if _, duplicate := effects.LoadOrStore(args.Index, struct{}{}); duplicate {
			t.Errorf("duplicate effect at index %d", args.Index)
		}
		switch args.Index {
		case 1:
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return tool.Result{}, ctx.Err()
			case <-time.After(5 * time.Second):
				t.Error("native call did not enter the target while the yielded call was active")
			}
		case 3:
			unblock()
		}
		return tool.Result{}, nil
	}}
	driver := &mcpScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return []model.ToolCall{{ID: "script-one", Name: codemode.ToolName, Args: `{"code":"return await tools.call('State',{index:1})","yield_time_ms":0}`}}
		case 1:
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Error("first yielded script did not enter its tool")
				return nil
			}
			return []model.ToolCall{
				{ID: "overlap", Name: codemode.ToolName, Args: `{"code":"return await tools.call('State',{index:2})","yield_time_ms":0}`},
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
		case 3:
			return []model.ToolCall{{ID: "script-two", Name: codemode.ToolName, Args: `{"code":"return await tools.call('State',{index:2})"}`}}

		}
		return nil
	}}
	tools := []tool.Tool{target, tasktool.New(), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}
	var snapshots atomic.Int32
	spec := agent.AgentSpec{Model: driver, Tools: tools, ResolveModelRequest: func(context.Context) (agent.ModelRequestSnapshot, error) {
		return agent.ModelRequestSnapshot{Model: driver, Tools: tools, Revision: fmt.Sprint(snapshots.Add(1)), Validate: func(context.Context) (func(), error) { return func() {}, nil }}, nil
	}}
	_, rt := runCodeMode(t, store, active, spec, mcpFixturePolicy(), nil)
	busy := errorDeliveryModelResult(t, driver.requests[2], "overlap")
	if !busy.IsError || !strings.Contains(string(errorDeliveryJSON(t, busy.Content)), "busy") {
		t.Fatalf("overlapping VM submission not rejected: %+v", busy)
	}
	if snapshots.Load() < 2 {
		t.Fatalf("snapshots=%d, want a replacement snapshot", snapshots.Load())
	}
	for _, index := range []int{1, 2, 3} {
		if _, found := effects.Load(index); !found {
			t.Errorf("missing effect for index %d", index)
		}
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

type taskWaitFactStore struct {
	session.Service
	started func(*session.EventTool)
}

func (s taskWaitFactStore) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	event, err := s.Service.AppendEvent(ctx, req)
	if err == nil && req.Event != nil && req.Event.Type == session.EventTypeToolCall && req.Event.Tool != nil {
		s.started(req.Event.Tool)
	}
	return event, err
}

func TestCodeModeNativeTaskWaitAllowsNestedTaskRead(t *testing.T) {
	files, active, _ := codeModeStore(t)
	waitStarted := make(chan struct{})
	markWait := sync.OnceFunc(func() { close(waitStarted) })
	store := taskWaitFactStore{Service: files, started: func(record *session.EventTool) {
		if record.Name == tasktool.ToolName && record.Parent == nil && record.Status == "pending" {
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
		return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: raw}}}, err
	}}
	driver := &mcpScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return []model.ToolCall{{ID: "yielded", Name: codemode.ToolName, Args: `{"code":"const reply=await tools.call('WaitForNativeTaskWait',{}); const value=reply.data; await tools.call('Task',{action:'wait',yield_time_ms:0,handle:value.handle}); return {done:true}","yield_time_ms":0}`}}
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
	t.Cleanup(func() { _ = rt.ReleaseCodeModeEnvironments() })
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
		if event.Type != session.EventTypeToolResult || event.Tool == nil {
			continue
		}
		record := event.Tool
		if record.Name == tasktool.ToolName && record.Status == "completed" {
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
