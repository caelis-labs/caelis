package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/model"
)

const defaultOpenAIBaseURL = "https://api.openai.com/v1"

// OpenAIProvider implements model.LLM using an OpenAI-compatible API.
type OpenAIProvider struct {
	name              string
	provider          string
	baseURL           string
	token             string
	model             string
	headers           map[string]string
	client            *http.Client
	firstEventTimeout time.Duration
	maxOutputTokens   int
	output            func(*openAIRequestPayload, *model.OutputSpec)
	reasoning         func(*openAIRequestPayload, model.ReasoningConfig)
	request           func(*openAIRequestPayload)
}

// OpenAIConfig holds configuration for an OpenAI-compatible provider.
type OpenAIConfig struct {
	Name                    string
	BaseURL                 string
	Token                   string
	Model                   string
	Headers                 map[string]string
	HTTPClient              *http.Client
	Timeout                 time.Duration
	StreamFirstEventTimeout time.Duration
}

// NewOpenAI creates a new OpenAI-compatible provider.
func NewOpenAI(cfg OpenAIConfig) *OpenAIProvider {
	return &OpenAIProvider{
		name:              cfg.Name,
		provider:          "openai",
		baseURL:           normalizeProviderBaseURL(cfg.BaseURL, defaultOpenAIBaseURL),
		token:             cfg.Token,
		model:             cfg.Model,
		headers:           cloneHeaders(cfg.Headers),
		client:            coalesceHTTPClient(cfg.HTTPClient),
		firstEventTimeout: normalizeStreamFirstEventTimeout(cfg.StreamFirstEventTimeout),
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
		applyConfiguredHeaders(httpReq, p.headers)

		resp, err := p.client.Do(httpReq)
		if err != nil {
			yield(model.ResponseEvent{}, fmt.Errorf("http request: %w", err))
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			yield(model.ResponseEvent{}, statusError(resp))
			return
		}

		// Read SSE stream with tool call accumulation.
		// OpenAI sends tool call arguments as partial JSON strings
		// across multiple chunks. We accumulate them until finish_reason.
		toolCalls := make(map[int]*pendingToolCall)

		if err := readSSEWithFirstEventTimeout(resp.Body, p.firstEventTimeout, func(data []byte) error {
			var chunk openaiChunk
			if err := json.Unmarshal(data, &chunk); err != nil {
				// Report parse errors instead of silently skipping.
				if !yield(model.ResponseEvent{}, fmt.Errorf("SSE parse error: %w", err)) {
					return errStopSSE
				}
				return nil
			}
			if len(chunk.Choices) == 0 {
				return nil
			}
			delta := chunk.Choices[0].Delta

			// Handle text content.
			if delta.Content != "" {
				if !yield(model.ResponseEvent{TextDelta: delta.Content}, nil) {
					return errStopSSE
				}
			}

			// Handle reasoning content (some providers use reasoning_content, OpenRouter uses reasoning).
			if reasoning := firstNonEmpty(delta.Reasoning, delta.ReasoningContent); reasoning != "" {
				if !yield(model.ResponseEvent{ReasoningDelta: reasoning}, nil) {
					return errStopSSE
				}
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
						args, err := toolArgsMap(tc.argsJSON)
						if err != nil {
							if !yield(model.ResponseEvent{}, fmt.Errorf("tool call %s arguments: %w", tc.id, err)) {
								return errStopSSE
							}
							return nil
						}
						if !yield(model.ResponseEvent{
							ToolCall: &model.ToolCallDelta{
								CallID: tc.id,
								Name:   tc.name,
								Args:   args,
							},
						}, nil) {
							return errStopSSE
						}
					}
				}
				evt := model.ResponseEvent{FinishReason: chunk.Choices[0].FinishReason}
				if chunk.Usage.hasAny() {
					usage := chunk.Usage.toModelUsage()
					evt.Usage = &usage
				}
				if !yield(evt, nil) {
					return errStopSSE
				}
				return errStopSSE
			}
			return nil
		}); err != nil {
			yield(model.ResponseEvent{}, err)
			return
		}
	}
}

// pendingToolCall accumulates streaming tool call fragments.
type pendingToolCall struct {
	id       string
	name     string
	argsJSON string
}

func (p *OpenAIProvider) buildRequest(req model.Request) map[string]any {
	payload := &openAIRequestPayload{
		Model:    p.model,
		Messages: make([]map[string]any, 0, len(req.Messages)),
		Stream:   true,
	}
	for _, m := range req.Messages {
		msg := p.buildMessage(m)
		payload.Messages = append(payload.Messages, msg)
	}
	if p.maxOutputTokens > 0 {
		payload.MaxTokens = p.maxOutputTokens
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
		payload.Tools = tools
	}

	if req.Temperature != nil {
		payload.Temperature = req.Temperature
	}
	if req.MaxTokens > 0 {
		payload.MaxTokens = req.MaxTokens
	}
	if p.output != nil {
		p.output(payload, req.Output)
	} else {
		applyOpenAIOutputSchema(payload, req.Output, openAIStructuredOutputSchema)
	}
	if p.reasoning != nil {
		p.reasoning(payload, req.Reasoning)
	}
	if p.request != nil {
		p.request(payload)
	}

	return payload.toMap()
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
		var reasoningText string
		for _, part := range m.Content {
			if part.ToolUse != nil {
				argsJSON := part.ToolUse.ArgJSON
				if argsJSON == "" {
					argsJSON = toolArgsRaw(part.ToolUse.Args)
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
			} else if part.Reasoning != nil && part.Reasoning.Text != "" && part.Reasoning.Visibility != model.ReasoningVisibilityRedacted {
				reasoningText += part.Reasoning.Text
			}
		}
		if len(textParts) > 0 {
			msg["content"] = strings.Join(textParts, "")
		}
		if reasoningText != "" {
			msg["reasoning_content"] = reasoningText
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
			if part.Reasoning != nil && part.Reasoning.Visibility != model.ReasoningVisibilityRedacted {
				text += part.Reasoning.Text
			}
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
			Reasoning        string                `json:"reasoning"`
			ReasoningContent string                `json:"reasoning_content"`
			ToolCalls        []openaiToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage openAICompatUsage `json:"usage"`
}

type openaiToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}
