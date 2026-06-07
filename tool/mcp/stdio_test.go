package mcp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/tool/mcp"
)

func TestStdioClientListsAndCallsTools(t *testing.T) {
	ctx := context.Background()
	client, err := mcp.NewStdioClient(ctx, mcp.StdioConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPStdioHelperProcess", "--"},
		Env:     append(os.Environ(), "CAELIS_MCP_STDIO_HELPER=1"),
	})
	if err != nil {
		t.Fatalf("NewStdioClient() error = %v", err)
	}
	defer client.Close()

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	if tools[0].Name != "echo" || tools[0].InputSchema.Properties["text"].Type != "string" {
		t.Fatalf("tool = %#v, want echo with text schema", tools[0])
	}

	result, err := client.CallTool(ctx, "echo", map[string]any{"text": "hi"})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Kind != mcp.ContentKindText || result.Content[0].Text != "echo:hi" {
		t.Fatalf("call result = %#v, want echo text", result)
	}
}

func TestMCPStdioHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_MCP_STDIO_HELPER") != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	writer := os.Stdout
	for {
		msg, err := readTestFrame(reader)
		if err != nil {
			os.Exit(0)
		}
		var req map[string]any
		if err := json.Unmarshal(msg, &req); err != nil {
			os.Exit(2)
		}
		id := req["id"]
		method, _ := req["method"].(string)
		switch method {
		case "initialize":
			writeTestFrame(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"protocolVersion": "2025-03-26",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "fake", "version": "test"},
				},
			})
		case "tools/list":
			writeTestFrame(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"tools": []any{map[string]any{
						"name":        "echo",
						"description": "Echo text",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"text": map[string]any{"type": "string"},
							},
							"required": []any{"text"},
						},
					}},
				},
			})
		case "tools/call":
			params, _ := req["params"].(map[string]any)
			args, _ := params["arguments"].(map[string]any)
			text, _ := args["text"].(string)
			writeTestFrame(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "echo:" + text}},
				},
			})
		default:
			writeTestFrame(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"error":   map[string]any{"code": -32601, "message": "method not found"},
			})
		}
	}
}

func readTestFrame(r *bufio.Reader) ([]byte, error) {
	var length int
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, err
			}
			length = parsed
		}
	}
	if length <= 0 {
		return nil, fmt.Errorf("missing content length")
	}
	data := make([]byte, length)
	_, err := io.ReadFull(r, data)
	return data, err
}

func writeTestFrame(w io.Writer, msg any) {
	data, _ := json.Marshal(msg)
	fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(data))
	w.Write(data)
}
