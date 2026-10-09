package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	builtintoolsearch "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	mcpProtocolVersion20260728 = "2026-07-28"
	mcpLegacyProtocolVersion   = "2025-11-25"
)

func TestProcessEnvironmentTargetOSCaseSemantics(t *testing.T) {
	base := []string{"PATH=/bin", "dtw_node_path=/package", "DTW_NODE_PATH=/stale"}
	windows := setProcessEnvironmentForOS(slices.Clone(base), "DTW_NODE_PATH", "/host", "windows")
	if !slices.Equal(windows, []string{"PATH=/bin", "DTW_NODE_PATH=/host"}) {
		t.Fatalf("Windows environment = %v", windows)
	}
	unix := setProcessEnvironmentForOS(slices.Clone(base), "DTW_NODE_PATH", "/host", "linux")
	if !slices.Equal(unix, []string{"PATH=/bin", "dtw_node_path=/package", "DTW_NODE_PATH=/host"}) {
		t.Fatalf("Unix environment = %v", unix)
	}
}

func TestMCPServerHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_MCP_HELPER") != "1" {
		return
	}
	mode := os.Getenv("CAELIS_MCP_HELPER_MODE")
	type echoArgs struct {
		Val string `json:"val"`
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
	if mode == "persistent_media" {
		var state int
		server.AddTool(&mcpsdk.Tool{Name: "desktop_exec", Description: "Execute one approved script.", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, _ *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			state++
			var imageBytes bytes.Buffer
			pixel := image.NewRGBA(image.Rect(0, 0, 1, 1))
			pixel.Set(0, 0, color.RGBA{R: 255, A: 255})
			if err := png.Encode(&imageBytes, pixel); err != nil {
				return nil, err
			}
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{
				&mcpsdk.TextContent{Text: fmt.Sprintf("execution:%d", state)},
				&mcpsdk.ImageContent{MIMEType: "image/png", Data: imageBytes.Bytes()},
			}, StructuredContent: map[string]any{"native_receipts": []any{map[string]any{"request_id": "original-1", "outcome": "succeeded"}}, "state": state}}, nil
		})
		server.AddTool(&mcpsdk.Tool{Name: "desktop_status", Description: "Inspect the persistent supervisor.", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, _ *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: fmt.Sprintf("state:%d secret:%s", state, os.Getenv("CAELIS_PRIVATE_SECRET"))}}}, nil
		})
	}
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
	if mode == "extra_tool" {
		mcpsdk.AddTool[any, any](server, &mcpsdk.Tool{
			Name:        "extra",
			Description: "Returns an extra result",
		}, func(_ context.Context, _ *mcpsdk.CallToolRequest, _ any) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{
				Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "extra"}},
			}, nil, nil
		})
	}
	if mode == "bad_schema" {
		server.AddTool(&mcpsdk.Tool{
			Name:        "bad",
			Description: "Malformed nested schema",
			InputSchema: map[string]any{"type": "object", "properties": []any{}},
		}, func(_ context.Context, _ *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			return &mcpsdk.CallToolResult{}, nil
		})
	}
	if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestMCPPersistentProcessKeepsImageStructuredReceiptAndOriginalCallID(t *testing.T) {
	t.Setenv("CAELIS_PRIVATE_SECRET", "must-not-inherit")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	dataDir := filepath.Join(t.TempDir(), "plugins", "data", "instance")
	mgr, err := newInitializedTestManager(ctx, []ServerSpec{{
		PluginID: "desktop-world", Name: "desktop-world", Command: os.Args[0],
		Args:    []string{"-test.run=^TestMCPServerHelperProcess$"},
		Env:     map[string]string{"CAELIS_MCP_HELPER": "1", "CAELIS_MCP_HELPER_MODE": "persistent_media"},
		WorkDir: dataDir, DataDir: dataDir, CleanEnvironment: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	toolsByName := map[string]tool.Tool{}
	for _, candidate := range mgr.Tools() {
		toolsByName[candidate.Definition().Name] = candidate
	}
	execTool, statusTool := toolsByName["desktop_world__desktop_exec"], toolsByName["desktop_world__desktop_status"]
	if execTool == nil || statusTool == nil {
		t.Fatalf("tools = %v", toolsByName)
	}
	for number := 1; number <= 2; number++ {
		result, err := execTool.Call(ctx, tool.Call{ID: fmt.Sprintf("call-%d", number), Name: execTool.Definition().Name, Input: []byte(`{}`)})
		if err != nil || result.IsError || result.ID != fmt.Sprintf("call-%d", number) || len(result.Content) != 3 {
			t.Fatalf("exec %d = %+v, %v", number, result, err)
		}
		if result.Content[0].Text == nil || result.Content[0].Text.Text != fmt.Sprintf("execution:%d", number) {
			t.Fatalf("text = %+v", result.Content)
		}
		if result.Content[1].Media == nil || result.Content[1].Media.MimeType != "image/png" {
			t.Fatalf("image = %+v", result.Content[1])
		}
		decoded, err := base64.StdEncoding.DecodeString(result.Content[1].Media.Source.Data)
		if err != nil || !bytes.HasPrefix(decoded, []byte("\x89PNG\r\n\x1a\n")) {
			t.Fatalf("PNG decode = %v", err)
		}
		if result.Content[2].JSON == nil || !bytes.Contains(result.Content[2].JSON.Value, []byte(`"request_id":"original-1"`)) {
			t.Fatalf("native receipt = %+v", result.Content[2])
		}
		status, err := statusTool.Call(ctx, tool.Call{ID: "status", Name: statusTool.Definition().Name, Input: []byte(`{}`)})
		if err != nil || status.IsError || status.Content[0].Text == nil || status.Content[0].Text.Text != fmt.Sprintf("state:%d secret:", number) {
			t.Fatalf("status = %+v, %v", status, err)
		}
	}
	if _, err := os.Stat(dataDir); err != nil {
		t.Fatalf("PLUGIN_DATA was not created: %v", err)
	}
}

func TestMCPManagerQuarantinesOnlyMalformedToolAndReportsWarning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mgr, err := newInitializedTestManager(ctx, []ServerSpec{{
		PluginID: "myplugin",
		Name:     "myserver",
		Command:  os.Args[0],
		Args:     []string{"-test.run=^TestMCPServerHelperProcess$"},
		Env: map[string]string{
			"CAELIS_MCP_HELPER":      "1",
			"CAELIS_MCP_HELPER_MODE": "bad_schema",
		},
		WorkDir: os.TempDir(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	tools := mgr.Tools()
	if len(tools) != 1 || tools[0].Definition().Name != "myserver__echo" {
		t.Fatalf("tools = %#v, want only valid echo", tools)
	}
	mutated := tools[0].Definition()
	mutated.InputSchema["type"] = "array"
	if got := tools[0].Definition().InputSchema["type"]; got != "object" {
		t.Fatalf("Definition schema after caller mutation = %#v, want object", got)
	}
	infos := mgr.GetServerInfos("myplugin")
	if len(infos) != 1 || !strings.Contains(infos[0].Warning, `tool "bad" quarantined`) {
		t.Fatalf("server infos = %#v, want bad-tool quarantine warning", infos)
	}
	if !slices.Equal(infos[0].Tools, []string{"echo"}) || len(infos[0].ToolDetails) != 1 || infos[0].ToolDetails[0].Name != "echo" || infos[0].ToolDetails[0].Description != tools[0].Definition().Description {
		t.Fatalf("status details include quarantined or non-callable definitions: %#v", infos[0])
	}
}

func TestMCPToolCallServerExitReturnsErrorResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	spec := ServerSpec{
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

	mgr, err := newInitializedTestManager(ctx, []ServerSpec{spec})
	if err != nil {
		t.Fatalf("failed to start MCP manager: %v", err)
	}
	defer mgr.Close()

	tools := mgr.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	res, err := tools[0].Call(ctx, tool.Call{
		ID:    "original-call",
		Name:  tools[0].Definition().Name,
		Input: []byte(`{"val":"hello"}`),
	})
	if err != nil {
		t.Fatalf("tool call returned transport error, want error result: %v", err)
	}
	if !res.IsError || res.ID != "original-call" {
		t.Fatalf("tool call IsError = false, want true")
	}
	if len(res.Content) == 0 || res.Content[0].Text == nil {
		t.Fatalf("tool call content = %#v, want text error", res.Content)
	}
	for len(mgr.Tools()) != 0 && ctx.Err() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	infos := mgr.GetServerInfos("myplugin")
	if len(mgr.Tools()) != 0 || len(infos) != 1 || infos[0].Status != "failed" || len(infos[0].Tools) != 0 {
		t.Fatalf("exited server remains callable: tools=%v infos=%+v", mgr.Tools(), infos)
	}
}

func TestMCPManagerAndTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	spec := ServerSpec{
		PluginID: "myplugin",
		Name:     "myserver",
		Command:  os.Args[0],
		Args:     []string{"-test.run=^TestMCPServerHelperProcess$"},
		Env:      map[string]string{"CAELIS_MCP_HELPER": "1"},
		WorkDir:  os.TempDir(),
	}

	mgr, err := newInitializedTestManager(ctx, []ServerSpec{spec})
	if err != nil {
		t.Fatalf("failed to start MCP manager: %v", err)
	}
	defer mgr.Close()
	requireNegotiatedProtocolVersion(t, mgr, spec.PluginID, spec.Name, mcpProtocolVersion20260728)

	tools := mgr.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	oneTool := tools[0]
	def := oneTool.Definition()
	if def.Name != "myserver__echo" {
		t.Errorf("unexpected tool name: %s", def.Name)
	}
	if !strings.HasPrefix(def.Description, tool.ExternalCapabilityDescriptionPrefix) || !strings.Contains(def.Description, "Echoes input") {
		t.Errorf("unexpected tool description: %s", def.Description)
	}
	if def.Metadata[tool.MetadataExternalCapability] != true ||
		def.Metadata[tool.MetadataDescriptionAuthority] != tool.MetadataAuthorityNonAuthorizing {
		t.Errorf("unexpected external capability metadata: %#v", def.Metadata)
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
	if len(info.ToolDetails) != 1 || info.ToolDetails[0].Name != "echo" || info.ToolDetails[0].Description != def.Description {
		t.Errorf("status details differ from ready callable definition: %#v", info.ToolDetails)
	}
	if err := oneTool.(*MCPTool).client.Close(); err != nil {
		t.Fatal(err)
	}
	disconnected := mgr.GetServerInfos("myplugin")
	if len(disconnected) != 1 || disconnected[0].Status != "failed" || len(disconnected[0].Tools) != 0 || len(disconnected[0].ToolDetails) != 0 {
		t.Fatalf("disconnected service exposed stale tool data: %#v", disconnected)
	}
}

func TestMCPManagerStreamableHTTP20260728(t *testing.T) {
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
	handler := mcpsdk.NewStreamableHTTPHandler(func(req *http.Request) *mcpsdk.Server {
		if req.Header.Get("X-Test-MCP") == "yes" {
			sawHeader.Store(true)
		}
		return server
	}, &mcpsdk.StreamableHTTPOptions{Stateless: true})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mgr, err := newMCPManagerWithHTTPHandler(ctx, handler, []ServerSpec{{
		PluginID:  "myplugin",
		Name:      "httpserver",
		Transport: TransportStreamableHTTP,
		URL:       "http://mcp.test",
		Headers:   map[string]string{"X-Test-MCP": "yes"},
	}})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Close()
	requireNegotiatedProtocolVersion(t, mgr, "myplugin", "httpserver", mcpProtocolVersion20260728)

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

func TestMCPManagerStreamableHTTPFallsBackToLegacy(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "legacy-http-test-server", Version: "1.0.0"}, nil)
	mcpsdk.AddTool[any, any](server, &mcpsdk.Tool{
		Name:        "ping",
		Description: "Pings over legacy HTTP",
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest, _ any) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "legacy-pong"}},
		}, nil, nil
	})
	// A stateful v1.7 Streamable HTTP handler cannot serve the stateless
	// 2026-07-28 protocol, so the client must fall back to legacy initialize.
	handler := mcpsdk.NewStreamableHTTPHandler(func(_ *http.Request) *mcpsdk.Server {
		return server
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mgr, err := newMCPManagerWithHTTPHandler(ctx, handler, []ServerSpec{{
		PluginID:  "myplugin",
		Name:      "legacyhttpserver",
		Transport: TransportStreamableHTTP,
		URL:       "http://mcp.test",
	}})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Close()
	requireNegotiatedProtocolVersion(t, mgr, "myplugin", "legacyhttpserver", mcpLegacyProtocolVersion)

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
	if res.IsError || len(res.Content) != 1 || res.Content[0].Text == nil || res.Content[0].Text.Text != "legacy-pong" {
		t.Fatalf("unexpected legacy HTTP MCP tool result: %+v", res)
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
	handler := mcpsdk.NewSSEHandler(func(req *http.Request) *mcpsdk.Server {
		if req.Header.Get("X-Test-MCP") == "yes" {
			sawHeader.Store(true)
		}
		return server
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mgr, err := newMCPManagerWithHTTPHandler(ctx, handler, []ServerSpec{{
		PluginID:  "myplugin",
		Name:      "sseserver",
		Transport: TransportSSE,
		URL:       "http://mcp.test",
		Headers:   map[string]string{"X-Test-MCP": "yes"},
	}})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Close()
	// HTTP+SSE has no 2026-07-28 binding; the SDK negotiates its latest
	// supported legacy version for this transport.
	requireNegotiatedProtocolVersion(t, mgr, "myplugin", "sseserver", mcpLegacyProtocolVersion)

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

func TestStandardPluginHeadersStayAtConfiguredOriginAndPreserveClientAuthorization(t *testing.T) {
	var sent []http.Header
	base := &http.Client{Transport: mcpTestRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		sent = append(sent, req.Header.Clone())
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: req}, nil
	})}
	client := httpClientWithHeaders(base, "https://mcp.example.test/api", map[string]string{"X-Package": "public", "Authorization": "package-must-not-win"})
	for _, endpoint := range []string{"https://mcp.example.test/api", "https://other.example.test/api"} {
		req, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header["authorization"] = []string{"Bearer client-owned"}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	if len(sent) != 2 || sent[0].Get("X-Package") != "public" || headerValueFold(sent[0], "Authorization") != "Bearer client-owned" || sent[1].Get("X-Package") != "" || headerValueFold(sent[1], "Authorization") != "Bearer client-owned" {
		t.Fatalf("configured headers crossed origin or replaced client auth: %+v", sent)
	}
}

func headerValueFold(headers http.Header, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

type mcpTestRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn mcpTestRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func requireNegotiatedProtocolVersion(t *testing.T, mgr *Manager, pluginID, serverName, want string) {
	t.Helper()
	var client *Client
	for _, server := range mgr.servers {
		if server.spec.PluginID == pluginID && server.spec.Name == serverName {
			client = server.client
			break
		}
	}
	if client == nil || client.session == nil {
		t.Fatalf("MCP client %s/%s has no active session", pluginID, serverName)
	}
	result := client.session.InitializeResult()
	if result == nil {
		t.Fatalf("MCP client %s/%s has no negotiation result", pluginID, serverName)
		return
	}
	if got := result.ProtocolVersion; got != want {
		t.Fatalf("MCP client %s/%s protocol version = %q, want %q", pluginID, serverName, got, want)
	}
}

func newMCPManagerWithHTTPHandler(ctx context.Context, handler http.Handler, specs []ServerSpec) (*Manager, error) {
	httpClient := &http.Client{Transport: mcpHandlerRoundTripper{handler: handler}}
	mgr, err := newManager(ctx, specs, func(ctx context.Context, spec ServerSpec) (*Client, error) {
		return startClientWithHTTPClient(ctx, spec, httpClient)
	}, nil)
	return awaitTestInitialization(ctx, mgr, err)
}

type mcpHandlerRoundTripper struct {
	handler http.Handler
}

func (rt mcpHandlerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	requestCtx, cancel := context.WithCancel(req.Context())
	request := req.Clone(requestCtx)
	reader, writer := io.Pipe()
	responseWriter := &mcpStreamingResponseWriter{
		header: make(http.Header),
		body:   writer,
		ready:  make(chan struct{}),
	}
	go func() {
		rt.handler.ServeHTTP(responseWriter, request)
		responseWriter.finish()
	}()

	select {
	case <-req.Context().Done():
		cancel()
		_ = reader.CloseWithError(req.Context().Err())
		_ = writer.CloseWithError(req.Context().Err())
		return nil, req.Context().Err()
	case <-responseWriter.ready:
		statusCode := responseWriter.statusCode
		return &http.Response{
			Status:     fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
			StatusCode: statusCode,
			Header:     responseWriter.header.Clone(),
			Body: &mcpResponseBody{
				ReadCloser: reader,
				cancel:     cancel,
			},
			Request: request,
		}, nil
	}
}

type mcpStreamingResponseWriter struct {
	header     http.Header
	body       *io.PipeWriter
	ready      chan struct{}
	readyOnce  sync.Once
	statusCode int
}

func (w *mcpStreamingResponseWriter) Header() http.Header {
	return w.header
}

func (w *mcpStreamingResponseWriter) WriteHeader(statusCode int) {
	w.readyOnce.Do(func() {
		w.statusCode = statusCode
		close(w.ready)
	})
}

func (w *mcpStreamingResponseWriter) Write(data []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.body.Write(data)
}

func (w *mcpStreamingResponseWriter) Flush() {
	w.WriteHeader(http.StatusOK)
}

func (w *mcpStreamingResponseWriter) FlushError() error {
	w.Flush()
	return nil
}

func (w *mcpStreamingResponseWriter) finish() {
	w.WriteHeader(http.StatusOK)
	_ = w.body.Close()
}

type mcpResponseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *mcpResponseBody) Close() error {
	b.cancel()
	return b.ReadCloser.Close()
}

func TestMCPToolNamesAreProviderSafeAndStable(t *testing.T) {
	if got := formatToolName("context7", "query_docs"); got != "context7__query_docs" {
		t.Fatalf("formatToolName(context7, query_docs) = %q", got)
	}
	longName := formatToolName("server/name", strings.Repeat("unsafe.tool:", 12))
	if len(longName) > 64 {
		t.Fatalf("tool name length = %d, want <= 64 (%q)", len(longName), longName)
	}
	for _, r := range longName {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		t.Fatalf("tool name contains unsafe rune %q in %q", r, longName)
	}
	if longName != formatToolName("server/name", strings.Repeat("unsafe.tool:", 12)) {
		t.Fatalf("formatToolName is not stable")
	}
}

func TestMCPManagerCompactNamesUseFirstAcceptedToolAndKeepLaterUniqueTools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	baseSpec := ServerSpec{
		Name:    "context7",
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestMCPServerHelperProcess$"},
		Env:     map[string]string{"CAELIS_MCP_HELPER": "1"},
		WorkDir: os.TempDir(),
	}
	catalogSpec := baseSpec
	catalogSpec.PluginID = "caelis.mcp.project"
	catalogSpec.ReplaySourceIDs = []string{"caelis.mcp.user", "caelis.mcp.project"}
	pluginSpec := baseSpec
	pluginSpec.PluginID = "context7-plugin"
	pluginSpec.Env = map[string]string{
		"CAELIS_MCP_HELPER":      "1",
		"CAELIS_MCP_HELPER_MODE": "extra_tool",
	}

	mgr, err := newInitializedTestManager(ctx, []ServerSpec{catalogSpec, pluginSpec})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Close()

	if len(mgr.servers) != 2 || mgr.servers[0].client == nil || mgr.servers[1].client == nil {
		t.Fatal("expected both same-named servers active")
	}
	tools := mgr.Tools()
	if len(tools) != 2 {
		t.Fatalf("tools = %d, want first echo plus later unique tool", len(tools))
	}
	definitions := map[string]tool.Definition{}
	for _, item := range tools {
		definitions[item.Definition().Name] = item.Definition()
	}
	echo := definitions["context7__echo"]
	if echo.Name == "" || echo.Metadata[tool.MetadataPluginID] != catalogSpec.PluginID {
		t.Fatalf("compact echo definition = %#v, want catalog first-win", echo)
	}
	aliases, _ := echo.Metadata[tool.MetadataReplayAliases].([]string)
	if got, want := aliases, []string{
		"mcp__caelis_mcp_project__context7__echo",
		"mcp__caelis_mcp_user__context7__echo",
		"mcp__context7_plugin__context7__echo",
	}; !slices.Equal(got, want) {
		t.Fatalf("echo replay aliases = %#v, want %#v", got, want)
	}
	extra := definitions["context7__extra"]
	if extra.Name == "" || extra.Metadata[tool.MetadataPluginID] != pluginSpec.PluginID {
		t.Fatalf("compact extra definition = %#v, want later unique plugin tool", extra)
	}

	search := builtintoolsearch.NewWithRanker(tools, builtintoolsearch.NewLexicalRanker())
	result, err := search.Call(ctx, tool.Call{Name: tool.ToolSearchToolName, Input: json.RawMessage(`{"query":"echo"}`)})
	if err != nil {
		t.Fatalf("ToolSearch.Call() error = %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].JSON == nil {
		t.Fatalf("ToolSearch result = %#v, want JSON", result)
	}
	var discovered tool.ToolSearchResult
	if err := json.Unmarshal(result.Content[0].JSON.Value, &discovered); err != nil {
		t.Fatalf("decode ToolSearch result: %v", err)
	}
	if len(discovered.Tools) != 1 || discovered.Tools[0].Name != "context7__echo" {
		t.Fatalf("ToolSearch tools = %#v, want one compact first-win echo", discovered.Tools)
	}
}

func newInitializedTestManager(ctx context.Context, specs []ServerSpec) (*Manager, error) {
	mgr, err := NewManager(ctx, specs, nil)
	return awaitTestInitialization(ctx, mgr, err)
}

func awaitTestInitialization(ctx context.Context, mgr *Manager, err error) (*Manager, error) {
	if err != nil {
		return nil, err
	}
	select {
	case <-mgr.Initialized():
		return mgr, nil
	case <-ctx.Done():
		_ = mgr.Close()
		return nil, ctx.Err()
	}
}
