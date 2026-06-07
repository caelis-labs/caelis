package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
)

// HTTPConfig configures an MCP client over HTTP or SSE responses.
type HTTPConfig struct {
	URL string
	SSE bool
}

// HTTPClient is a minimal MCP JSON-RPC client over streamable HTTP.
type HTTPClient struct {
	endpoint string
	client   *http.Client
	sse      bool

	mu     sync.Mutex
	nextID int64
}

// NewHTTPClient creates a streamable HTTP MCP client and performs initialize.
func NewHTTPClient(ctx context.Context, cfg HTTPConfig) (*HTTPClient, error) {
	endpoint := strings.TrimSpace(cfg.URL)
	if endpoint == "" {
		return nil, fmt.Errorf("tool/mcp: http url is required")
	}
	client := &HTTPClient{
		endpoint: endpoint,
		client:   http.DefaultClient,
		sse:      cfg.SSE,
	}
	if _, err := client.request(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "caelis", "version": "layer4"},
	}); err != nil {
		return nil, err
	}
	return client, nil
}

func (c *HTTPClient) ListTools(ctx context.Context) ([]RemoteTool, error) {
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
		out = append(out, RemoteTool{
			Name:        one.Name,
			Description: one.Description,
			InputSchema: schemaFromJSON(one.InputSchema),
		})
	}
	return out, nil
}

func (c *HTTPClient) CallTool(ctx context.Context, name string, args map[string]any) (CallResult, error) {
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
			Data:     append([]byte(nil), part.Data...),
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

func (c *HTTPClient) Close() error { return nil }

func (c *HTTPClient) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := c.nextID
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		payload["params"] = params
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.sse {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("tool/mcp: http status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	raw, err := decodeHTTPRPCResponse(resp.Body, resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	var rpcResp struct {
		ID     int64           `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &rpcResp); err != nil {
		return nil, err
	}
	if rpcResp.ID != id {
		return nil, fmt.Errorf("tool/mcp: response id %d does not match request id %d", rpcResp.ID, id)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("tool/mcp: rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}

func decodeHTTPRPCResponse(r io.Reader, contentType string) ([]byte, error) {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if strings.EqualFold(mediaType, "text/event-stream") {
		return readSSEData(r)
	}
	return io.ReadAll(r)
}

func readSSEData(r io.Reader) ([]byte, error) {
	scanner := bufio.NewScanner(r)
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(data) > 0 {
				return []byte(strings.Join(data, "\n")), nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("tool/mcp: missing sse data")
	}
	return []byte(strings.Join(data, "\n")), nil
}
