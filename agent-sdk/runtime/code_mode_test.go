package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// This executor is deliberately not a language backend. It drives the common
// protocol deterministically through a real Runtime, policy and Session store.
type codeExecutorFunc func(context.Context, codemode.Source, codemode.Host) (json.RawMessage, error)

type codeModeImageModel struct{ *mcpScriptModel }

func (codeModeImageModel) Capabilities() model.Capabilities {
	c := runtimeTestModelCapabilities()
	c.ImageInput = true
	return c
}

func (f codeExecutorFunc) Execute(ctx context.Context, source codemode.Source, _ codemode.Limits, host codemode.Host) (codemode.Output, error) {
	raw, err := f(ctx, source, host)
	return codemode.Output{Value: raw}, err
}

type codeOutputExecutorFunc func(context.Context, codemode.Source, codemode.Host) (codemode.Output, error)

func (f codeOutputExecutorFunc) Execute(ctx context.Context, source codemode.Source, _ codemode.Limits, host codemode.Host) (codemode.Output, error) {
	return f(ctx, source, host)
}

func codeModeModel(search bool) *mcpScriptModel {
	return &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if search && i == 0 {
			return []model.ToolCall{{ID: "search", Name: mcp.SearchToolName, Args: `{"query":"live__read"}`}}
		}
		if (!search && i == 0) || (search && i == 1) {
			return []model.ToolCall{{ID: "script", Name: "js_repl", Args: `{"code":"fixture"}`}}
		}
		return nil
	}}
}

func codeModeStore(t *testing.T) (*sessionfile.Store, session.Session, string) {
	t.Helper()
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "fixture", Workspace: session.WorkspaceRef{Key: "fixture", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	return store, active, root
}

func runCodeMode(t *testing.T, store session.Service, active session.Session, spec agent.AgentSpec, registry policy.Registry, requester agent.ApprovalRequester) ([]*session.Event, *Runtime) {
	t.Helper()
	if registry == nil {
		registry = mcpFixturePolicy()
	}
	cfg := Config{Sessions: store, AgentFactory: chat.Factory{}, PolicyRegistry: registry, DefaultPolicyMode: "mcp-test"}
	if files, ok := store.(*sessionfile.Store); ok {
		cfg.TaskStore = sessionfile.NewTaskStore(files)
	}
	rt, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.ReleaseCodeModeEnvironments() })
	var mu sync.Mutex
	var live []*session.Event
	observer := agent.SourceEventObserverFunc(func(_ context.Context, event agent.SourceEvent) error {
		if event.Canonical != nil {
			mu.Lock()
			live = append(live, session.CloneEvent(event.Canonical))
			mu.Unlock()
		}
		return nil
	})
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "synthetic Code Mode task", AgentSpec: spec, ApprovalRequester: requester, SourceObserver: observer})
	if err != nil {
		t.Fatal(err)
	}
	err = run.Handle.WaitCompletion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]*session.Event(nil), live...), rt
}

func TestCodeModeRuntimeMCPAggregationAndMediaReceipts(t *testing.T) {
	store, active, root := codeModeStore(t)
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "code-mode", Version: "1"}, nil)
	var effects atomic.Int32
	var bitmap bytes.Buffer
	if err := png.Encode(&bitmap, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	server.AddTool(&mcpsdk.Tool{Name: "read", InputSchema: map[string]any{"type": "object", "required": []any{"value"}, "properties": map[string]any{"value": map[string]any{"type": "integer"}}}}, func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		effects.Add(1)
		var args struct {
			Value int `json:"value"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, err
		}
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.ImageContent{Data: bitmap.Bytes(), MIMEType: "image/png"}}, StructuredContent: map[string]any{"value": args.Value, "receipt": json.Number("9223372036854775807")}}, nil
	})
	endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
	defer endpoint.Close()
	manager, err := mcp.NewManager(t.Context(), []mcp.ServerSpec{{PluginID: "fixture", Name: "live", Transport: mcp.TransportStreamableHTTP, URL: endpoint.URL}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	<-manager.Initialized()
	if len(manager.Tools()) != 1 {
		t.Fatalf("MCP not ready: %+v", manager.GetServerInfos("fixture"))
	}
	sandbox, err := host.New(host.Config{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close()
	read, err := filesystem.NewRead(filesystem.DefaultReadConfig(), sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("code-mode-native"), 0600); err != nil {
		t.Fatal(err)
	}
	var policyNames []string
	registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(ctx context.Context, input policy.ToolContext) (policy.Decision, error) {
		if parent := tool.ParentFromContext(ctx); parent != nil {
			execution := tool.InvocationFromContext(ctx)
			if parent.ID != "script" || execution.SessionID != active.SessionID || execution.ItemID == "" {
				t.Errorf("lost nested identity: %+v", input.Call)
			}
			policyNames = append(policyNames, input.Tool.Name)
		}
		return policy.Decision{Action: policy.ActionAllow}, nil
	}}}
	declaration := &codemode.Tool{Name: "js_repl", Executor: codeOutputExecutorFunc(func(ctx context.Context, source codemode.Source, host codemode.Host) (codemode.Output, error) {
		if source.Code != "fixture" || source.SHA256 == "" {
			return codemode.Output{}, errors.New("source was not frozen")
		}
		total := 0
		var selected codemode.Part
		for _, value := range []int{2, 3} {
			raw, _ := json.Marshal(map[string]any{"value": value})
			reply, err := host.Call(ctx, "live__read", raw)
			if err != nil {
				return codemode.Output{}, err
			}
			if reply.IsError || len(reply.Parts) != 2 || reply.Parts[0].Handle == "" || !bytes.Contains(reply.Parts[1].JSON, []byte("9223372036854775807")) {
				return codemode.Output{}, errors.New("MCP JSON/media projection lost")
			}
			wire, _ := json.Marshal(reply)
			if bytes.Contains(wire, []byte("base64")) || bytes.Contains(wire, []byte("iVBOR")) {
				return codemode.Output{}, errors.New("image leaked into script JSON")
			}
			var result struct {
				Value int `json:"value"`
			}
			if err := json.Unmarshal(reply.Parts[1].JSON, &result); err != nil {
				return codemode.Output{}, err
			}
			total += result.Value
			if value == 3 {
				selected = reply.Parts[0]
			}
		}
		readReply, err := host.Call(ctx, "Read", []byte(`{"path":"input.txt"}`))
		if err != nil || readReply.IsError {
			return codemode.Output{}, errors.New("real filesystem read failed")
		}
		raw, err := json.Marshal(map[string]any{"sum": total, "selected": selected})
		return codemode.Output{Value: raw, Parts: []codemode.Part{selected}}, err
	})}
	llm := codeModeModel(true)
	live, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: codeModeImageModel{llm}, Tools: []tool.Tool{declaration, read}, DeferredTools: manager}, registry, nil)
	if effects.Load() != 2 || !reflect.DeepEqual(policyNames, []string{"live__read", "live__read", "Read"}) {
		t.Fatalf("effects=%d policy=%v", effects.Load(), policyNames)
	}
	seenMirror := false
	for _, event := range live {
		if session.IsMirror(event) && event.Tool != nil && event.Tool.Name == "live__read" && event.Tool.Parent != nil {
			seenMirror = true
		}
	}
	if !seenMirror {
		t.Fatal("nested tool names/arguments were not published live")
	}
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	loaded, err := reopened.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	terminals, outer := 0, false
	for _, event := range loaded.Events {
		if record := event.Tool; event.Type == session.EventTypeToolResult && record != nil && record.Parent != nil && record.Status == "completed" {
			terminals++
			if !session.IsMirror(event) || record.Result == nil {
				t.Fatal("child full result missing")
			}
			if record.Name == "live__read" && !bytes.Contains(toolResultJSON(record.Result), []byte("9223372036854775807")) {
				t.Fatal("full result rounded")
			}
		}

		if session.IsCanonicalHistoryEvent(event) && event.Tool != nil && event.Tool.Name == "js_repl" && event.Type == session.EventTypeToolResult {
			outer = event.Tool.Output["sum"] == float64(5)
			if event.Message == nil || len(event.Message.Parts[0].ToolResult.Content) != 2 || event.Message.Parts[0].ToolResult.Content[1].Media == nil {
				raw, _ := json.Marshal(event.Message)
				t.Fatalf("model image was flattened or omitted: %s", raw)
			}
		}
		if session.IsCanonicalHistoryEvent(event) && event.Tool != nil && event.Tool.Parent != nil {
			t.Fatal("nested display polluted model tool protocol")
		}
	}
	if terminals != 3 || !outer {
		t.Fatalf("independent terminals=%d aggregate=%v", terminals, outer)
	}
	if missingToolPairs(t, loaded.Events) != 0 {
		t.Fatal("completed nested calls appear interrupted")
	}
	// Compare actual model requests across a fresh Runtime and file Store. The
	// existing model-context owner must retain the outer aggregate and image,
	// while independently durable nested facts remain outside tool protocol.
	idle := &mcpScriptModel{steps: func(int, *model.Request) []model.ToolCall { return nil }}
	runCodeMode(t, reopened, active, agent.AgentSpec{Model: codeModeImageModel{idle}}, nil, nil)
	expected := llm.requests[len(llm.requests)-1].Messages
	actual := idle.requests[0].Messages
	if len(actual) < len(expected) || !reflect.DeepEqual(actual[:len(expected)], expected) {
		want, _ := json.Marshal(expected)
		got, _ := json.Marshal(actual)
		t.Fatalf("model context changed across reload:\nwant prefix=%s\ngot=%s", want, got)
	}
	if effects.Load() != 2 {
		t.Fatal("context reconstruction replayed an effect")
	}
}

func TestCodeModeAssembledToolsDoNotRequireSearch(t *testing.T) {
	for _, mode := range []string{"without-search", "prior-search"} {
		t.Run(mode, func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			var effects atomic.Int32
			def := fixtureMCPDefinition("v1")
			source := &runtimeDeferredSource{}
			source.publish(tool.NamedTool{Def: def, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
				effects.Add(1)
				return tool.Result{Content: []tool.ContentBlock{tool.Text("ok")}}, nil
			}})
			declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
				_, err := host.Call(ctx, "docs__read", []byte(`{}`))
				if err != nil {
					return nil, err
				}
				if _, err := host.Call(ctx, mcp.SearchToolName, []byte(`{"query":"docs__read"}`)); err == nil {
					return nil, errors.New("raw ToolSearch bypassed admission")
				}
				return []byte(`{"checked":true}`), nil
			})}
			llm := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				searchCall := model.ToolCall{ID: "search", Name: mcp.SearchToolName, Args: `{"query":"docs__read"}`}
				codeCall := model.ToolCall{ID: "script", Name: "js_repl", Args: `{"code":"fixture"}`}
				if mode == "prior-search" {
					if i == 0 {
						return []model.ToolCall{searchCall}
					}
					if i == 1 {
						return []model.ToolCall{codeCall}
					}
					return nil
				}
				if i == 0 {
					return []model.ToolCall{codeCall}
				}
				return nil
			}}
			runCodeMode(t, store, active, agent.AgentSpec{Model: llm, Tools: []tool.Tool{declaration}, DeferredTools: source}, nil, nil)
			if effects.Load() != 1 {
				t.Fatalf("effects=%d want=1", effects.Load())
			}
		})
	}
}

func TestCodeModeTerminalReceiptsSurviveDiscardedAggregate(t *testing.T) {
	store, active, root := codeModeStore(t)
	var effects atomic.Int32
	completed := tool.NamedTool{Def: tool.Definition{Name: "Write", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		effects.Add(1)
		return tool.Result{Content: []tool.ContentBlock{tool.Text("original receipt")}}, nil
	}}
	failed := tool.NamedTool{Def: tool.Definition{Name: "Fail", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: []byte(`{"failed":true}`)}}}, tool.FailedReply("fixture tool failure")
	}}
	declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		if _, err := host.Call(ctx, "Write", []byte(`{}`)); err != nil {
			return nil, err
		}
		reply, err := host.Call(ctx, "Fail", []byte(`{}`))
		if err != nil || !reply.IsError {
			return nil, errors.New("IsError lost")
		}
		return nil, errors.New("executor failed after effects")
	})}
	runCodeMode(t, store, active, agent.AgentSpec{Model: codeModeModel(false), Tools: []tool.Tool{declaration, completed, failed}}, nil, nil)
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	events, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, event := range events {
		if event.Type == session.EventTypeToolResult && event.Tool != nil && event.Tool.Parent != nil {
			statuses[event.Tool.Name] = event.Tool.Status
		}
	}
	if effects.Load() != 1 || statuses["Write"] != "completed" || statuses["Fail"] != "failed" {
		t.Fatalf("effects=%d statuses=%v", effects.Load(), statuses)
	}
	if missingToolPairs(t, events) != 0 {
		t.Fatal("discarded aggregation lost terminal receipts")
	}
}

type codeModeReceiptFailure struct {
	session.Service
	failed atomic.Bool
	status string
}

func (s *codeModeReceiptFailure) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	if req.Event.Type == session.EventTypeToolResult && req.Event.Tool != nil {
		r := req.Event.Tool
		if r.Parent != nil && (r.Status == s.status || (s.status == "" && r.Status == "completed")) && s.failed.CompareAndSwap(false, true) {
			return nil, errors.New("fixture: child terminal persistence failed")
		}
	}
	return s.Service.AppendEvent(ctx, req)
}

func TestCodeModeReceiptFailureClosesBridgeAndRecoversOriginalUnknown(t *testing.T) {
	store, active, _ := codeModeStore(t)
	failing := &codeModeReceiptFailure{Service: store}
	var effects atomic.Int32
	write := tool.NamedTool{Def: tool.Definition{Name: "Write", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		effects.Add(1)
		return tool.Result{Content: []tool.ContentBlock{tool.Text("effect happened")}}, nil
	}}
	var checked atomic.Bool
	declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		if _, err := host.Call(ctx, "Write", []byte(`{}`)); err == nil {
			return nil, errors.New("receipt error was hidden")
		}
		if _, err := host.Call(context.Background(), "Write", []byte(`{}`)); err == nil {
			return nil, errors.New("effect admitted after receipt failure")
		}
		checked.Store(true)
		return []byte(`{}`), nil // Deliberately swallow the callback error.
	})}
	runCodeMode(t, failing, active, agent.AgentSpec{Model: codeModeModel(false), Tools: []tool.Tool{declaration, write}}, nil, nil)
	idle := &mcpScriptModel{steps: func(int, *model.Request) []model.ToolCall { return nil }}
	runCodeMode(t, store, active, agent.AgentSpec{Model: idle, Tools: []tool.Tool{declaration, write}}, nil, nil)
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	var unknown bool
	var original string
	for _, event := range events {
		if event.Type != session.EventTypeToolResult || event.Tool == nil {
			continue
		}
		r := event.Tool
		if r.Parent == nil {
			continue
		}
		if original != "" && original != r.Execution.ItemID {
			t.Fatal("recovery replaced original operation identity")
		}
		original = r.Execution.ItemID
		unknown = r.Outcome == "unknown_outcome"
	}
	if !checked.Load() || !failing.failed.Load() || !unknown || effects.Load() != 1 {
		t.Fatalf("checked=%v failed=%v unknown=%v effects=%d", checked.Load(), failing.failed.Load(), unknown, effects.Load())
	}
}

func TestCodeModeSettledFailureStillRequiresDurableReceipt(t *testing.T) {
	store, active, _ := codeModeStore(t)
	failing := &codeModeReceiptFailure{Service: store, status: "failed"}
	var effects atomic.Int32
	fail := tool.NamedTool{Def: tool.Definition{Name: "Fail", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{}, tool.NewError(tool.ErrorCodeInvalidInput, "ordinary invalid field")
	}}
	write := tool.NamedTool{Def: tool.Definition{Name: "Write", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { effects.Add(1); return tool.Result{}, nil }}
	var checked atomic.Bool
	declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		if _, err := host.Call(ctx, "Fail", []byte(`{}`)); err == nil {
			return nil, errors.New("failure hidden")
		}
		if _, err := host.Call(context.Background(), "Write", []byte(`{}`)); err == nil {
			return nil, errors.New("admitted after failed receipt")
		}
		checked.Store(true)
		return []byte(`{"caught":true}`), nil
	})}
	events, _ := runCodeMode(t, failing, active, agent.AgentSpec{Model: codeModeModel(false), Tools: []tool.Tool{declaration, fail, write}}, nil, nil)
	parentFailed := false
	for _, ev := range events {
		if ev.Message != nil {
			for _, part := range ev.Message.Parts {
				if part.ToolResult != nil {
					parentFailed = parentFailed || part.ToolResult.IsError
				}
			}
		}
	}
	if !checked.Load() || !failing.failed.Load() || effects.Load() != 0 || !parentFailed {
		t.Fatalf("checked=%v failed=%v effects=%d parentFailed=%v", checked.Load(), failing.failed.Load(), effects.Load(), parentFailed)
	}
}
