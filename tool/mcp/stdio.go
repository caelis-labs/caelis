package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/OnslaughtSnail/caelis/tool"
)

// StdioConfig configures an MCP stdio client process.
type StdioConfig struct {
	Command string
	Args    []string
	Env     []string
	Dir     string
}

// StdioClient is a minimal MCP JSON-RPC client over stdio.
type StdioClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader

	mu     sync.Mutex
	nextID int64
}

// NewStdioClient starts an MCP stdio server process and performs initialize.
func NewStdioClient(ctx context.Context, cfg StdioConfig) (*StdioClient, error) {
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, fmt.Errorf("tool/mcp: stdio command is required")
	}
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	if len(cfg.Env) > 0 {
		cmd.Env = append([]string(nil), cfg.Env...)
	}
	cmd.Dir = strings.TrimSpace(cfg.Dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	client := &StdioClient{
		cmd:    cmd,
		stdin:  stdin,
		reader: bufio.NewReader(stdout),
	}
	if _, err := client.request(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "caelis", "version": "layer4"},
	}); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func (c *StdioClient) ListTools(ctx context.Context) ([]RemoteTool, error) {
	raw, err := c.request(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	out := make([]RemoteTool, 0, len(payload.Tools))
	for _, one := range payload.Tools {
		remote := RemoteTool{
			Name:        one.Name,
			Description: one.Description,
			InputSchema: schemaFromJSON(one.InputSchema),
		}
		out = append(out, remote)
	}
	return out, nil
}

func (c *StdioClient) CallTool(ctx context.Context, name string, args map[string]any) (CallResult, error) {
	raw, err := c.request(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return CallResult{}, err
	}
	var payload struct {
		Content []struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			JSON     json.RawMessage `json:"json"`
			MIMEType string          `json:"mimeType"`
			Data     []byte          `json:"data"`
			URI      string          `json:"uri"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return CallResult{}, err
	}
	result := CallResult{IsError: payload.IsError}
	for _, part := range payload.Content {
		mapped := ContentPart{
			Kind:     part.Type,
			Text:     part.Text,
			MIMEType: part.MIMEType,
			Data:     part.Data,
			URI:      part.URI,
		}
		if len(part.JSON) > 0 {
			var value any
			if err := json.Unmarshal(part.JSON, &value); err != nil {
				return CallResult{}, err
			}
			mapped.JSON = value
		}
		result.Content = append(result.Content, mapped)
	}
	return result, nil
}

func (c *StdioClient) Close() error {
	if c == nil {
		return nil
	}
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		return c.cmd.Wait()
	}
	return nil
}

func (c *StdioClient) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := c.nextID
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}
	if err := writeFrame(c.stdin, req); err != nil {
		return nil, err
	}
	type response struct {
		ID     int64           `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	data, err := readFrame(ctx, c.reader)
	if err != nil {
		return nil, err
	}
	var resp response
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	if resp.ID != id {
		return nil, fmt.Errorf("tool/mcp: response id %d does not match request id %d", resp.ID, id)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("tool/mcp: rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

func writeFrame(w io.Writer, msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(data)); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func readFrame(ctx context.Context, r *bufio.Reader) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		data, err := readFrameBlocking(r)
		ch <- result{data: data, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-ch:
		return result.data, result.err
	}
}

func readFrameBlocking(r *bufio.Reader) ([]byte, error) {
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
		return nil, fmt.Errorf("tool/mcp: missing content length")
	}
	data := make([]byte, length)
	_, err := io.ReadFull(r, data)
	return data, err
}

func schemaFromJSON(raw json.RawMessage) tool.Schema {
	if len(bytes.TrimSpace(raw)) == 0 {
		return tool.Schema{Type: "object"}
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return tool.Schema{Type: "object"}
	}
	return schemaFromAny(value)
}

func schemaFromAny(value any) tool.Schema {
	m, ok := value.(map[string]any)
	if !ok {
		return tool.Schema{}
	}
	schema := tool.Schema{}
	if text, _ := m["type"].(string); text != "" {
		schema.Type = text
	}
	if text, _ := m["description"].(string); text != "" {
		schema.Description = text
	}
	if text, _ := m["format"].(string); text != "" {
		schema.Format = text
	}
	if rawRequired, ok := m["required"].([]any); ok {
		for _, one := range rawRequired {
			if text, _ := one.(string); text != "" {
				schema.Required = append(schema.Required, text)
			}
		}
	}
	if rawEnum, ok := m["enum"].([]any); ok {
		schema.Enum = append([]any(nil), rawEnum...)
	}
	if rawProperties, ok := m["properties"].(map[string]any); ok {
		schema.Properties = make(map[string]tool.Schema, len(rawProperties))
		for key, property := range rawProperties {
			schema.Properties[key] = schemaFromAny(property)
		}
	}
	if rawItems, ok := m["items"]; ok {
		items := schemaFromAny(rawItems)
		schema.Items = &items
	}
	return schema
}
