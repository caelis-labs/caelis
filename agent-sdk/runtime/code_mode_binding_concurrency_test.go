package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/mcpdiscovery"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

// changingBindingSource replaces one method while retaining its original name.
type changingBindingSource struct {
	mu      sync.Mutex
	current tool.Tool
}

func (s *changingBindingSource) Tools() []tool.Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []tool.Tool{s.current}
}

func (s *changingBindingSource) SearchTools() []tool.Tool { return s.Tools() }

func (s *changingBindingSource) Lookup(ctx context.Context, name string) (tool.Tool, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil || s.current.Definition().Name != name {
		return nil, false, nil
	}
	return s.current, true, nil
}

func (s *changingBindingSource) CheckToolBinding(ctx context.Context, binding tool.Binding) error {
	one, found, err := s.Lookup(ctx, binding.Name)
	if err != nil {
		return err
	}
	if !found || tool.BindingForDefinition(one.Definition()) != binding {
		return tool.NewError(tool.ErrorCodeStaleRevision, "source replaced the binding")
	}
	return nil
}

func (s *changingBindingSource) publish(one tool.Tool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = one
}

// Session scripts are serial. A compatible replacement still needs explicit
// discovery; a retained VM does not refresh canonical binding authority.
func TestCodeModeSequentialBindingRequiresExplicitRefresh(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "without-search", true: "explicit-search"}[refresh], func(t *testing.T) {
			files, active, root := codeModeStore(t)
			source := &changingBindingSource{}
			var effectsV1, effectsV2 atomic.Int32
			publish := func(version string, counter *atomic.Int32) {
				def := fixtureMCPDefinition(version)
				source.publish(tool.NamedTool{Def: def, Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
					counter.Add(1)
					if call.Parent == nil || call.TargetBinding == nil || call.TargetBinding.Version != version {
						return tool.Result{}, fmt.Errorf("wrong canonical binding: %+v", call)
					}
					return tool.Result{Content: []model.Part{model.NewJSONPart([]byte(`{"ok":true}`))}}, nil
				}})
			}
			publish("serial-v1", &effectsV1)
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "script-a", Name: codemode.ToolName, Args: `{"code":"return await tools.call('docs__read',{});"}`}}
				}
				if i == 1 {
					if effectsV1.Load() != 1 {
						t.Error("first script did not complete before the next")
					}
					publish("serial-v2", &effectsV2)
					code := `return await tools.call("docs__read",{});`
					if refresh {
						code = `await tools.search({name:"docs__read"});` + code
					}
					raw, _ := json.Marshal(map[string]any{"code": code})
					return []model.ToolCall{{ID: "script-b", Name: codemode.ToolName, Args: string(raw)}}
				}
				return nil
			}}
			_, rt := runCodeMode(t, files, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: source}, nil, nil)
			events, err := files.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			wantV2 := int32(0)
			wantBinding, wantState := "serial-v1", "failed"
			if refresh {
				wantV2 = 1
				wantBinding, wantState = "serial-v2", "completed"
			}
			if effectsV1.Load() != 1 || effectsV2.Load() != wantV2 {
				t.Fatalf("effects v1=%d v2=%d; want 1/%d", effectsV1.Load(), effectsV2.Load(), wantV2)
			}
			entries, err := rt.tasks.store.ListSession(t.Context(), active.SessionRef)
			if err != nil || len(entries) != 2 {
				t.Fatalf("Tasks=%d error=%v", len(entries), err)
			}
			for _, entry := range entries {
				if entry.Metadata["parent_call"] == "script-b" && string(entry.State) != wantState {
					t.Fatalf("second Task state=%s want %s", entry.State, wantState)
				}
				if entry.Running {
					t.Fatal("script remained running")
				}
			}
			if binding := codeModeBindings(events)["docs__read"]; binding.Version != wantBinding {
				t.Fatalf("binding=%+v want %s", binding, wantBinding)
			}
			if !refresh {
				result := errorDeliveryModelResult(t, driver.requests[len(driver.requests)-1], "script-b")
				raw := string(errorDeliveryJSON(t, result.Content))
				if !result.IsError || !strings.Contains(raw, "tools.search") {
					t.Fatal(raw)
				}
			}
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
			reloaded, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			if binding := codeModeBindings(reloaded)["docs__read"]; binding.Version != wantBinding {
				t.Fatal(binding)
			}
			assertSubagentSagaModelRoundTrip(t, reopened, active.SessionRef)
		})
	}
}

func TestCodeModeConcurrentSearchCannotRedirectPendingOccurrence(t *testing.T) {
	files, active, _ := codeModeStore(t)
	source := &changingBindingSource{}
	var oldEffects, newEffects atomic.Int32
	publish := func(version string, count *atomic.Int32) {
		source.publish(tool.NamedTool{Def: fixtureMCPDefinition(version), Invoke: func(context.Context, tool.Call) (tool.Result, error) {
			count.Add(1)
			return tool.Result{Content: []model.Part{model.NewJSONPart([]byte(`{"complete":true,"value":9007199254740993123456789}`))}}, nil
		}})
	}
	publish("concurrent-v1", &oldEffects)
	asked, release := make(chan struct{}), make(chan struct{})
	requester := approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		if req.Call.TargetBinding != nil && req.Call.TargetBinding.Version == "concurrent-v1" {
			close(asked)
			<-release
		}
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
	})
	var checked atomic.Bool
	declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		finishApproval := sync.OnceFunc(func() { close(release) })
		defer finishApproval()
		first := make(chan error, 1)
		go func() { _, err := host.Call(ctx, "docs__read", []byte(`{}`)); first <- err }()
		select {
		case <-asked:
		case err := <-first:
			if err != nil {
				return nil, fmt.Errorf("call failed before approval: %w", err)
			}
			return nil, errors.New("call finished without approval")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		publish("concurrent-v2", &newEffects)
		if _, err := host.(codemode.DiscoveryHost).Search(ctx, codemode.SearchRequest{Name: "docs__read"}); err != nil {
			return nil, err
		}
		finishApproval()
		if err := <-first; err == nil || (errorcode.CodeOf(err) != tool.ErrorCodeStaleRevision && errorcode.CodeOf(err) != tool.ErrorCodePermissionDenied) {
			return nil, fmt.Errorf("pending occurrence accepted changed source: %w", err)
		}
		checked.Store(true)
		return []byte(`{}`), nil
	})}
	_, _ = runCodeMode(t, files, active, agent.AgentSpec{Model: codeModeModel(false), Tools: []tool.Tool{declaration}, DeferredTools: source}, mcpAskFixturePolicy(), requester)

	fresh := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		_, err := host.Call(ctx, "docs__read", []byte(`{}`))
		return []byte(`{}`), err
	})}
	runCodeMode(t, files, active, agent.AgentSpec{Model: codeModeModel(false), Tools: []tool.Tool{fresh}, DeferredTools: source}, mcpAskFixturePolicy(), requester)
	if !checked.Load() || oldEffects.Load() != 0 || newEffects.Load() != 1 {
		t.Fatalf("checked=%v old=%d new=%d", checked.Load(), oldEffects.Load(), newEffects.Load())
	}
	events, err := files.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := session.PairToolFacts(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range pairs {
		if pair.Call.Tool.Name == "docs__read" && (pair.Result == nil || pair.Result.Tool.Result == nil || pair.Call.Tool.Parent == nil || pair.Call.Tool.Parent.Execution.ItemID == "") {
			t.Fatal("lost complete nested occurrence or parent")
		}
	}
	if codeModeBindings(events)["docs__read"].Version != "concurrent-v2" {
		t.Fatal("late result changed discovery binding")
	}
}

// The Host can resolve v1, then a sibling can commit a v2 search before the old
// intent reaches the writer. That late intent must not roll discovery back.
func TestCodeModeOldPreparedIntentCannotOverwriteNewDiscovery(t *testing.T) {
	files, active, _ := codeModeStore(t)
	rt, _ := New(Config{Sessions: files, AgentFactory: chat.Factory{}})
	fence, err := files.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "interleaved-host"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := session.ContextWithRuntimeFence(t.Context(), fence)
	old, current := tool.BindingForDefinition(fixtureMCPDefinition("v1")), tool.BindingForDefinition(fixtureMCPDefinition("v2"))
	parent := &tool.ParentCall{ID: "script", Name: "js_exec", Kind: codemode.ToolKind, Execution: tool.InvocationContext{SessionID: active.SessionID, TurnID: "turn", ItemID: "script"}}
	raw, _ := json.Marshal(map[string]any{"tools": []any{map[string]any{"name": current.Name, "binding": current, "input_schema": map[string]any{"type": "object"}}}})
	search := tool.Call{ID: "search", Name: tool.ToolSearchToolName, Input: []byte(`{}`), Parent: parent, Execution: tool.InvocationContext{SessionID: active.SessionID, TurnID: "turn", ItemID: "search"}}
	result := tool.Result{Content: []model.Part{model.NewJSONPart(raw)}, Metadata: map[string]any{mcpdiscovery.MetadataKey: true}}
	if err := rt.appendToolFact(ctx, active.SessionRef, "turn", search, &result, nil); err != nil {
		t.Fatal(err)
	}
	prepared := tool.Call{ID: "old-intent", Name: old.Name, Input: []byte(`{}`), Parent: parent, TargetBinding: &old, Execution: tool.InvocationContext{SessionID: active.SessionID, TurnID: "turn", ItemID: "old"}}
	if err := rt.appendToolFact(ctx, active.SessionRef, "turn", prepared, nil, nil); !errorcode.Is(err, tool.ErrorCodeStaleRevision) {
		t.Fatalf("old intent accepted after new discovery: %v", err)
	}
	events, err := files.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	if codeModeBindings(events)[current.Name] != current {
		t.Fatal("late intent rolled back discovery")
	}
	for _, event := range events {
		if event.Tool != nil && event.Tool.Execution == prepared.Execution {
			t.Fatal("rejected stale intent became binding authority")
		}
	}
}
