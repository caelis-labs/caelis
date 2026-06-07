package mcp_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	caelisplugin "github.com/OnslaughtSnail/caelis/plugin"
	"github.com/OnslaughtSnail/caelis/tool/mcp"
)

func TestDefaultClientFactorySupportsStreamableHTTP(t *testing.T) {
	var calledTool string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		id := req["id"]
		switch req["method"] {
		case "initialize":
			writeJSONRPCResult(t, w, id, map[string]any{"protocolVersion": "2025-03-26"})
		case "tools/list":
			writeJSONRPCResult(t, w, id, map[string]any{"tools": []any{map[string]any{
				"name":        "search",
				"description": "Search notes",
				"inputSchema": map[string]any{"type": "object"},
			}}})
		case "tools/call":
			params := req["params"].(map[string]any)
			calledTool, _ = params["name"].(string)
			writeJSONRPCResult(t, w, id, map[string]any{"content": []any{map[string]any{"type": "text", "text": "found"}}})
		default:
			t.Fatalf("unexpected method: %#v", req["method"])
		}
	}))
	defer server.Close()

	client, err := (mcp.DefaultClientFactory{}).NewClient(t.Context(), caelisplugin.MCPServer{
		Name:      "notes",
		Transport: "streamable-http",
		URL:       server.URL,
	}, "")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer client.Close()

	tools, err := client.ListTools(t.Context())
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "search" {
		t.Fatalf("tools = %#v, want search", tools)
	}
	result, err := client.CallTool(t.Context(), "search", map[string]any{"query": "caelis"})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if calledTool != "search" || len(result.Content) != 1 || result.Content[0].Text != "found" {
		t.Fatalf("called=%q result=%#v, want search/found", calledTool, result)
	}
}

func TestDefaultClientFactorySupportsSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch req["method"] {
		case "initialize":
			writeSSERPCResult(t, w, req["id"], map[string]any{"protocolVersion": "2025-03-26"})
		case "tools/list":
			writeSSERPCResult(t, w, req["id"], map[string]any{"tools": []any{map[string]any{
				"name":        "echo",
				"description": "Echo text",
				"inputSchema": map[string]any{"type": "object"},
			}}})
		case "tools/call":
			writeSSERPCResult(t, w, req["id"], map[string]any{"content": []any{map[string]any{"type": "text", "text": "echo"}}})
		default:
			t.Fatalf("unexpected method: %#v", req["method"])
		}
	}))
	defer server.Close()

	client, err := (mcp.DefaultClientFactory{}).NewClient(t.Context(), caelisplugin.MCPServer{
		Name:      "events",
		Transport: "sse",
		URL:       server.URL,
	}, "")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer client.Close()

	tools, err := client.ListTools(t.Context())
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %#v, want echo", tools)
	}
}

func writeJSONRPCResult(t *testing.T, w http.ResponseWriter, id any, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func writeSSERPCResult(t *testing.T, w http.ResponseWriter, id any, result any) {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if _, err := w.Write([]byte("event: message\ndata: " + string(data) + "\n\n")); err != nil {
		t.Fatalf("write response: %v", err)
	}
}
