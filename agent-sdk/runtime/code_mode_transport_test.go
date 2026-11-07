package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

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
	manager, err := mcp.NewManager(t.Context(), []mcp.ServerSpec{{PluginID: "fixture", Name: "live", Transport: mcp.TransportStreamableHTTP, URL: endpoint.URL, SourceTrusted: true}}, nil)
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
	runCodeMode(t, store, active, agent.AgentSpec{Model: codeModeModel(true), Tools: []tool.Tool{toolsearch.NewSource(manager, exactMCPRanker{}), declaration}, DeferredTools: manager}, nil, nil)
	idle := &mcpScriptModel{steps: func(int, *model.Request) []model.ToolCall { return nil }}
	runCodeMode(t, store, active, agent.AgentSpec{Model: idle, Tools: []tool.Tool{declaration}, DeferredTools: manager}, nil, nil)
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	unknown := false
	for _, event := range events {
		if event.Journal != nil && event.Journal.ToolExecution != nil {
			r := event.Journal.ToolExecution
			if r.Parent != nil && r.ToolName == "live__read" && r.Status == session.ToolExecutionUnknownOutcome && r.Binding != nil && len(r.Result) > 0 {
				unknown = true
			}
		}
	}
	if !checked.Load() || !unknown || effects.Load() != 1 {
		t.Fatalf("checked=%v unknown=%v effects=%d", checked.Load(), unknown, effects.Load())
	}
}
