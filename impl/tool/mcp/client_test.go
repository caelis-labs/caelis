package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/plugin"
	"github.com/OnslaughtSnail/caelis/ports/tool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPServerHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_MCP_HELPER") != "1" {
		return
	}
	mode := os.Getenv("CAELIS_MCP_HELPER_MODE")
	type echoArgs struct {
		Val string `json:"val"`
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
	mcpsdk.AddTool[echoArgs, any](server, &mcpsdk.Tool{
		Name:        "echo",
		Description: "Echoes input",
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest, input echoArgs) (*mcpsdk.CallToolResult, any, error) {
		if mode == "exit_on_call" {
			os.Exit(3)
		}
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{
				&mcpsdk.TextContent{Text: fmt.Sprintf("echo:%s", input.Val)},
			},
		}, nil, nil
	})
	if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestMCPToolCallServerExitReturnsErrorResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	spec := plugin.MCPServerSpec{
		PluginID: "myplugin",
		Name:     "myserver",
		Command:  os.Args[0],
		Args:     []string{"-test.run=^TestMCPServerHelperProcess$"},
		Env: map[string]string{
			"CAELIS_MCP_HELPER":      "1",
			"CAELIS_MCP_HELPER_MODE": "exit_on_call",
		},
		WorkDir: os.TempDir(),
	}

	mgr, err := NewManager(ctx, []plugin.MCPServerSpec{spec})
	if err != nil {
		t.Fatalf("failed to start MCP manager: %v", err)
	}
	defer mgr.Close()

	tools := mgr.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	res, err := tools[0].Call(ctx, tool.Call{
		Name:  tools[0].Definition().Name,
		Input: []byte(`{"val":"hello"}`),
	})
	if err != nil {
		t.Fatalf("tool call returned transport error, want error result: %v", err)
	}
	if !res.IsError {
		t.Fatalf("tool call IsError = false, want true")
	}
	if len(res.Content) == 0 || res.Content[0].Text == nil {
		t.Fatalf("tool call content = %#v, want text error", res.Content)
	}
}

func TestMCPManagerAndTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	spec := plugin.MCPServerSpec{
		PluginID: "myplugin",
		Name:     "myserver",
		Command:  os.Args[0],
		Args:     []string{"-test.run=^TestMCPServerHelperProcess$"},
		Env:      map[string]string{"CAELIS_MCP_HELPER": "1"},
		WorkDir:  os.TempDir(),
	}

	mgr, err := NewManager(ctx, []plugin.MCPServerSpec{spec})
	if err != nil {
		t.Fatalf("failed to start MCP manager: %v", err)
	}
	defer mgr.Close()

	tools := mgr.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	oneTool := tools[0]
	def := oneTool.Definition()
	if def.Name != "mcp__myplugin__myserver__echo" {
		t.Errorf("unexpected tool name: %s", def.Name)
	}
	if def.Description != "Echoes input" {
		t.Errorf("unexpected tool description: %s", def.Description)
	}

	res, err := oneTool.Call(ctx, tool.Call{
		Name:  def.Name,
		Input: []byte(`{"val":"hello"}`),
	})
	if err != nil {
		t.Fatalf("tool call failed: %v", err)
	}

	if res.IsError {
		t.Errorf("expected tool call to succeed, but got error result")
	}

	if len(res.Content) != 1 || res.Content[0].Text == nil || res.Content[0].Text.Text != "echo:hello" {
		t.Errorf("unexpected content: %+v", res.Content)
	}

	infos := mgr.GetServerInfos("myplugin")
	if len(infos) != 1 {
		t.Fatalf("expected 1 server info, got %d", len(infos))
	}
	info := infos[0]
	if info.Name != "myserver" {
		t.Errorf("expected server name 'myserver', got %s", info.Name)
	}
	if info.Status != "running" {
		t.Errorf("expected status 'running', got %s", info.Status)
	}
	if len(info.Tools) != 1 || info.Tools[0] != "echo" {
		t.Errorf("unexpected tool status in server info: %v", info.Tools)
	}
}

func TestMCPManagerStreamableHTTP(t *testing.T) {
	var sawHeader atomic.Bool
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "http-test-server", Version: "1.0.0"}, nil)
	mcpsdk.AddTool[any, any](server, &mcpsdk.Tool{
		Name:        "ping",
		Description: "Pings over HTTP",
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest, _ any) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "pong"}},
		}, nil, nil
	})
	httpServer := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(req *http.Request) *mcpsdk.Server {
		if req.Header.Get("X-Test-MCP") == "yes" {
			sawHeader.Store(true)
		}
		return server
	}, nil))
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mgr, err := NewManager(ctx, []plugin.MCPServerSpec{{
		PluginID:  "myplugin",
		Name:      "httpserver",
		Transport: plugin.MCPTransportStreamableHTTP,
		URL:       httpServer.URL,
		Headers:   map[string]string{"X-Test-MCP": "yes"},
	}})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Close()

	tools := mgr.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	res, err := tools[0].Call(ctx, tool.Call{
		Name:  tools[0].Definition().Name,
		Input: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("tool call failed: %v", err)
	}
	if res.IsError || len(res.Content) != 1 || res.Content[0].Text == nil || res.Content[0].Text.Text != "pong" {
		t.Fatalf("unexpected HTTP MCP tool result: %+v", res)
	}
	if !sawHeader.Load() {
		t.Fatal("streamable HTTP MCP server did not receive configured header")
	}
}

func TestMCPManagerSSE(t *testing.T) {
	var sawHeader atomic.Bool
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "sse-test-server", Version: "1.0.0"}, nil)
	mcpsdk.AddTool[any, any](server, &mcpsdk.Tool{
		Name:        "ping",
		Description: "Pings over SSE",
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest, _ any) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "sse-pong"}},
		}, nil, nil
	})
	httpServer := httptest.NewServer(mcpsdk.NewSSEHandler(func(req *http.Request) *mcpsdk.Server {
		if req.Header.Get("X-Test-MCP") == "yes" {
			sawHeader.Store(true)
		}
		return server
	}, nil))
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mgr, err := NewManager(ctx, []plugin.MCPServerSpec{{
		PluginID:  "myplugin",
		Name:      "sseserver",
		Transport: plugin.MCPTransportSSE,
		URL:       httpServer.URL,
		Headers:   map[string]string{"X-Test-MCP": "yes"},
	}})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Close()

	tools := mgr.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	res, err := tools[0].Call(ctx, tool.Call{
		Name:  tools[0].Definition().Name,
		Input: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("tool call failed: %v", err)
	}
	if res.IsError || len(res.Content) != 1 || res.Content[0].Text == nil || res.Content[0].Text.Text != "sse-pong" {
		t.Fatalf("unexpected SSE MCP tool result: %+v", res)
	}
	if !sawHeader.Load() {
		t.Fatal("SSE MCP server did not receive configured header")
	}
}

func TestMCPToolNamesAreProviderSafeAndStable(t *testing.T) {
	longName := formatToolName("Plugin ID", "server/name", strings.Repeat("unsafe.tool:", 12))
	if len(longName) > 64 {
		t.Fatalf("tool name length = %d, want <= 64 (%q)", len(longName), longName)
	}
	for _, r := range longName {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		t.Fatalf("tool name contains unsafe rune %q in %q", r, longName)
	}
	if longName != formatToolName("Plugin ID", "server/name", strings.Repeat("unsafe.tool:", 12)) {
		t.Fatalf("formatToolName is not stable")
	}
}
