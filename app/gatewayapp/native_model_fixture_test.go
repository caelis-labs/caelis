package gatewayapp_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
)

type nativeModelTool struct{ Name, Input string }
type nativeModelScript struct {
	mu           sync.Mutex
	calls        []nativeModelTool
	index        int
	callSequence int
	seen         [][]byte
	resourcePath string
}

func (m *nativeModelScript) set(calls ...nativeModelTool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = calls
	m.index = 0
	m.resourcePath = ""
}
func (m *nativeModelScript) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.seen = append(m.seen, append([]byte(nil), raw...))
	// The next model request includes the actual ReadResource tool result.
	// Resolve its returned path once, rather than manufacturing a Host path
	// from resource IDs or exposing the Store's internal directory.
	if m.index == 1 && len(m.calls) > 0 && m.calls[0].Name == "ReadResource" {
		match := regexp.MustCompile(`\.resources/[a-f0-9]{64}`).Find(raw)
		if len(match) == 0 {
			m.mu.Unlock()
			return nil, fmt.Errorf("ReadResource returned no model-visible relative path")
		}
		m.resourcePath = string(match)
	}
	var message map[string]any
	finish := "stop"
	if m.index < len(m.calls) {
		call := m.calls[m.index]
		m.index++
		m.callSequence++
		if strings.Contains(call.Input, "$RESOURCE_PATH") {
			if m.resourcePath == "" {
				m.mu.Unlock()
				return nil, fmt.Errorf("resource path is unavailable to the model")
			}
			call.Input = strings.ReplaceAll(call.Input, "$RESOURCE_PATH", m.resourcePath)
		}
		message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": fmt.Sprintf("native-%d", m.callSequence), "index": 0, "type": "function", "function": map[string]any{"name": call.Name, "arguments": call.Input}}}}
		finish = "tool_calls"
	} else {
		message = map[string]any{"role": "assistant", "content": "Native operation complete."}
	}
	m.mu.Unlock()
	body, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": message, "finish_reason": finish}}})
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(body) + "\n\ndata: [DONE]\n\n")), Request: req}, nil
}
