package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"sort"
	"strings"

	"github.com/OnslaughtSnail/caelis/model"
)

// OpenAIProvider implements model.LLM using an OpenAI-compatible API.
type OpenAIProvider struct {
	name    string
	baseURL string
	token   string
	model   string
	client  *http.Client
}

// OpenAIConfig holds configuration for an OpenAI-compatible provider.
type OpenAIConfig struct {
	Name    string
	BaseURL string
	Token   string
	Model   string
}

// NewOpenAI creates a new OpenAI-compatible provider.
func NewOpenAI(cfg OpenAIConfig) *OpenAIProvider {
	return &OpenAIProvider{
		name:    cfg.Name,
		baseURL: strings.TrimSuffix(cfg.BaseURL, "/"),
		token:   cfg.Token,
		model:   cfg.Model,
		client:  &http.Client{},
	}
}

func (p *OpenAIProvider) Name() string { return p.model }

func (p *OpenAIProvider) Generate(ctx context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	return func(yield func(model.ResponseEvent, error) bool) {
		body := p.buildRequest(req)
		data, err := json.Marshal(body)
		if err != nil {
			yield(model.ResponseEvent{}, fmt.Errorf("marshal request: %w", err))
			return
		}

		httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewReader(data))
		if err != nil {
			yield(model.ResponseEvent{}, fmt.Errorf("create request: %w", err))
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+p.token)

		resp, err := p.client.Do(httpReq)
		if err != nil {
			yield(model.ResponseEvent{}, fmt.Errorf("http request: %w", err))
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			var errBody map[string]any
			json.NewDecoder(resp.Body).Decode(&errBody)
			yield(model.ResponseEvent{}, fmt.Errorf("API error %d: %v", resp.StatusCode, errBody))
			return
		}

		// Read SSE stream with tool call accumulation.
		// OpenAI sends tool call arguments as partial JSON strings
		// across multiple chunks. We accumulate them until finish_reason.
		toolCalls := make(map[int]*pendingToolCall)

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}

			var chunk openaiChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				// Report parse errors instead of silently skipping.
				yield(model.ResponseEvent{}, fmt.Errorf("SSE parse error: %w", err))
				return
			}
			if len(chunk.Choices) == 0 {
				continue
			}
			delta := chunk.Choices[0].Delta

			// Handle text content.
			if delta.Content != "" {
				yield(model.ResponseEvent{TextDelta: delta.Content}, nil)
			}

			// Handle reasoning content (some providers use this).
			if delta.ReasoningContent != "" {
				yield(model.ResponseEvent{ReasoningDelta: delta.ReasoningContent}, nil)
			}

			// Handle tool call deltas.
			for _, tc := range delta.ToolCalls {
				idx := tc.Index
				existing, ok := toolCalls[idx]
				if !ok {
					existing = &pendingToolCall{}
					toolCalls[idx] = existing
				}
				if tc.ID != "" {
					existing.id = tc.ID
				}
				if tc.Function.Name != "" {
					existing.name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					existing.argsJSON += tc.Function.Arguments
				}
			}

			// On finish_reason, emit completed tool calls.
			if chunk.Choices[0].FinishReason == "tool_calls" || chunk.Choices[0].FinishReason == "stop" {
				if chunk.Choices[0].FinishReason == "tool_calls" {
					indices := make([]int, 0, len(toolCalls))
					for idx := range toolCalls {
						indices = append(indices, idx)
					}
					sort.Ints(indices)
					for _, idx := range indices {
						tc := toolCalls[idx]
						args, err := parseArgsJSON(tc.argsJSON)
						if err != nil {
							yield(model.ResponseEvent{}, fmt.Errorf("tool call %s arguments: %w", tc.id, err))
							return
						}
						yield(model.ResponseEvent{
							ToolCall: &model.ToolCallDelta{
								CallID: tc.id,
								Name:   tc.name,
								Args:   args,
							},
						}, nil)
					}
				}
				evt := model.ResponseEvent{FinishReason: chunk.Choices[0].FinishReason}
				if chunk.Usage != nil {
					evt.Usage = &model.Usage{
						PromptTokens:     chunk.Usage.PromptTokens,
						CompletionTokens: chunk.Usage.CompletionTokens,
						TotalTokens:      chunk.Usage.TotalTokens,
					}
				}
				yield(evt, nil)
				return
			}
		}
	}
}

// pendingToolCall accumulates streaming tool call fragments.
type pendingToolCall struct {
	id       string
	name     string
	argsJSON string
}

// parseArgsJSON parses a streamed JSON argument object into a map.
func parseArgsJSON(s string) (map[string]any, error) {
	if s == "" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (p *OpenAIProvider) buildRequest(req model.Request) map[string]any {
	msgs := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		msg := p.buildMessage(m)
		msgs = append(msgs, msg)
	}

	body := map[string]any{
		"model":    p.model,
		"messages": msgs,
		"stream":   true,
	}

	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  schemaToMap(t.Schema),
				},
			})
		}
		body["tools"] = tools
	}

	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}

	return body
}

// buildMessage converts a model.Message to OpenAI wire format,
// handling tool_use and tool_result parts.
func (p *OpenAIProvider) buildMessage(m model.Message) map[string]any {
	msg := map[string]any{"role": string(m.Role)}

	switch m.Role {
	case model.RoleTool:
		// Tool results are sent as role=tool with tool_call_id.
		if len(m.Content) > 0 && m.Content[0].ToolResult != nil {
			msg["tool_call_id"] = m.Content[0].ToolResult.CallID
			msg["content"] = m.Content[0].ToolResult.Content
		}
		return msg

	case model.RoleAssistant:
		// Check for tool_use parts.
		var toolCalls []map[string]any
		var textParts []string
		for _, part := range m.Content {
			if part.ToolUse != nil {
				argsJSON := part.ToolUse.ArgJSON
				if argsJSON == "" {
					data, _ := json.Marshal(part.ToolUse.Args)
					argsJSON = string(data)
				}
				toolCalls = append(toolCalls, map[string]any{
					"id":   part.ToolUse.CallID,
					"type": "function",
					"function": map[string]any{
						"name":      part.ToolUse.Name,
						"arguments": argsJSON,
					},
				})
			} else if part.Text != "" {
				textParts = append(textParts, part.Text)
			}
		}
		if len(textParts) > 0 {
			msg["content"] = strings.Join(textParts, "")
		}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		return msg

	default:
		// User/system: join text parts.
		var text string
		for _, part := range m.Content {
			text += part.Text
		}
		if text != "" {
			msg["content"] = text
		}
		return msg
	}
}

func schemaToMap(s model.Schema) map[string]any {
	m := map[string]any{"type": s.Type}
	if len(s.Properties) > 0 {
		props := make(map[string]any, len(s.Properties))
		for k, v := range s.Properties {
			props[k] = schemaToMap(v)
		}
		m["properties"] = props
	}
	if len(s.Required) > 0 {
		m["required"] = s.Required
	}
	if s.Description != "" {
		m["description"] = s.Description
	}
	return m
}

type openaiChunk struct {
	Choices []struct {
		Delta struct {
			Content          string                `json:"content"`
			ReasoningContent string                `json:"reasoning_content"`
			ToolCalls        []openaiToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type openaiToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}
