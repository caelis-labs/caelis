package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestCodeModeRevalidatesMCPAndGenerationAfterApproval(t *testing.T) {
	for _, mode := range []string{"allow", "deny", "schema", "revoke", "generation", "missing-validator"} {
		t.Run(mode, func(t *testing.T) {
			store, active := newTestSessionService(t, "approval-revalidation")
			server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "admission", Version: "1"}, nil)
			var effects, approvals atomic.Int32
			publish := func(kind string) {
				server.AddTool(&mcpsdk.Tool{Name: "read", InputSchema: map[string]any{"type": "object", "required": []any{"value"}, "properties": map[string]any{"value": map[string]any{"type": kind}}}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
					effects.Add(1)
					return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "ok"}}}, nil
				})
			}
			publish("integer")
			endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
			defer endpoint.Close()
			manager, err := mcp.NewManager(t.Context(), []mcp.ServerSpec{{PluginID: "fixture", Name: "live", Transport: mcp.TransportStreamableHTTP, URL: endpoint.URL}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			<-manager.Initialized()
			if len(manager.Tools()) != 1 {
				t.Fatal("MCP fixture did not initialize")
			}
			var stale atomic.Bool
			requester := approvalRequesterFunc(func(ctx context.Context, req approval.Request) (approval.Response, error) {
				approvals.Add(1)
				if req.Tool.Name != "live__read" || req.Parent == nil || req.Parent.ID != "script" || req.Execution.ItemID == "" {
					t.Errorf("approval lost real identity: %+v", req.Call)
				}
				switch mode {
				case "schema":
					publish("string")
					if err := manager.RefreshTools(ctx, "fixture", "live"); err != nil {
						return approval.Response{}, err
					}
				case "revoke":
					if err := manager.RevokeServer("fixture", "live"); err != nil {
						return approval.Response{}, err
					}
				case "generation":
					stale.Store(true)
				}
				option := "allow_once"
				if mode == "deny" {
					option = "cancel"
				}
				return approval.Response{Outcome: "selected", OptionID: option, Approved: mode != "deny"}, nil
			})
			var checked atomic.Bool
			declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
				reply, err := host.Call(ctx, "live__read", []byte(`{"value":1}`))
				switch mode {
				case "allow":
					if err != nil || reply.IsError {
						return nil, errors.New("allowed call failed")
					}
				case "deny":
					if err != nil || !reply.IsError {
						return nil, errors.New("approval rejection lost")
					}
				default:
					if err == nil {
						return nil, errors.New("stale/revoked pending call reached effect")
					}
				}
				checked.Store(true)
				return []byte(`{"checked":true}`), nil
			})}
			llm := codeModeModel(false)
			spec := agent.AgentSpec{ResolveModelRequest: func(context.Context) (agent.ModelRequestSnapshot, error) {
				snapshot := agent.ModelRequestSnapshot{Model: llm, Tools: []tool.Tool{declaration}, DeferredTools: manager, Revision: "generation-1", Validate: func(context.Context) (func(), error) {
					if stale.Load() {
						return nil, tool.NewError(tool.ErrorCodeStaleRevision, "generation revoked")
					}
					return func() {}, nil
				}}
				if mode == "missing-validator" {
					snapshot.Validate = nil
				}
				return snapshot, nil
			}}
			registry := mcpAskFixturePolicy()
			runCodeMode(t, store, active, spec, registry, requester)
			want := int32(0)
			if mode == "allow" {
				want = 1
			}
			wantApprovals := int32(1)
			if mode == "missing-validator" {
				wantApprovals = 0
			}
			if !checked.Load() || approvals.Load() != wantApprovals || effects.Load() != want {
				t.Fatalf("checked=%v approvals=%d effects=%d", checked.Load(), approvals.Load(), effects.Load())
			}
		})
	}
}

func TestCodeModeCancellationAndRecoveryDoNotReplayEffects(t *testing.T) {
	for _, outcome := range []string{"responsive-unknown", "unresponsive-unknown", "unresponsive-success"} {
		responsive := outcome == "responsive-unknown"
		wantStatus := "unknown_outcome"
		if outcome == "unresponsive-success" {
			wantStatus = "succeeded"
		}
		t.Run(outcome, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			var effects atomic.Int32
			started, release, executorDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			write := tool.NamedTool{Def: tool.Definition{Name: "Write", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
				effects.Add(1)
				return tool.Result{Content: []tool.ContentBlock{tool.Text("done once")}}, nil
			}}
			pending := tool.NamedTool{Def: tool.Definition{Name: "Pending", InputSchema: map[string]any{"type": "object"}}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
				effects.Add(1)
				close(started)
				if responsive {
					<-ctx.Done()
					return tool.Result{}, ctx.Err()
				}
				<-release
				if wantStatus == "succeeded" {
					return tool.Result{Content: []tool.ContentBlock{tool.Text("confirmed after cancellation")}}, nil
				}
				return tool.Result{}, ctx.Err()
			}}
			var retained codemode.Host
			declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
				defer close(executorDone)
				retained = host
				if _, err := host.Call(ctx, "Write", []byte(`{}`)); err != nil {
					return nil, err
				}
				_, err := host.Call(ctx, "Pending", []byte(`{}`))
				return []byte(`{}`), err
			})}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPolicy: mcpFixturePolicy()}, DefaultPolicyMode: "mcp-test"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fence, err := store.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "code-mode-cancellation-fixture"})
			if err != nil {
				t.Fatal(err)
			}
			ctx = session.ContextWithRuntimeFence(ctx, fence)
			run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "effects then cancel", AgentSpec: agent.AgentSpec{Model: codeModeModel(false), Tools: []tool.Tool{declaration, write, pending}}})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("pending effect never started")
			}
			cancel()
			if !responsive {
				// Foreground cancellation is not quiescence. The existing Run
				// owner must remain joined while this producer ignores cancellation.
				wait, stop := context.WithTimeout(t.Context(), 250*time.Millisecond)
				err := run.Handle.WaitCompletion(wait)
				stop()
				if !errors.Is(err, context.DeadlineExceeded) {
					close(release)
					t.Fatalf("Runner released a live producer: %v", err)
				}
			}
			close(release)
			wait, stop := context.WithTimeout(t.Context(), 2*time.Second)
			defer stop()
			if err := run.Handle.WaitCompletion(wait); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel terminal=%v", err)
			}
			select {
			case <-executorDone:
			case <-time.After(5 * time.Second):
				t.Fatal("executor did not drain")
			}
			if _, err := retained.Call(t.Context(), "Write", []byte(`{}`)); err == nil {
				t.Fatal("retained Host accepted work after cancellation")
			}
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
			before, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			var original string
			var pendingStatus string
			for _, event := range before {
				if event.Type == session.EventTypeToolResult && event.Tool != nil && event.Tool.Name == "Pending" {
					original = event.Tool.Execution.ItemID
					pendingStatus = string(event.Tool.Outcome)
				}
			}
			if original == "" || pendingStatus != wantStatus {
				t.Fatalf("child terminal missing before fence release: identity=%s status=%s", original, pendingStatus)
			}
			if err := store.ReleaseSessionFence(t.Context(), session.SessionFenceReleaseRequest(fence)); err != nil {
				t.Fatal(err)
			}
			// A fresh Runtime performs existing receipt recovery, without dispatching
			// the executor or either effect again.
			idle := &mcpScriptModel{steps: func(int, *model.Request) []model.ToolCall { return nil }}
			runCodeMode(t, reopened, active, agent.AgentSpec{Model: idle, Tools: []tool.Tool{declaration, write, pending}}, nil, nil)
			after, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			statuses := map[string]string{}
			for _, event := range after {
				if event.Type == session.EventTypeToolResult && event.Tool != nil {
					r := event.Tool
					if r.Parent != nil {
						statuses[r.Name] = string(r.Outcome)
						if r.Name == "Pending" && r.Execution.ItemID != original {
							t.Fatal("recovery replaced original operation identity")
						}
					}
				}
				if session.IsCanonicalHistoryEvent(event) && event.Tool != nil && event.Tool.Parent != nil {
					t.Fatal("recovery fabricated inner model tool result")
				}
			}
			if original == "" || effects.Load() != 2 || statuses["Write"] != "succeeded" || statuses["Pending"] != wantStatus {
				t.Fatalf("effects=%d statuses=%v original=%s", effects.Load(), statuses, original)
			}
		})
	}
}

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
	if !found || mcp.BindingForDefinition(one.Definition()) != binding {
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
				source.publish(tool.NamedTool{Def: def, Invoke: func(ctx context.Context, call tool.Call) (tool.Result, error) {
					counter.Add(1)
					if binding := toolbinding.Target(ctx); tool.ParentFromContext(ctx) == nil || binding == nil || binding.Version != version {
						return tool.Result{}, fmt.Errorf("wrong canonical binding: %+v", call)
					}
					return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: []byte(`{"ok":true}`)}}}, nil
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
			return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: []byte(`{"complete":true,"value":9007199254740993123456789}`)}}}, nil
		}})
	}
	publish("concurrent-v1", &oldEffects)
	asked, release := make(chan struct{}), make(chan struct{})
	requester := approvalRequesterFunc(func(_ context.Context, req approval.Request) (approval.Response, error) {
		if mcp.BindingForDefinition(req.Tool).Version == "concurrent-v1" {
			close(asked)
			<-release
		}
		return approval.Response{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
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
	old, current := mcp.BindingForDefinition(fixtureMCPDefinition("v1")), mcp.BindingForDefinition(fixtureMCPDefinition("v2"))
	parent := &tool.ParentCall{ID: "script", Name: "js_exec", Kind: codemode.ToolKind, ItemID: "script"}
	raw, _ := json.Marshal(map[string]any{"tools": []any{map[string]any{"name": current.Name, "binding": current, "input_schema": map[string]any{"type": "object"}}}})
	search := tool.Call{ID: "search", Name: mcp.SearchToolName, Input: []byte(`{}`)}
	searchCtx := toolbinding.WithOccurrence(ctx, tool.InvocationContext{SessionID: active.SessionID, TurnID: "turn", ItemID: "search"}, parent, nil)
	searchCtx, observed := toolbinding.Observe(searchCtx)
	observed.Discovery = true
	result := tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: raw}}}
	if err := rt.appendToolFact(searchCtx, active.SessionRef, "turn", search, &result, nil); err != nil {
		t.Fatal(err)
	}
	prepared := tool.Call{ID: "old-intent", Name: old.Name, Input: []byte(`{}`)}
	preparedExecution := tool.InvocationContext{SessionID: active.SessionID, TurnID: "turn", ItemID: "old"}
	preparedCtx := toolbinding.WithOccurrence(ctx, preparedExecution, parent, &old)
	if err := rt.appendToolFact(preparedCtx, active.SessionRef, "turn", prepared, nil, nil); !errorcode.Is(err, tool.ErrorCodeStaleRevision) {
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
		if event.Tool != nil && event.Tool.Execution == preparedExecution {
			t.Fatal("rejected stale intent became binding authority")
		}
	}
}

func TestCodeModeSourceInputAndUserDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    map[string]any
		want    string
		failure bool
	}{
		{"neither", map[string]any{}, "invalid_input", true},
		{"both", map[string]any{"code": "return 1;", "file": "probe.js"}, "invalid_input", true},
		{"file", map[string]any{"file": "probe.js"}, "probe.js:3", true},
		{"undefined_field", map[string]any{"code": "return {nested:{bad:undefined}};"}, "$.nested.bad", true},
		{"omitted_return", map[string]any{"code": "const value=1;"}, "null", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			backend, err := host.New(host.Config{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			if err = os.WriteFile(filepath.Join(root, "probe.js"), []byte("const a=1;\nconst b=2;\nthrow new Error('user failure');"), 0600); err != nil {
				t.Fatal(err)
			}
			args, _ := json.Marshal(tc.args)
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "source", Name: codemode.ToolName, Args: string(args)}}
				}
				return nil
			}}
			runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Sandbox: backend}}}, nil, nil)
			result := errorDeliveryModelResult(t, driver.requests[len(driver.requests)-1], "source")
			raw, _ := json.Marshal(result.Content)
			if result.IsError != tc.failure || !bytes.Contains(raw, []byte(tc.want)) {
				t.Fatalf("wrong source error/result: %s", raw)
			}
			if (tc.name == "neither" || tc.name == "both") && bytes.Contains(raw, []byte("unknown_outcome")) {
				t.Fatal("pre-execution input became unknown")
			}
			if tc.name == "file" && bytes.Contains(raw, []byte("bootstrap")) {
				t.Fatal("internal bootstrap displaced user source")
			}
		})
	}
}

type codeModeCountingFS struct {
	sandbox.FileSystem
	opens *atomic.Int32
}

func (f codeModeCountingFS) Open(path string) (*os.File, error) {
	f.opens.Add(1)
	return f.FileSystem.Open(path)
}

type codeModeCountingRuntime struct {
	sandbox.Runtime
	opens *atomic.Int32
}

func (r codeModeCountingRuntime) FileSystemFor(constraints sandbox.Constraints) sandbox.FileSystem {
	provider := r.Runtime.(interface {
		FileSystemFor(sandbox.Constraints) sandbox.FileSystem
	})
	return codeModeCountingFS{FileSystem: provider.FileSystemFor(constraints), opens: r.opens}
}

func TestCodeModeSourceReadPolicyUsesConcreteSemanticsAndExistingGrants(t *testing.T) {
	for _, name := range []string{"relative", "sensitive", "tilde", "symlink", "granted", "inline"} {
		t.Run(name, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			// The complete sensitive tree is a temporary fixture, never a user
			// home. Its existing target also makes the symlink a reachable read.
			home := filepath.Join(root, "home")
			sensitive := filepath.Join(home, ".ssh", "fixture.js")
			if err := os.MkdirAll(filepath.Dir(sensitive), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			base, err := host.New(host.Config{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			defer base.Close()
			fixture := filepath.Join(root, "safe.js")
			if err := os.WriteFile(fixture, []byte("frozen fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sensitive, []byte("frozen fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			path := sensitive
			wantOpens := int32(0)
			metadata := map[string]any{}
			switch name {
			case "relative":
				path, wantOpens = "safe.js", 1
			case "tilde":
				path = "~/.ssh/" + filepath.Base(sensitive)
			case "symlink":
				path = filepath.Join(root, "link.js")
				if err := os.Symlink(sensitive, path); err != nil {
					if runtime.GOOS == "windows" && (errors.Is(err, syscall.Errno(1314)) || os.IsPermission(err)) {
						t.Skipf("Windows symlink permission unavailable: %v", err)
					}
					t.Fatal(err)
				}
			case "granted":
				metadata[policy.MetadataExtraReadRoots] = []string{filepath.Dir(sensitive)}
				wantOpens = 1
			}
			input := map[string]string{"file": path}
			if name == "inline" {
				input = map[string]string{"code": "frozen fixture"}
			}
			var opens, executed atomic.Int32
			// An arbitrary name still carries the concrete implementation's read
			// semantics; allowed reads still pass through actual constrained FS.
			declaration := &codemode.Tool{Name: "fixture_script", Sandbox: codeModeCountingRuntime{Runtime: base, opens: &opens}, Executor: codeExecutorFunc(func(_ context.Context, source codemode.Source, _ codemode.Host) (json.RawMessage, error) {
				if source.Code != "frozen fixture" {
					t.Errorf("unexpected source: %q", source.Code)
				}
				executed.Add(1)
				return []byte(`{}`), nil
			})}
			raw, _ := json.Marshal(input)
			llm := codeModeModel(false)
			llm.steps = func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "script", Name: declaration.Name, Args: string(raw)}}
				}
				return nil
			}
			registry, err := presets.NewRegistry()
			if err != nil {
				t.Fatal(err)
			}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPolicy: registry}, DefaultPolicyMode: policy.ProfileWorkspaceWrite})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "read fixture source", AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{declaration}, Metadata: metadata}})
			if err != nil {
				t.Fatal(err)
			}
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			wantExecutions := int32(0)
			if wantOpens == 1 || name == "inline" {
				wantExecutions = 1
			}
			if opens.Load() != wantOpens || executed.Load() != wantExecutions {
				t.Fatalf("file opens=%d want=%d executions=%d want=%d", opens.Load(), wantOpens, executed.Load(), wantExecutions)
			}
		})
	}
}

func assertScriptSearchOmitsDefinitionMetadata(t *testing.T, events []*session.Event) {
	t.Helper()
	found := 0
	for _, ev := range events {
		if ev.Message == nil {
			continue
		}
		for _, part := range ev.Message.Parts {
			if part.ToolResult == nil || part.ToolResult.Name != codemode.ToolName {
				continue
			}
			for _, output := range part.ToolResult.Content {
				if output.JSON == nil {
					continue
				}
				var entry map[string]json.RawMessage
				if json.Unmarshal(output.JSON.Value, &entry) != nil || entry["input_schema"] == nil {
					continue
				}
				if entry["metadata"] != nil || entry["fixture.private"] != nil {
					t.Fatalf("leaked private metadata: %s", output.JSON.Value)
				}
				found++
				t.Logf("final model-facing js_exec result: %s", output.JSON.Value)
			}
		}
	}
	if found == 0 {
		t.Fatal("no final JS search result")
	}
}

func TestJSExecSearchOmitsDefinitionMetadataNativeAndMCPRebuild(t *testing.T) {
	var nativePrefix, mcpPrefix []byte
	for _, private := range []bool{false, true} {
		t.Run(fmt.Sprint("native-", private), func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			target := tool.NamedTool{Def: tool.Definition{Name: "NativeRead", Metadata: map[string]any{"fixture.private": private}, InputSchema: map[string]any{"type": "object"}}}
			driver := &mcpScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
				if i == 0 {
					raw, _ := json.Marshal(struct {
						Instructions []model.Part
						Tools        []model.ToolSpec
					}{req.Instructions, req.Tools})
					if nativePrefix == nil {
						nativePrefix = raw
					} else if !bytes.Equal(nativePrefix, raw) {
						t.Fatal("private metadata changed native declaration prefix")
					}
					return h1Script(`return await tools.search({name:"NativeRead"});`)
				}
				return nil
			}}
			events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{target, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
			assertScriptSearchOmitsDefinitionMetadata(t, events)
		})
		t.Run(fmt.Sprint("mcp-fuzzy-exact-rebuild-", private), func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			source := h1Directory(2)
			for i, item := range source.items {
				one := item.(h1Tool)
				one.def.Metadata["fixture.private"] = private
				source.items[i] = one
			}
			driver := &h1Model{s: source, main: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return h1Script(`return await tools.search({name:"a__target"});`)
				}
				return nil
			}}
			events, _ := runCodeMode(t, store, active, h1Spec(source, driver), nil, nil)
			assertScriptSearchOmitsDefinitionMetadata(t, events)
			if mcpPrefix == nil {
				mcpPrefix = driver.prefixes[0]
			} else if !bytes.Equal(mcpPrefix, driver.prefixes[0]) {
				t.Fatal("private metadata changed MCP declaration prefix")
			}
			// New Runtime/Host/VM restores the Session binding, then exact discovery uses
			// the current wrapped definition.
			driver = &h1Model{s: source, main: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return h1Script(`return await tools.search({name:"z__target"});`)
				}
				return nil
			}}
			events, _ = runCodeMode(t, store, active, h1Spec(source, driver), nil, nil)
			assertScriptSearchOmitsDefinitionMetadata(t, events)
		})
	}
}

func TestJSExecSameTargetCallsOverlapByDefault(t *testing.T) {
	testJSExecConcurrentTargets(t, true)
}

func TestJSExecDistinctDefaultTargetsOverlap(t *testing.T) {
	testJSExecConcurrentTargets(t, false)
}

func testJSExecConcurrentTargets(t *testing.T, sameTarget bool) {
	t.Helper()
	for _, mcp := range []bool{false, true} {
		t.Run(fmt.Sprint("mcp-", mcp), func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			started, release := make(chan struct{}, 2), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			var effects, completed atomic.Int32
			def := tool.Definition{Name: "Read", InputSchema: map[string]any{"type": "object"}}
			if mcp {
				def = fixtureMCPDefinition("parallel-default")
				def.Name = "z__target"
			}
			target := tool.NamedTool{Def: def, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
				effects.Add(1)
				started <- struct{}{}
				select {
				case <-release:
					completed.Add(1)
					return tool.Result{Content: []tool.ContentBlock{tool.Text("ok")}}, nil
				case <-ctx.Done():
					return tool.Result{}, ctx.Err()
				}
			}}
			observed := make(chan error, 1)
			go func() {
				defer unblock()
				wait, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				for range 2 {
					select {
					case <-started:
					case <-wait.Done():
						observed <- fmt.Errorf("both calls did not enter tool execution: %w", wait.Err())
						return
					}
				}
				observed <- nil
			}()
			other := target
			if !sameTarget {
				other.Def = tool.CloneDefinition(def)
				other.Def.Name += "Other"
			}
			code := fmt.Sprintf(`await tools.search({name:%q}); await tools.search({name:%q}); const replies = await Promise.all([tools.call(%q, {}), tools.call(%q, {})]); return {count: replies.length};`, def.Name, other.Def.Name, def.Name, other.Def.Name)
			targets := []tool.Tool{target}
			if !sameTarget {
				targets = append(targets, other)
			}
			var spec agent.AgentSpec
			if mcp {
				source := &h1Source{items: targets}
				driver := &h1Model{s: source, main: func(i int, _ *model.Request) []model.ToolCall {
					if i == 0 {
						return h1Script(code)
					}
					return nil
				}}
				spec = h1Spec(source, driver)
			} else {
				driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
					if i == 0 {
						return h1Script(code)
					}
					return nil
				}}
				spec = agent.AgentSpec{Model: driver, Tools: append(targets, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}})}
			}
			events, _ := runCodeMode(t, store, active, spec, nil, nil)
			if err := <-observed; err != nil {
				t.Fatal(err)
			}
			if effects.Load() != 2 || completed.Load() != 2 {
				t.Fatalf("effects=%d completed=%d", effects.Load(), completed.Load())
			}
			found := false
			for _, event := range events {
				if event.Tool != nil && event.Tool.Name == codemode.ToolName && event.Type == session.EventTypeToolResult {
					if event.Message != nil {
						for _, part := range event.Message.Parts {
							if part.ToolResult != nil && !part.ToolResult.IsError {
								found = true
							}
						}
					}
				}
			}
			if !found {
				t.Fatal("concurrent script did not produce a successful result")
			}
		})
	}
}
