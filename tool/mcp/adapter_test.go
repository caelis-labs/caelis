package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/tool"
	"github.com/OnslaughtSnail/caelis/tool/mcp"
)

type fakeMCPClient struct {
	tools      []mcp.RemoteTool
	callName   string
	callArgs   map[string]any
	callResult mcp.CallResult
	callErr    error
}

func (c *fakeMCPClient) ListTools(context.Context) ([]mcp.RemoteTool, error) {
	return append([]mcp.RemoteTool(nil), c.tools...), nil
}

func (c *fakeMCPClient) CallTool(_ context.Context, name string, args map[string]any) (mcp.CallResult, error) {
	c.callName = name
	c.callArgs = tool.CloneCall(tool.Call{Args: args}).Args
	if c.callErr != nil {
		return mcp.CallResult{}, c.callErr
	}
	return c.callResult, nil
}

func (c *fakeMCPClient) Close() error { return nil }

func TestToolsetListsMCPToolsAsNormalTools(t *testing.T) {
	client := &fakeMCPClient{tools: []mcp.RemoteTool{{
		Name:        "read_file",
		Description: "Read a file from the MCP server.",
		InputSchema: tool.Schema{
			Type:       "object",
			Properties: map[string]tool.Schema{"path": {Type: "string"}},
			Required:   []string{"path"},
		},
	}}}

	toolset := mcp.NewToolset(mcp.Config{
		Name:           "filesystem",
		Client:         client,
		ToolNamePrefix: "mcp.filesystem.",
	})
	tools, err := toolset.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("len(Tools) = %d, want 1", len(tools))
	}
	def := tools[0].Definition()
	if def.Name != "mcp.filesystem.read_file" {
		t.Fatalf("tool name = %q, want prefixed MCP tool", def.Name)
	}
	if def.Description != "Read a file from the MCP server." {
		t.Fatalf("description = %q", def.Description)
	}
	if def.Schema.Properties["path"].Type != "string" {
		t.Fatalf("schema = %#v, want path string", def.Schema)
	}
	if def.Metadata["mcp_server"] != "filesystem" || def.Metadata["mcp_tool"] != "read_file" {
		t.Fatalf("metadata = %#v, want mcp server/tool", def.Metadata)
	}
}

func TestMCPToolRunCallsOriginalToolNameAndMapsContent(t *testing.T) {
	client := &fakeMCPClient{
		tools: []mcp.RemoteTool{{Name: "search", InputSchema: tool.Schema{Type: "object"}}},
		callResult: mcp.CallResult{
			Content: []mcp.ContentPart{
				{Kind: mcp.ContentKindText, Text: "hello"},
				{Kind: mcp.ContentKindJSON, JSON: map[string]any{"count": float64(2)}},
				{Kind: mcp.ContentKindFileRef, URI: "file:///tmp/result.txt", MIMEType: "text/plain"},
			},
		},
	}
	tools, err := mcp.NewToolset(mcp.Config{
		Name:           "search-server",
		Client:         client,
		ToolNamePrefix: "mcp.search.",
	}).Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}

	result, err := tools[0].Run(testToolContext{}, tool.Call{
		Name: "mcp.search.search",
		Args: map[string]any{"query": "caelis"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if client.callName != "search" {
		t.Fatalf("call name = %q, want original MCP tool name", client.callName)
	}
	if client.callArgs["query"] != "caelis" {
		t.Fatalf("call args = %#v", client.callArgs)
	}
	if result.Output != "hello" {
		t.Fatalf("output = %q, want text content", result.Output)
	}
	if got, want := len(result.Parts), 3; got != want {
		t.Fatalf("parts = %d, want %d", got, want)
	}
	if result.Parts[0].Kind != "text" || result.Parts[0].Text != "hello" {
		t.Fatalf("text part = %#v", result.Parts[0])
	}
	var decoded map[string]any
	if err := json.Unmarshal(result.Parts[1].Data, &decoded); err != nil {
		t.Fatalf("json part data: %v", err)
	}
	if decoded["count"] != float64(2) {
		t.Fatalf("json part = %#v", decoded)
	}
	if result.Parts[2].Kind != "file_ref" || result.Parts[2].URI != "file:///tmp/result.txt" {
		t.Fatalf("file part = %#v", result.Parts[2])
	}
}

func TestMCPToolRunReturnsModelVisibleError(t *testing.T) {
	client := &fakeMCPClient{
		tools:   []mcp.RemoteTool{{Name: "fail", InputSchema: tool.Schema{Type: "object"}}},
		callErr: errors.New("server unavailable"),
	}
	tools, err := mcp.NewToolset(mcp.Config{Name: "srv", Client: client}).Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	result, err := tools[0].Run(testToolContext{}, tool.Call{Name: "fail"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.IsError || result.Output != "MCP tool fail error: server unavailable" {
		t.Fatalf("result = %#v, want model-visible MCP error", result)
	}
}

type testToolContext struct{}

func (testToolContext) Deadline() (deadline time.Time, ok bool) { return time.Time{}, false }
func (testToolContext) Done() <-chan struct{}                   { return nil }
func (testToolContext) Err() error                              { return nil }
func (testToolContext) Value(any) any                           { return nil }
func (testToolContext) SessionRef() string                      { return "session-1" }
func (testToolContext) InvocationID() string                    { return "inv-1" }
func (testToolContext) AgentName() string                       { return "agent-1" }
func (testToolContext) FileSystem() sandbox.FileSystem          { return nil }
