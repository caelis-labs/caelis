package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/mcpdiscovery"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLegacyMCPSearchReceiptAfterStoreReopen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		marked bool
	}{
		{"marked-stale", true},
		{"unmarked-undiscovered", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			source := &changingBindingSource{}
			source.publish(tool.NamedTool{Def: fixtureMCPDefinition("v1")})
			first := &mcpScriptModel{steps: func(step int, _ *model.Request) []model.ToolCall {
				if step == 0 {
					return h1Script(`return await tools.search({name:"docs__read"});`)
				}
				return nil
			}}
			_, rt := runCodeMode(t, store, active, agent.AgentSpec{
				Model: first, DeferredTools: source,
				Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}},
			}, nil, nil)
			if err := rt.ReleaseCodeModeEnvironments(); err != nil {
				t.Fatal(err)
			}
			if len(first.requests) != 2 {
				t.Fatalf("search turn made %d model requests, want 2", len(first.requests))
			}
			storeRoot := filepath.Join(root, "sessions")
			writeLegacyMCPSearchReceipt(t, storeRoot, tc.marked)
			reopened := file.NewStore(file.Config{RootDir: storeRoot})
			if !tc.marked {
				events, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
				if err != nil {
					t.Fatal(err)
				}
				if got := codeModeBindings(events); len(got) != 0 {
					t.Fatalf("unmarked receipt restored a binding: %v", got)
				}
			}

			var effects atomic.Int32
			source.publish(tool.NamedTool{Def: fixtureMCPDefinition("v2"), Invoke: func(context.Context, tool.Call) (tool.Result, error) {
				effects.Add(1)
				return tool.Result{}, nil
			}})
			resumed := &mcpScriptModel{steps: func(step int, _ *model.Request) []model.ToolCall {
				if tc.marked && step == 0 {
					return []model.ToolCall{{ID: "after-upgrade", Name: codemode.ToolName, Args: `{"code":"return await tools.call('docs__read', {});"}`}}
				}
				return nil
			}}
			runCodeMode(t, reopened, active, agent.AgentSpec{
				Model: resumed, DeferredTools: source,
				Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}},
			}, nil, nil)
			want := append(model.CloneMessages(first.requests[1].Messages), model.NewTextMessage(model.RoleAssistant, "done"), model.NewTextMessage(model.RoleUser, "synthetic Code Mode task"))
			if len(resumed.requests) == 0 || !reflect.DeepEqual(resumed.requests[0].Messages, want) {
				t.Fatal("reopened history differs from the original model context")
			}
			if effects.Load() != 0 {
				t.Fatalf("reopened discovery dispatched %d effects against the replacement", effects.Load())
			}
			if tc.marked {
				result := errorDeliveryModelResult(t, resumed.requests[len(resumed.requests)-1], "after-upgrade")
				parts := errorDeliveryJSON(t, result.Content)
				if !result.IsError || !bytes.Contains(parts, []byte(`"code":"stale_revision"`)) {
					t.Fatalf("resumed model received %s, want stale_revision", parts)
				}
			}
		})
	}
}

func writeLegacyMCPSearchReceipt(t *testing.T, root string, marked bool) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, "*", "*", "*", "*", "*.events.jsonl"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("finding fixture event log: %v, %v", paths, err)
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(raw, []byte{'\n'})
	count := 0
	for i, line := range lines {
		var event session.Event
		if len(line) == 0 {
			continue
		}
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type != session.EventTypeToolResult || event.Tool == nil || event.Tool.Name != mcp.SearchToolName {
			continue
		}
		if event.Tool.Result == nil || !event.Tool.Discovery || event.Tool.Outcome != session.ToolOutcomeSucceeded {
			t.Fatal("fixture did not produce a successful discovery receipt")
		}
		// v0.83.1 wrote plain model parts with the trusted marker in result.metadata.
		// Replace only this wire record so the resumed Store must decode that shape.
		legacy := struct {
			Content  []model.Part    `json:"content"`
			Metadata map[string]bool `json:"metadata,omitempty"`
		}{Content: tool.ModelParts(event.Tool.Result.Content)}
		if marked {
			legacy.Metadata = map[string]bool{mcpdiscovery.MetadataKey: true}
		}
		row := struct {
			*session.Event
			Tool struct {
				*session.EventTool
				Result any `json:"result"`
			} `json:"tool"`
		}{Event: &event}
		event.Tool.Discovery = false
		row.Tool.EventTool, row.Tool.Result = event.Tool, legacy
		lines[i], err = json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 1 {
		t.Fatalf("found %d discovery receipts, want 1", count)
	}
	if err := os.WriteFile(paths[0], bytes.Join(lines, []byte{'\n'}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMCPPreparedEnvelopeSurvivesFileReopen(t *testing.T) {
	ctx := t.Context()
	storeConfig := file.Config{RootDir: t.TempDir()}
	store := file.NewStore(storeConfig)
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "user", Workspace: session.WorkspaceRef{Key: "test", CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	def := fixtureMCPDefinition("v1")
	discovery, err := json.Marshal(mcp.NewToolSearchResult([]tool.Definition{def}))
	if err != nil {
		t.Fatal(err)
	}
	search := tool.NamedTool{Def: tool.Definition{Name: mcp.SearchToolName}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
		toolbinding.Observed(ctx).Discovery = true
		return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: discovery}}}, nil
	}}
	proposal := model.ToolCall{ID: "routed", Name: mcp.CallToolName, Args: `{"tool_name":"docs__read","arguments":{"number":1}}`}
	const effectiveInput = `{"number":9007199254740993}`
	var dispatched string
	remote := tool.NamedTool{Def: def, Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
		dispatched = string(call.Input)
		return tool.Result{Content: []tool.ContentBlock{tool.Text("read")}}, nil
	}}
	driver := &mcpScriptModel{steps: func(step int, _ *model.Request) []model.ToolCall {
		switch step {
		case 0:
			return []model.ToolCall{{ID: "search", Name: mcp.SearchToolName, Args: `{"query":"docs"}`}}
		case 1:
			return []model.ToolCall{proposal}
		default:
			return nil
		}
	}}
	hooks := agent.Hooks{ToolPrepare: []agent.Hook[agent.ToolPreparation, agent.ToolPreparation]{{Name: "rewrite", Apply: func(_ context.Context, evidence agent.HookContext, preparation agent.ToolPreparation) (agent.ToolPreparation, error) {
		if evidence.ToolDefinition.Name == def.Name {
			preparation.Input = json.RawMessage(effectiveInput)
		}
		return preparation, nil
	}}}}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, PolicyRegistry: mcpAskFixturePolicy(), DefaultPolicyMode: "mcp-test", Hooks: hooks})
	if err != nil {
		t.Fatal(err)
	}
	var approved string
	run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "Read a document", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{search, mcp.NewCallTool()}, DeferredTools: &runtimeDeferredSource{ready: []tool.Tool{remote}}}, ApprovalRequester: approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		approved = string(req.Call.Input)
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once"}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(ctx); err != nil {
		t.Fatal(err)
	}
	reopened := file.NewStore(storeConfig)
	events, err := reopened.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := session.PairToolFacts(events)
	if err != nil || len(pairs) != 2 {
		t.Fatalf("pairs=%v err=%v", pairs, err)
	}
	if dispatched != effectiveInput || approved != effectiveInput || len(driver.requests) != 3 {
		t.Fatalf("dispatch=%q approval=%q model requests=%d result=%+v", dispatched, approved, len(driver.requests), pairs[1].Result)
	}
	fact := pairs[1].Call.Tool
	if fact.Name != def.Name || fact.ProposedCall == nil || *fact.ProposedCall != proposal || fact.ModelCall == nil || fmt.Sprint(fact.Input["number"]) != "9007199254740993" {
		t.Fatalf("reopened call fact=%+v", fact)
	}
	routed, err := tool.ParseMCPCall(*fact.ModelCall)
	if err != nil || fact.ModelCall.Name != proposal.Name || routed.ID != proposal.ID || routed.Name != def.Name || routed.Args != effectiveInput || pairs[1].Result == nil || pairs[1].Result.Message.ToolResults()[0].Name != proposal.Name {
		t.Fatalf("reopened envelope=%+v routed=%+v err=%v", fact.ModelCall, routed, err)
	}
	probe := &recoveryCaptureModel{}
	resumed, err := New(Config{Sessions: reopened, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	followup := "Follow up"
	run, err = resumed.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: followup, AgentSpec: agent.AgentSpec{Model: probe}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(ctx); err != nil {
		t.Fatal(err)
	}
	want := append(model.CloneMessages(driver.requests[2].Messages), model.NewTextMessage(model.RoleAssistant, "done"), model.NewTextMessage(model.RoleUser, followup))
	if !reflect.DeepEqual(probe.messages, want) {
		t.Fatalf("replayed messages=%#v, want %#v", probe.messages, want)
	}
}

func TestMCPFixedCallRejectsBindingChangedDuringApproval(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	store, active := newTestSessionService(t, "mcp-stale-approval")
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "docs", Version: "1"}, nil)
	var effects atomic.Int32
	publish := func(schema map[string]any) {
		server.AddTool(&mcpsdk.Tool{Name: "read", InputSchema: schema}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			effects.Add(1)
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "ok"}}}, nil
		})
	}
	publish(map[string]any{"type": "object"})
	endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
	defer endpoint.Close()
	manager, err := mcp.NewManager(ctx, []mcp.ServerSpec{{PluginID: "fixture", Name: "docs", Transport: mcp.TransportStreamableHTTP, URL: endpoint.URL}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	select {
	case <-manager.Initialized():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	remote, found, err := manager.Lookup(ctx, "docs__read")
	if err != nil || !found {
		t.Fatalf("initial catalog: found=%t, err=%v", found, err)
	}
	discovery, err := json.Marshal(mcp.NewToolSearchResult([]tool.Definition{remote.Definition()}))
	if err != nil {
		t.Fatal(err)
	}
	search := tool.NamedTool{Def: tool.Definition{Name: mcp.SearchToolName}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
		toolbinding.Observed(ctx).Discovery = true
		return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: discovery}}}, nil
	}}
	driver := &mcpScriptModel{steps: func(step int, _ *model.Request) []model.ToolCall {
		switch step {
		case 0:
			return []model.ToolCall{{ID: "search", Name: mcp.SearchToolName, Args: `{"query":"docs"}`}}
		case 1:
			return []model.ToolCall{{ID: "routed", Name: mcp.CallToolName, Args: `{"tool_name":"docs__read","arguments":{}}`}}
		default:
			return nil
		}
	}}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, PolicyRegistry: mcpAskFixturePolicy(), DefaultPolicyMode: "mcp-test"})
	if err != nil {
		t.Fatal(err)
	}
	var approvals atomic.Int32
	run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "Read a document", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{search, mcp.NewCallTool()}, DeferredTools: manager}, ApprovalRequester: approvalRequesterFunc(func(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		approvals.Add(1)
		// Keep the model's arguments valid for both schemas so dispatching to the
		// new definition would reach the handler, not merely fail its validation.
		publish(map[string]any{"type": "object", "properties": map[string]any{"hint": map[string]any{"type": "string"}}})
		if err := manager.RefreshTools(ctx, "fixture", "docs"); err != nil {
			return agent.ApprovalResponse{}, err
		}
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once"}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := store.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := session.PairToolFacts(events)
	if err != nil || len(pairs) != 2 || pairs[1].Result == nil {
		t.Fatalf("missing routed result: pairs=%v err=%v", pairs, err)
	}
	if got := pairs[1].Result.Tool; got.ErrorCode != string(tool.ErrorCodeStaleRevision) || approvals.Load() != 1 || effects.Load() != 0 {
		t.Fatalf("stale fixed call: error=%q approvals=%d effects=%d", got.ErrorCode, approvals.Load(), effects.Load())
	}
}

func TestMCPFixedUnknownRecoveryKeepsOuterCallWithoutReplay(t *testing.T) {
	service, active := newToolFactTestSession(t, "fixed-unknown")
	original := model.ToolCall{ID: "provider-operation", Name: mcp.CallToolName, Args: `{"tool_name":"docs__read","arguments":{"number":9007199254740993}}`}
	input := map[string]any{"number": json.Number("9007199254740993")}
	message := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{original}, "")
	fact := &session.Event{ID: "original-call", Type: session.EventTypeToolCall, Scope: &session.EventScope{TurnID: "original-turn"}, Message: &message, Tool: &session.EventTool{ID: original.ID, Name: "docs__read", Input: input, ModelCall: &original, Binding: &tool.Binding{Name: "docs__read", Identity: "identity", Version: "v1"}, Execution: tool.InvocationContext{SessionID: active.SessionID, TurnID: "original-turn", ItemID: "original-occurrence"}}}
	if _, err := service.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: fact}); err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{sessions: service, clock: time.Now}
	fence, err := service.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "new-owner"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := session.ContextWithRuntimeFence(t.Context(), fence)
	effects := 0
	if err := rt.repairToolPairs(ctx, active.SessionRef); err != nil {
		t.Fatal(err)
	}
	events, err := service.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if effects != 0 || last.Tool.Outcome != "unknown_outcome" || last.Tool.Name != "docs__read" || last.Tool.ModelCall == nil || last.Message.ToolResults()[0].Name != mcp.CallToolName || fmt.Sprint(last.Tool.Input["number"]) != "9007199254740993" {
		t.Fatalf("unknown recovery changed identity or replayed: effects=%d event=%+v", effects, last.Tool)
	}
}
