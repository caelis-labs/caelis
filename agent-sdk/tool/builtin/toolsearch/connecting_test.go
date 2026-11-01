package toolsearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSearchConnectingCatalogThenReadyUsesFreshSnapshot(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	connecting, connectRelease := make(chan struct{}, 1), make(chan struct{})
	listing, listRelease := make(chan struct{}, 1), make(chan struct{})
	var connectOnce, listOnce sync.Once
	releaseConnect := func() { connectOnce.Do(func() { close(connectRelease) }) }
	releaseList := func() { listOnce.Do(func() { close(listRelease) }) }
	var businessCalls atomic.Int32
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "slow-fixture", Version: "1"}, nil)
	server.AddTool(&mcpsdk.Tool{Name: "read", Description: "Read a fixture", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"fixture_marker": map[string]any{"type": "string"}}, "required": []string{"fixture_marker"}}},
		func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			businessCalls.Add(1)
			return &mcpsdk.CallToolResult{}, nil
		})
	server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			var entered chan struct{}
			var release <-chan struct{}
			switch method {
			case "initialize":
				entered, release = connecting, connectRelease
			case "tools/list":
				entered, release = listing, listRelease
			}
			if entered != nil {
				select {
				case entered <- struct{}{}:
				default:
				}
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return next(ctx, method, req)
		}
	})
	endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
	defer endpoint.Close()
	manager, err := mcp.NewManager(ctx, []mcp.ServerSpec{{PluginID: "fixture", Name: "slow", Transport: mcp.TransportStreamableHTTP, URL: endpoint.URL}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		releaseConnect()
		releaseList()
		_ = manager.Close()
	}()
	select {
	case <-connecting:
	case <-ctx.Done():
		t.Fatal("initialize did not reach startup barrier")
	}
	search := NewSource(manager)
	llm := &selectionModel{}
	call := tool.Call{ID: "cold-search", Name: tool.ToolSearchToolName, Input: json.RawMessage(`{"query":"read fixture"}`), RuntimeModel: llm}
	cold, err := search.Call(ctx, call)
	if err != nil || string(cold.Content[0].JSON.Value) != "{}" || len(llm.requests) != 0 {
		t.Fatalf("connecting search: %+v %v selector_calls=%d", cold, err, len(llm.requests))
	}
	status := manager.GetServerInfos("fixture")
	if len(status) != 1 || status[0].Status != "connecting" || len(status[0].Tools) != 0 {
		t.Fatalf("connecting status: %+v", status)
	}
	releaseConnect()
	select {
	case <-listing:
	case <-ctx.Done():
		t.Fatal("tools/list did not reach catalog barrier")
	}
	status = manager.GetServerInfos("fixture")
	if len(status) != 1 || status[0].Status != "running" || !status[0].Refreshing || !status[0].Stale || len(status[0].Tools) != 0 {
		t.Fatalf("connected directory is still unready: %+v", status)
	}
	call.ID = "listing-search"
	unready, err := search.Call(ctx, call)
	if err != nil || string(unready.Content[0].JSON.Value) != "{}" || len(llm.requests) != 0 {
		t.Fatalf("listing search: %+v %v selector_calls=%d", unready, err, len(llm.requests))
	}
	releaseList()
	select {
	case <-manager.Initialized():
	case <-ctx.Done():
		t.Fatal("catalog did not become ready")
	}
	definitions := manager.Tools()
	if len(definitions) != 1 {
		t.Fatalf("ready definitions=%d", len(definitions))
	}
	status = manager.GetServerInfos("fixture")
	if len(status) != 1 || status[0].Status != "running" || status[0].Refreshing || status[0].Stale || status[0].RefreshFailed || len(status[0].Tools) != 1 {
		t.Fatalf("ready status: %+v", status)
	}
	name := definitions[0].Definition().Name
	llm.respond = func(index int, _ *model.Request) (*model.Response, error) {
		if index == 0 {
			args, _ := json.Marshal(map[string]string{"name": name})
			return &model.Response{Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "inspect-ready", Name: inspectSchemaToolName, Args: string(args)}}, ""), TurnComplete: true}, nil
		}
		names, _ := json.Marshal(map[string][]string{"tools": {name}})
		return &model.Response{Message: model.NewTextMessage(model.RoleAssistant, string(names)), TurnComplete: true}, nil
	}
	call.ID = "ready-search"
	ready, err := search.Call(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	var result tool.ToolSearchResult
	if err := json.Unmarshal(ready.Content[0].JSON.Value, &result); err != nil || len(result.Tools) != 1 || result.Tools[0].Name != name {
		t.Fatalf("fresh ready search: %+v %v", result, err)
	}
	if string(cold.Content[0].JSON.Value) != "{}" || len(llm.requests) != 2 || businessCalls.Load() != 0 {
		t.Fatalf("search changed history or invoked business tool: selector_calls=%d business_calls=%d", len(llm.requests), businessCalls.Load())
	}
	wantSchema, _ := json.Marshal(definitions[0].Definition().InputSchema)
	gotSchema, _ := json.Marshal(result.Tools[0].InputSchema)
	if string(gotSchema) != string(wantSchema) || result.Tools[0].Binding == nil {
		t.Fatalf("complete ready schema or binding missing: %+v", result.Tools[0])
	}
}
