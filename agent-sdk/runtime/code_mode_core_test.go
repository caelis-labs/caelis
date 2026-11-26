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
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

func runCodeMode(t *testing.T, store session.Service, active session.Session, spec agent.AgentSpec, registry policy.Registry, requester approval.Requester) ([]*session.Event, *Runtime) {
	t.Helper()
	if registry == nil {
		registry = mcpFixturePolicy()
	}
	cfg := Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPolicy: registry}, DefaultPolicyMode: "mcp-test"}
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

func TestCodeModeDirectBindingSurvivesResumeAndRequiresExplicitRefresh(t *testing.T) {
	store, active, root := codeModeStore(t)
	source := &runtimeDeferredSource{}
	effects := 0
	publish := func(version, kind string) {
		def := fixtureMCPDefinition(version)
		def.InputSchema = map[string]any{"type": "object", "additionalProperties": false,
			"required": []string{"value"}, "properties": map[string]any{"value": map[string]any{"type": kind}}}
		source.publish(tool.NamedTool{Def: def, Invoke: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			effects++
			if parent := tool.ParentFromContext(ctx); call.Name != "docs__read" || toolbinding.Target(ctx) == nil || parent == nil || parent.Kind != codemode.ToolKind {
				t.Errorf("lost canonical identity: %+v", call)
			}
			return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: call.Input}}}, nil
		}})
	}
	publish("direct-v1", "integer")
	run := func(sessions session.Service, scripts ...string) *mcpScriptModel {
		t.Helper()
		driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
			if i < len(scripts) {
				return h1Script(scripts[i])
			}
			return nil
		}}
		runCodeMode(t, sessions, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{
			&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}},
		}, DeferredTools: source}, nil, nil)
		return driver
	}
	first := run(store, `return await docs.read({value:1});`, `return await tools.call("docs__read",{value:2});`)
	if effects != 2 {
		t.Fatalf("direct calls without search: effects=%d", effects)
	}
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	if binding := codeModeBindings(events)["docs__read"]; binding != mcp.BindingForDefinition(source.Tools()[0].Definition()) {
		t.Fatalf("actual Journal did not retain direct binding: %+v", binding)
	}
	for _, event := range events {
		if event.Type == session.EventTypeToolResult && event.Tool != nil && event.Tool.Name == mcp.SearchToolName {
			t.Fatal("direct path performed discovery")
		}
	}
	// The Store is reopened and a new Runtime is constructed for every run.
	// Compare rebuilt model context with the actual prior request frontier.
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	resumed := run(reopened, `return await tools.plugins.fixture.docs.read({value:3});`)
	live := first.requests[len(first.requests)-1].Messages
	var callIDs, replyIDs []string
	for _, msg := range live {
		for _, call := range msg.ToolCalls() {
			if call.Name != codemode.ToolName {
				t.Fatalf("model history exposed child tool %q", call.Name)
			}
			callIDs = append(callIDs, call.ID)
		}
		for _, reply := range msg.ToolResults() {
			if reply.Name != codemode.ToolName {
				t.Fatalf("model history exposed child result %q", reply.Name)
			}
			replyIDs = append(replyIDs, reply.ToolUseID)
		}
	}
	if len(callIDs) != 2 || !slices.Equal(callIDs, replyIDs) {
		t.Fatalf("model call IDs %v, reply IDs %v", callIDs, replyIDs)
	}
	want := append(model.CloneMessages(live), model.NewTextMessage(model.RoleAssistant, "done"), model.NewTextMessage(model.RoleUser, "synthetic Code Mode task"))
	if !reflect.DeepEqual(resumed.requests[0].Messages, want) {
		t.Fatal("reopening the Session changed the runtime-produced model context")
	}
	if effects != 3 {
		t.Fatalf("same binding did not resume: effects=%d", effects)
	}
	publish("direct-v2", "string")
	stale := run(reopened, `return await docs.read({value:4});`)
	if effects != 3 {
		t.Fatal("old arguments executed against replacement schema")
	}
	raw, _ := json.Marshal(stale.requests[len(stale.requests)-1].Messages)
	if !strings.Contains(string(raw), "schema changed") || !strings.Contains(string(raw), "tools.search") {
		t.Fatalf("missing explicit refresh diagnostic: %s", raw)
	}
	run(reopened, `const schema=await tools.search({name:"docs__read"});if(schema.name!=="docs__read")throw Error('schema unavailable');const args={value:"new"};return await eval(schema.call_path);`)
	if effects != 4 {
		t.Fatalf("explicit refresh did not admit new arguments: effects=%d", effects)
	}
	assertSubagentSagaModelRoundTrip(t, reopened, active.SessionRef)
}

func TestCodeModeNativeOwnerSupersedesHistoricalMCPBinding(t *testing.T) {
	prior := fixtureMCPDefinition("v1")
	binding := mcp.BindingForDefinition(prior)
	current := tool.NamedTool{Def: tool.Definition{Name: prior.Name, InputSchema: map[string]any{"type": "object"}}}
	host := &codeModeHost{ctx: t.Context(), limits: codemode.DefaultLimits(), targets: map[string]codeModeTarget{}, owner: runtimeCodeModeTool{
		availableTools: func() []tool.Tool { return []tool.Tool{current} },
		lookup:         func(context.Context, string) (tool.Tool, bool, error) { return current, true, nil },
		checkBinding: func(context.Context, tool.Binding) error {
			t.Error("native owner consulted obsolete MCP authority")
			return tool.NewError(tool.ErrorCodeStaleRevision, "MCP binding is no longer present")
		},
	}}
	events := []*session.Event{{Type: session.EventTypeToolCall, Visibility: session.VisibilityMirror, Tool: &session.EventTool{
		Name: prior.Name, Binding: &binding, Status: "pending", Parent: &session.EventParentTool{Kind: codemode.ToolKind},
	}}}
	if err := host.prepareTargets(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	if _, err := host.resolveTarget(t.Context(), prior.Name, false); err != nil {
		t.Fatalf("current native owner blocked: %v", err)
	}
	if _, err := host.Search(t.Context(), codemode.SearchRequest{Name: prior.Name}); err != nil {
		t.Fatalf("native schema query: %v", err)
	}
	if _, err := host.resolveTarget(t.Context(), prior.Name, false); err != nil {
		t.Fatalf("native schema query restored obsolete MCP binding: %v", err)
	}
}

// Exercise the real encoder, process protocol, Runtime admission and Write tool.
// The model supplies deterministic source; no live DTW or model is involved.
func TestCodeModeLargeWriteArguments(t *testing.T) {
	for _, tc := range []struct {
		name, call  string
		largeOutput bool
	}{
		{name: "direct", call: `Write(args)`},
		{name: "native", call: `tools.native.Write(args)`},
		{name: "tools_call", call: `tools.call("Write",args)`},
		{name: "output_budget", call: `Write(args)`, largeOutput: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			backend, err := host.New(host.Config{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			write, err := filesystem.NewWrite(backend)
			if err != nil {
				t.Fatal(err)
			}
			code := `const records=Array.from({length:80},(_,index)=>({index,text:"采集".repeat(600)}));const args={path:"records.json",content:JSON.stringify(records)};await ` + tc.call + `;`
			if tc.largeOutput {
				code += `return "x".repeat(2048);`
			} else {
				code += `return {written:records.length};`
			}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i != 0 {
					return nil
				}
				raw, _ := json.Marshal(map[string]string{"code": code})
				return []model.ToolCall{{ID: "large-write", Name: codemode.ToolName, Args: string(raw)}}
			}}
			limits := codemode.Limits{OutputBytes: 1024}
			runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{write, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Limits: limits}}}, nil, nil)
			type record struct {
				Index int    `json:"index"`
				Text  string `json:"text"`
			}
			var records []record
			for index := range 80 {
				records = append(records, record{index, strings.Repeat("采集", 600)})
			}
			want, err := json.Marshal(records)
			if err != nil {
				t.Fatal(err)
			}
			if len(want) <= 64<<10 || len(want) <= limits.OutputBytes {
				t.Fatal("fixture must exceed both 64 KiB and the configured output budget")
			}
			if len(driver.requests) != 2 {
				t.Fatalf("model requests=%d", len(driver.requests))
			}
			result := errorDeliveryModelResult(t, driver.requests[1], "large-write")
			output, _ := json.Marshal(result.Content)
			got, err := os.ReadFile(filepath.Join(root, "records.json"))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("Write did not preserve the %d-byte JSON content: bytes=%d err=%v output=%s", len(want), len(got), err, output)
			}
			t.Logf("verified %d-byte JSON Write", len(want))
			if result.IsError != tc.largeOutput {
				t.Fatalf("IsError=%v output=%s", result.IsError, output)
			}
			if tc.largeOutput {
				if !bytes.Contains(output, []byte("JSON byte budget exceeded")) {
					t.Fatalf("output budget was not enforced: %s", output)
				}
			} else if !bytes.Contains(output, []byte(`"written":80`)) {
				t.Fatalf("script did not complete: %s", output)
			}
		})
	}
}

func TestCodeModeGojaUsesFrozenOccurrence(t *testing.T) {
	for _, approve := range []bool{false, true} {
		t.Run(map[bool]string{false: "allow", true: "approval"}[approve], func(t *testing.T) {
			files, active, root := codeModeStore(t)
			var actualParent *tool.ParentCall
			var policyExecution, approvalExecution tool.InvocationContext
			rawFull := json.RawMessage(`{"n":9007199254740993123456789,"body":"` + strings.Repeat("x", 45000) + `"}`)
			completeFull := tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: rawFull}, tool.Image{Data: []byte("abc"), MIMEType: "image/png", Name: "fixture"}}}
			target := tool.NamedTool{Def: tool.Definition{Name: "ReadFact", InputSchema: map[string]any{"type": "object"}}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
				actualParent = tool.ParentFromContext(ctx)
				return completeFull, nil
			}}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: `{"code":"const b=await tools.call('ReadFact',{});if(b.data.body.length!==45000)throw Error('truncated full Body');return {length:b.data.body.length};"}`}}
				}
				return nil
			}}
			registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "allow", Decide: func(ctx context.Context, input policy.ToolContext) (policy.Decision, error) {
				if input.Tool.Name == codemode.ToolName {
					policyExecution = tool.InvocationFromContext(ctx)
					if approve {
						return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{}}, nil
					}
				}
				return policy.Decision{Action: policy.ActionAllow}, nil
			}}}
			rt, err := New(Config{Sessions: files, TaskStore: sessionfile.NewTaskStore(files), AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPolicy: registry}, DefaultPolicyMode: "allow"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = rt.ReleaseCodeModeEnvironments() })
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, ApprovalRequester: approvalRequesterFunc(func(_ context.Context, req approval.Request) (approval.Response, error) {
				approvalExecution = req.Execution
				return approval.Response{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
			}), AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{target, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
			events, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			pairs, err := session.PairToolFacts(events)
			if err != nil {
				t.Fatal(err)
			}
			var outer, nested *session.Event
			for _, pair := range pairs {
				if pair.Call.Tool.Name == codemode.ToolName {
					outer = pair.Call
				}
				if pair.Call.Tool.Name == "ReadFact" {
					nested = pair.Call
				}
			}

			for _, pair := range pairs {
				if pair.Call.Tool.Name == "ReadFact" {
					if pair.Result == nil || !reflect.DeepEqual(pair.Result.Tool.Result, &completeFull) {
						t.Fatal("full nested result lost structured content, media or numeric tokens on reopen")
					}
				}
			}

			if outer == nil || nested == nil || actualParent == nil {
				t.Fatalf("missing actual chain: outer=%v nested=%v actual=%+v", outer, nested, actualParent)
			}
			t.Logf("frozen parent=%+v; policy occurrence=%+v; dispatched parent=%+v; persisted child parent=%+v", outer.Tool.Execution, policyExecution, actualParent.ItemID, nested.Tool.Parent.Execution)
			for _, pair := range pairs {
				if pair.Call.Tool.Name == codemode.ToolName && (pair.Result == nil || pair.Result.Tool.Execution != outer.Tool.Execution) {
					t.Fatal("outer result changed frozen occurrence")
				}
			}
			if approve && approvalExecution != outer.Tool.Execution {
				t.Fatal("approval changed frozen occurrence")
			}
			for _, e := range events {
				if e.Approval != nil && e.Approval.ToolName == codemode.ToolName && e.Approval.ItemID != outer.Tool.Execution.ItemID {
					t.Fatal("persisted approval changed frozen occurrence")
				}
			}
			expectedTask, err := taskIDForOperation(active.SessionRef, "code-mode/"+outer.Tool.Execution.TurnID+"/"+outer.Tool.Execution.ItemID+"/"+outer.Tool.ID)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := sessionfile.NewTaskStore(reopened).ListSession(t.Context(), active.SessionRef)
			if err != nil || len(entries) != 1 || entries[0].TaskID != expectedTask {
				t.Fatalf("Task identity differs from canonical occurrence: %+v %v", entries, err)
			}
			assertSubagentSagaModelRoundTrip(t, reopened, active.SessionRef)
			if outer.Tool.Execution != policyExecution {
				t.Error("Policy used an occurrence different from the accepted ToolCall")
			}
			if outer.Tool.Execution.ItemID != actualParent.ItemID || outer.Tool.Execution != nested.Tool.Parent.Execution {
				t.Fatal("nested Goja call parent points to an occurrence never accepted in Session")
			}

		})
	}
}

func TestCodeModeProducerRejectsUnpreparedOrChangedCall(t *testing.T) {
	for _, scenario := range []string{"caller_identity", "changed_identity", "changed_input", "changed_parent", "changed_binding"} {
		t.Run(scenario, func(t *testing.T) {
			called := false
			original := tool.Call{ID: "script", Name: codemode.ToolName, Input: []byte(`{"code":"return 1"}`)}
			execution, parent, target := tool.InvocationContext{ItemID: "frozen"}, tool.ParentCall{ID: "parent"}, tool.Binding{Name: "binding"}
			ctx := toolbinding.WithPrepared(toolbinding.WithOccurrence(t.Context(), execution, &parent, &target), original, nil, nil)
			call := tool.CloneCall(original)
			switch scenario {
			case "caller_identity":
				ctx = toolbinding.WithOccurrence(t.Context(), execution, &parent, &target)
			case "changed_identity":
				execution.ItemID = "forged"
			case "changed_input":
				original.Input[0] = '['
				call.Input = original.Input
			case "changed_parent":
				parent.ID = "forged"
			case "changed_binding":
				target.Name = "forged"
			}
			ctx = toolbinding.WithOccurrence(ctx, execution, &parent, &target)
			producer := codeModeProducerTool{owner: t.Context(), scope: newTaskContinuationScope(), base: tool.NamedTool{Def: tool.Definition{Name: codemode.ToolName}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { called = true; return tool.Result{}, nil }}}
			if _, err := producer.Call(ctx, call); err == nil || called {
				t.Fatalf("unprepared producer dispatched: %v %v", called, err)
			}
		})
	}
}

func TestCodeModeEnvironmentAcrossRunsResetAndRelease(t *testing.T) {
	store, active, root := codeModeStore(t)
	rt, err := New(Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store), AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPolicy: mcpFixturePolicy()}, DefaultPolicyMode: "mcp-test"})
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
	for _, step := range []struct{ code, want string }{
		{`const cmds=["build"];const previous=()=>cmds;return cmds;`, `["build"]`},
		{`cmds=[];return cmds;`, `[]`},
		{`const cmds=["test"];return [cmds,previous()];`, `[["test"],["build"]]`},
		{`return cmds;`, `["test"]`},
	} {
		if out := execute(active.SessionRef, step.code, false); !strings.Contains(out, step.want) {
			t.Fatalf("%s: got %s, want %s", step.code, out, step.want)
		}
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
	after := execute(active.SessionRef, `return {state:runtime.state(),values:[typeof retained,typeof cmds,typeof previous]};`, false)
	if !strings.Contains(after, `["undefined","undefined","undefined"]`) || generationOf(t, before) != generationOf(t, firstState) || generationOf(t, before) == generationOf(t, after) {
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
	if third.environment.results.retained != 0 || len(third.environment.results.replyParts) != 0 {
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
	if env.results.retained != 6 || len(env.results.replyParts) != 1 || len(env.results.handles) != 0 {
		t.Fatalf("reset generation retained bytes=%d replies=%d handles=%d", env.results.retained, len(env.results.replyParts), len(env.results.handles))
	}
}

func TestCodeModeUnknownOutcomePrecedesJavaScriptError(t *testing.T) {
	store, active, _ := codeModeStore(t)
	var effects atomic.Int32
	target := tool.NamedTool{Def: tool.Definition{Name: "Effect", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		effects.Add(1)
		return tool.Result{}, errorcode.New(errorcode.UnknownOutcome, "effect response was lost")
	}}
	var checked atomic.Bool
	declaration := &codemode.Tool{Name: codemode.ToolName, Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		reply, err := host.Call(ctx, "Effect", []byte(`{}`))
		if errorcode.CodeOf(err) != errorcode.UnknownOutcome || reply.Outcome != codemode.OutcomeUnknown || !reply.IsError || reply.CallID == "" {
			return nil, errors.New("Host lost the unknown effect disposition")
		}
		checked.Store(true)
		// Deterministically model a worker exception winning the race against
		// cancellation after the Host has reported an unresolved effect.
		return nil, tool.NewError(codemode.ErrorCodeJavaScript, "script rejected after the unknown effect")
	})}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "unknown-script", Name: codemode.ToolName, Args: `{"code":"fixture"}`}}
		}
		return nil
	}}
	runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{target, declaration}}, nil, nil)
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	outerUnknown, childUnknown := false, false
	for _, event := range events {
		if event.Type != session.EventTypeToolResult || event.Tool == nil {
			continue
		}
		record := event.Tool
		if record.Outcome != "unknown_outcome" {
			continue
		}
		if record.Name == codemode.ToolName && record.Parent == nil && record.ID == "unknown-script" {
			outerUnknown = true
		}
		if record.Name == "Effect" && record.Parent != nil && record.Parent.CallID == "unknown-script" {
			childUnknown = true
		}
	}
	if !checked.Load() || !outerUnknown || !childUnknown || effects.Load() != 1 {
		t.Fatalf("checked=%v outer_unknown=%v child_unknown=%v effects=%d", checked.Load(), outerUnknown, childUnknown, effects.Load())
	}
}

func TestCodeModeMCPTransportLossRetainsUnknownWithoutReplay(t *testing.T) {
	store, active, _ := codeModeStore(t)
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "unknown-effect", Version: "1"}, nil)
	server.AddTool(&mcpsdk.Tool{Name: "read", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{}, nil
	})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	var effects atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var request struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &request)
		if request.Method == "tools/call" {
			// Commit an effect after receiving the actual RPC, then lose the
			// connection before any execution response can reach the client.
			effects.Add(1)
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		handler.ServeHTTP(w, r)
	}))
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
	var checked atomic.Bool
	declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		reply, err := host.Call(ctx, "live__read", []byte(`{}`))
		if !errorcode.Is(err, errorcode.UnknownOutcome) || !reply.IsError || reply.Outcome != codemode.OutcomeUnknown || reply.CallID == "" {
			return nil, errors.New("transport uncertainty became ordinary failure")
		}
		if _, err := host.Call(context.Background(), "live__read", []byte(`{}`)); err == nil {
			return nil, errors.New("bridge admitted an uncertain-effect replay")
		}
		checked.Store(true)
		return []byte(`{}`), nil
	})}
	runCodeMode(t, store, active, agent.AgentSpec{Model: codeModeModel(true), Tools: []tool.Tool{declaration}, DeferredTools: manager}, nil, nil)
	idle := &mcpScriptModel{steps: func(int, *model.Request) []model.ToolCall { return nil }}
	runCodeMode(t, store, active, agent.AgentSpec{Model: idle, Tools: []tool.Tool{declaration}, DeferredTools: manager}, nil, nil)
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	unknown := false
	for _, event := range events {
		if event.Type == session.EventTypeToolResult && event.Tool != nil {
			r := event.Tool
			if r.Parent != nil && r.Name == "live__read" && r.Outcome == "unknown_outcome" && r.Binding != nil && r.Result != nil {
				unknown = true
			}
		}
	}
	if !checked.Load() || !unknown || effects.Load() != 1 {
		t.Fatalf("checked=%v unknown=%v effects=%d", checked.Load(), unknown, effects.Load())
	}
}

func TestCodeModeKnownReceiptPreservesUnknownOutcomeWhenCaught(t *testing.T) {
	store, active, _ := codeModeStore(t)
	const diagnostic = "callback receipt"
	target := tool.NamedTool{Def: tool.Definition{Name: "Effect", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{Content: []tool.ContentBlock{tool.Text(diagnostic)}}, tool.WrapError(errorcode.UnknownOutcome, tool.FailedReply(diagnostic), diagnostic)
	}}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: `{"code":"try { await Effect({}); } catch (e) { return {code:e.code, text:e.result.text()}; }"}`}}
		}
		return nil
	}}
	runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{target, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
	visible := errorDeliveryModelResult(t, driver.requests[1], "script")
	raw := errorDeliveryJSON(t, visible.Content)
	if !visible.IsError || !bytes.Contains(raw, []byte(`"code":"unknown_outcome","text":"callback receipt"`)) {
		t.Fatalf("catchable unknown result was lost: %s", raw)
	}
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == session.EventTypeToolResult && event.Tool != nil && event.Tool.ID == "script" {
			if event.Tool.Outcome != session.ToolOutcomeUnknown || event.Meta[codemode.MetadataScriptCompletion] != "completed" {
				t.Fatalf("script lost outcome or completion: %+v", event.Tool)
			}
			return
		}
	}
	t.Fatal("script result missing")
}

func TestCodeModeSourceFileChangesOnNextRuntimeExecution(t *testing.T) {
	store, active, root := codeModeStore(t)
	sandbox, err := host.New(host.Config{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close()
	var sources []codemode.Source
	declaration := &codemode.Tool{Name: "js_repl", Sandbox: sandbox, Executor: codeExecutorFunc(func(_ context.Context, source codemode.Source, _ codemode.Host) (json.RawMessage, error) {
		sources = append(sources, source)
		return json.Marshal(map[string]string{"source": source.Code})
	})}
	llm := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i > 1 {
			return nil
		}
		value := "first"
		if i == 1 {
			value = "changed"
		}
		if err := os.WriteFile(filepath.Join(root, "task.js"), []byte(value), 0600); err != nil {
			t.Error(err)
			return nil
		}
		return []model.ToolCall{{ID: "script", Name: "js_repl", Args: `{"file":"task.js"}`}}
	}}
	runCodeMode(t, store, active, agent.AgentSpec{Model: llm, Tools: []tool.Tool{declaration}}, nil, nil)
	if len(sources) != 2 || sources[0].Code != "first" || sources[1].Code != "changed" || sources[0].SHA256 == sources[1].SHA256 {
		t.Fatalf("sources=%+v", sources)
	}
}

func TestCodeModeRepeatedProviderIDsKeepDistinctChildReceipts(t *testing.T) {
	store, active, _ := codeModeStore(t)
	var effects atomic.Int32
	target := tool.NamedTool{Def: tool.Definition{Name: "Read", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		effects.Add(1)
		return tool.Result{Content: []tool.ContentBlock{tool.Text("ok")}}, nil
	}}
	declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		_, err := host.Call(ctx, "Read", []byte(`{}`))
		return []byte(`{}`), err
	})}
	for range 2 {
		runCodeMode(t, store, active, agent.AgentSpec{Model: codeModeModel(false), Tools: []tool.Tool{declaration, target}}, nil, nil)
	}
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	children, terminals := map[string]bool{}, map[string]bool{}
	for _, event := range events {
		if session.IsMirror(event) && event.Tool != nil && event.Tool.Parent != nil {
			children[event.Tool.ID] = true
		}
		if event.Type == session.EventTypeToolResult && event.Tool != nil {
			r := event.Tool
			if r.Parent != nil && r.Status == "completed" {
				terminals[r.Execution.ItemID] = true
			}
		}
	}
	if effects.Load() != 2 || len(children) != 2 || len(terminals) != 2 {
		t.Fatalf("effects=%d child IDs=%d terminal identities=%d", effects.Load(), len(children), len(terminals))
	}
}

func TestCodeModeBoundaryBudgetsDuringConcurrentCalls(t *testing.T) {
	store, active, _ := codeModeStore(t)
	started, release := make(chan struct{}, 2), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var effects atomic.Int32
	target := tool.NamedTool{Def: tool.Definition{Name: "Read", InputSchema: map[string]any{"type": "object"}}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
		effects.Add(1)
		started <- struct{}{}
		select {
		case <-release:
			return tool.Result{Content: []tool.ContentBlock{tool.Text("ok")}}, nil
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	}}
	var checked atomic.Bool
	declaration := &codemode.Tool{Name: "js_repl", Limits: codemode.Limits{Calls: 3, ReplyBytes: 2048}, Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		defer unblock()
		if _, err := host.Call(ctx, "Read", []byte(`[]`)); err == nil {
			return nil, errors.New("non-object arguments accepted")
		}
		done := make(chan error, 2)
		for range 2 {
			go func() { _, err := host.Call(ctx, "Read", []byte(`{}`)); done <- err }()
		}
		for range 2 {
			select {
			case <-started:
			case err := <-done:
				return nil, fmt.Errorf("concurrent call ended before both started: %w", err)
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if _, err := host.Call(ctx, "Read", []byte(`{}`)); err == nil {
			return nil, errors.New("call budget ignored while calls pending")
		}
		unblock()
		for range 2 {
			if err := <-done; err != nil {
				return nil, err
			}
		}
		checked.Store(true)
		return []byte(`{"ok":true}`), nil
	})}
	runCodeMode(t, store, active, agent.AgentSpec{Model: codeModeModel(false), Tools: []tool.Tool{declaration, target}}, nil, nil)
	if effects.Load() != 2 || !checked.Load() {
		t.Fatalf("effects=%d budgets_checked=%v", effects.Load(), checked.Load())
	}
}

func TestCodeModeBoundaryReplyBudgets(t *testing.T) {
	store, active, _ := codeModeStore(t)
	large := tool.NamedTool{Def: tool.Definition{Name: "Large", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{Content: []tool.ContentBlock{tool.Text(strings.Repeat("x", 1024))}}, nil
	}}
	mixed := tool.NamedTool{Def: tool.Definition{Name: "Mixed", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: []byte(`{}`)}, tool.Text("small")}}, nil
	}}
	var checked atomic.Bool
	declaration := &codemode.Tool{Name: "js_repl", Limits: codemode.Limits{ReplyBytes: 2048}, Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		reply, err := host.Call(ctx, "Large", []byte(`{}`))
		if err == nil || len(reply.Parts) != 0 {
			return nil, errors.New("oversized result crossed script transfer boundary")
		}
		reply, err = host.Call(ctx, "Mixed", []byte(`{}`))
		if err != nil || len(reply.Parts) != 2 || reply.Parts[1].Text != "small" || string(reply.Parts[0].JSON) != `{}` {
			return nil, errors.New("part projection and budget used different discriminants")
		}
		checked.Store(true)
		return []byte(`{}`), nil
	})}
	runCodeMode(t, store, active, agent.AgentSpec{Model: codeModeModel(false), Tools: []tool.Tool{declaration, large, mixed}}, nil, nil)
	if !checked.Load() {
		t.Fatal("projection limit was not checked")
	}
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Type == session.EventTypeToolResult && event.Tool != nil && event.Tool.Parent != nil && strings.Contains(string(toolResultJSON(event.Tool.Result)), strings.Repeat("x", 1024)) {
			found = true
		}
	}
	if !found {
		t.Fatal("projection budgeting discarded full durable result")
	}
}

func TestCodeModeIntentDoesNotHoldConfigurationGuardDuringApprovalOrIO(t *testing.T) {
	files, active, _ := codeModeStore(t)
	var revision sync.RWMutex
	var stale atomic.Bool
	store := &reviewGenerationStore{Service: files, revisionMu: &revision, stale: &stale}
	registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
		if input.Tool.Name == "Write" {
			return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{}}, nil
		}
		return policy.Decision{Action: policy.ActionAllow}, nil
	}}}
	var approved, executed atomic.Bool
	requester := approvalRequesterFunc(func(context.Context, approval.Request) (approval.Response, error) {
		if !revision.TryLock() {
			return approval.Response{}, errors.New("configuration guard held during approval")
		}
		revision.Unlock()
		approved.Store(true)
		return approval.Response{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
	})
	write := tool.NamedTool{Def: tool.Definition{Name: "Write", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		if !revision.TryLock() {
			return tool.Result{}, errors.New("configuration guard held during external tool I/O")
		}
		revision.Unlock()
		executed.Store(true)
		return tool.Result{}, nil
	}}
	declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		_, err := host.Call(ctx, "Write", []byte(`{}`))
		return []byte(`{}`), err
	})}
	llm := codeModeModel(false)
	spec := agent.AgentSpec{Model: llm, Tools: []tool.Tool{declaration, write}}
	spec.ResolveModelRequest = func(context.Context) (agent.ModelRequestSnapshot, error) {
		return agent.ModelRequestSnapshot{Model: llm, Tools: spec.Tools, Validate: func(context.Context) (func(), error) {
			revision.RLock()
			return revision.RUnlock, nil
		}}, nil
	}
	runCodeMode(t, store, active, spec, registry, requester)
	if !approved.Load() || !executed.Load() || !store.updated.Load() {
		t.Fatalf("approval=%v execution=%v replacement_during_claim=%v", approved.Load(), executed.Load(), store.updated.Load())
	}
}

func TestCodeModeRunnerJoinsUnresponsiveExecutor(t *testing.T) {
	store, active, _ := codeModeStore(t)
	started, release := make(chan struct{}), make(chan struct{})
	declaration := &codemode.Tool{Name: "js_repl", Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, _ codemode.Host) (json.RawMessage, error) {
		close(started)
		<-release
		return nil, ctx.Err()
	})}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPolicy: mcpFixturePolicy()}, DefaultPolicyMode: "mcp-test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "cancel unresponsive adapter", AgentSpec: agent.AgentSpec{Model: codeModeModel(false), Tools: []tool.Tool{declaration}}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	wait, stop := context.WithTimeout(t.Context(), 250*time.Millisecond)
	err = run.Handle.WaitCompletion(wait)
	stop()
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Runner released an active adapter: %v", err)
	}
	if err := run.Handle.WaitCompletion(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("terminal=%v", err)
	}
}
