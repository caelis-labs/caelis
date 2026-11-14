package runtime

import (
	"context"
	"encoding/json"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type managerAdmissionSource struct {
	*mcp.Manager
	checks      atomic.Int32
	beforeFinal func(context.Context) error
}

func (s *managerAdmissionSource) CheckToolBinding(ctx context.Context, binding tool.Binding) error {
	if s.checks.Add(1) == 2 {
		if err := s.beforeFinal(ctx); err != nil {
			return err
		}
	}
	return s.Manager.CheckToolBinding(ctx, binding)
}

func TestJSExecManagerLookupFinalAdmissionRejectsSourceChange(t *testing.T) {
	for _, change := range []string{"revoke", "schema"} {
		t.Run(change, func(t *testing.T) {
			var effects atomic.Int32
			server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "manager-admission", Version: "1"}, nil)
			publish := func(kind string) {
				server.AddTool(&mcpsdk.Tool{Name: "read", InputSchema: map[string]any{"type": "object", "required": []string{"value"}, "properties": map[string]any{"value": map[string]any{"type": kind}}}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
					effects.Add(1)
					return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "effect"}}}, nil
				})
			}
			publish("integer")
			endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
			defer endpoint.Close()
			manager, err := mcp.NewManager(t.Context(), []mcp.ServerSpec{{PluginID: "fixture", Name: "live", SourceTrusted: true, Transport: mcp.TransportStreamableHTTP, URL: endpoint.URL}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			<-manager.Initialized()
			source := &managerAdmissionSource{Manager: manager, beforeFinal: func(ctx context.Context) error {
				if change == "revoke" {
					return manager.RevokeServer("fixture", "live")
				}
				publish("string")
				return manager.RefreshTools(ctx, "fixture", "live")
			}}
			store, active, _ := codeModeStore(t)
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				switch i {
				case 0:
					return h1Script(`return await tools.search("live__read");`)
				case 1:
					return h1Script(`await tools.call("live__read",{value:1});`)
				}
				return nil
			}}
			events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{toolsearch.NewSource(source, toolsearch.NewLexicalRanker()), &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, DeferredTools: source}, nil, nil)
			if effects.Load() != 0 || source.checks.Load() != 2 {
				t.Fatalf("final boundary bypass: effects=%d checks=%d", effects.Load(), source.checks.Load())
			}
			failed := false
			for _, event := range events {
				if event.Tool != nil && event.Tool.Name == codemode.ToolName && event.Tool.Status == "failed" {
					failed = true
				}
			}
			if !failed {
				t.Fatal("final admission error hidden")
			}
			if change == "schema" {
				raw, _ := json.Marshal(driver.requests[len(driver.requests)-1].Messages)
				if !strings.Contains(string(raw), "tools.search") || !strings.Contains(string(raw), "schema changed") {
					t.Fatalf("next request lacks Code Mode stale guidance: %s", raw)
				}
			}
			if directory := os.Getenv("CAELIS_LOOKUP_EVIDENCE"); directory != "" {
				if err := os.MkdirAll(directory, 0700); err != nil {
					t.Fatal(err)
				}
				raw, err := json.MarshalIndent(map[string]any{"case": change, "actual_effects": effects.Load(), "admission_checks": source.checks.Load(), "original_session_events": events, "classification": "real JS/Runtime/local MCP Manager; deterministic model fixture"}, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, "final-claim-"+change+".json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}

		})
	}
}
