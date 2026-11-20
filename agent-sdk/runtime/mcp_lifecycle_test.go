package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/mcpdiscovery"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

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
	search := tool.NamedTool{Def: tool.Definition{Name: mcp.SearchToolName}, Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
		return tool.Result{ID: call.ID, Name: call.Name, Content: []tool.ContentBlock{tool.StructuredContent{Value: discovery}}, Metadata: map[string]any{mcpdiscovery.MetadataKey: true}}, nil
	}}
	proposal := model.ToolCall{ID: "routed", Name: mcp.CallToolName, Args: `{"tool_name":"docs__read","arguments":{"number":1}}`}
	const effectiveInput = `{"number":9007199254740993}`
	var dispatched string
	remote := tool.NamedTool{Def: def, Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
		dispatched = string(call.Input)
		return tool.Result{ID: call.ID, Name: call.Name, Content: []tool.ContentBlock{tool.Text("read")}}, nil
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
