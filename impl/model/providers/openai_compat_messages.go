package providers

import (
	"cmp"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/ports/model"
)

type openAICompatMsg struct {
	Role             string                 `json:"role"`
	Content          any                    `json:"content,omitempty"`
	ReasoningContent string                 `json:"reasoning_content,omitempty"`
	ToolCallID       string                 `json:"tool_call_id,omitempty"`
	ToolCalls        []openAICompatToolCall `json:"tool_calls,omitempty"`
}

type openAICompatReqMsg struct {
	Role             string                 `json:"role"`
	Content          any                    `json:"content,omitempty"`
	ReasoningContent *string                `json:"reasoning_content,omitempty"`
	ToolCallID       string                 `json:"tool_call_id,omitempty"`
	ToolCalls        []openAICompatToolCall `json:"tool_calls,omitempty"`
}

type openAIImageURL struct {
	URL string `json:"url"`
}

type openAIContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openAIImageURL `json:"image_url,omitempty"`
}

type openAICompatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      openAICompatMsg `json:"message"`
		FinishReason string          `json:"finish_reason"`
	} `json:"choices"`
	Usage openAICompatUsage `json:"usage"`
}

type openAICompatStreamChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta        openAICompatMsg `json:"delta"`
		FinishReason string          `json:"finish_reason"`
	} `json:"choices"`
	Usage openAICompatUsage `json:"usage"`
}

type openAIStreamAccumulator struct {
	role      model.Role
	text      strings.Builder
	reasoning strings.Builder
	toolCalls map[int]*openAICompatToolCall
}

func (a *openAIStreamAccumulator) message() (model.Message, error) {
	calls := make([]model.ToolCall, 0, len(a.toolCalls))
	keys := make([]int, 0, len(a.toolCalls))
	for idx := range a.toolCalls {
		keys = append(keys, idx)
	}
	sort.Ints(keys)
	for _, idx := range keys {
		tc := a.toolCalls[idx]
		calls = append(calls, model.ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: tc.Function.Arguments,
		})
	}
	parts := make([]model.Part, 0, len(calls)+2)
	if strings.TrimSpace(a.reasoning.String()) != "" {
		parts = append(parts, model.NewReasoningPart(a.reasoning.String(), model.ReasoningVisibilityVisible))
	}
	if strings.TrimSpace(a.text.String()) != "" {
		parts = append(parts, model.NewTextPart(a.text.String()))
	}
	for _, call := range calls {
		part := model.NewToolUsePart(call.ID, call.Name, json.RawMessage(strings.TrimSpace(call.Args)))
		if part.ToolUse != nil && strings.TrimSpace(call.ThoughtSignature) != "" {
			part.ToolUse.Replay = &model.ReplayMeta{Token: call.ThoughtSignature}
		}
		parts = append(parts, part)
	}
	return model.Message{Role: cmp.Or(a.role, model.RoleAssistant), Parts: parts}, nil
}

func (l *openAICompatLLM) fromKernelMessages(instructions []model.Part, messages []model.Message) []openAICompatReqMsg {
	if len(instructions) > 0 {
		messages = append([]model.Message{model.NewMessage(model.RoleSystem, instructions...)}, messages...)
	}
	out := make([]openAICompatReqMsg, 0, len(messages))
	seenToolCalls := map[string]struct{}{}
	for _, m := range messages {
		// OpenAI-compatible APIs reject role=tool messages that do not carry
		// a tool_call_id. Skip malformed history entries.
		if m.Role == model.RoleTool && m.ToolResponse() == nil {
			continue
		}
		for _, call := range m.ToolCalls() {
			callID := strings.TrimSpace(call.ID)
			if callID != "" {
				seenToolCalls[callID] = struct{}{}
			}
		}
		// OpenAI-compatible APIs require tool messages to carry a non-empty
		// tool_call_id that references a preceding assistant tool call.
		// Resume/legacy histories may contain incomplete tool responses; skip
		// these invalid entries to avoid hard request failures.
		if resp := m.ToolResponse(); resp != nil {
			respID := strings.TrimSpace(resp.ID)
			if respID == "" {
				continue
			}
			if _, ok := seenToolCalls[respID]; !ok {
				continue
			}
		}
		out = append(out, l.fromKernelMessage(m))
	}
	return out
}

func (l *openAICompatLLM) fromKernelMessage(m model.Message) openAICompatReqMsg {
	if resp := m.ToolResponse(); resp != nil {
		raw, _ := json.Marshal(resp.Result)
		return openAICompatReqMsg{
			Role:       string(model.RoleTool),
			ToolCallID: resp.ID,
			Content:    string(raw),
		}
	}
	if callsIn := m.ToolCalls(); len(callsIn) > 0 {
		calls := make([]openAICompatToolCall, 0, len(callsIn))
		for _, c := range callsIn {
			raw := strings.TrimSpace(c.Args)
			if raw == "" {
				raw = "{}"
			}
			calls = append(calls, openAICompatToolCall{
				ID:   c.ID,
				Type: "function",
				Function: openAICompatCallFunction{
					Name:      c.Name,
					Arguments: raw,
				},
			})
		}
		content := any(nil)
		if text := m.TextContent(); text != "" {
			content = text
		}
		return openAICompatReqMsg{
			Role:             string(m.Role),
			Content:          content,
			ReasoningContent: l.reasoningContentField(m.ReasoningText(), true, true),
			ToolCalls:        calls,
		}
	}
	if m.Role == model.RoleUser {
		contentParts := model.ContentPartsFromParts(m.Parts)
		if len(contentParts) > 0 {
			parts := make([]openAIContentPart, 0, len(contentParts))
			for _, cp := range contentParts {
				switch cp.Type {
				case model.ContentPartText:
					parts = append(parts, openAIContentPart{Type: "text", Text: cp.Text})
				case model.ContentPartImage:
					parts = append(parts, openAIContentPart{
						Type:     "image_url",
						ImageURL: &openAIImageURL{URL: fmt.Sprintf("data:%s;base64,%s", cp.MimeType, cp.Data)},
					})
				}
			}
			return openAICompatReqMsg{
				Role:    string(m.Role),
				Content: parts,
			}
		}
	}
	return openAICompatReqMsg{
		Role:             string(m.Role),
		Content:          m.TextContent(),
		ReasoningContent: l.reasoningContentField(m.ReasoningText(), false, m.Role == model.RoleAssistant),
	}
}

func (l *openAICompatLLM) reasoningContentField(reasoning string, hasToolCalls bool, assistant bool) *string {
	if l == nil || !l.options.IncludeReasoningContent {
		return nil
	}
	if strings.TrimSpace(reasoning) != "" {
		return &reasoning
	}
	if hasToolCalls && l.options.EmitEmptyReasoningForToolCall {
		empty := ""
		return &empty
	}
	if assistant && l.options.EmitEmptyReasoningForAssistant {
		empty := ""
		return &empty
	}
	return nil
}

func toKernelMessage(m openAICompatMsg) (model.Message, error) {
	role := model.Role(m.Role)
	if role == "" {
		role = model.RoleAssistant
	}
	text := ""
	if contentText, ok := m.Content.(string); ok {
		text = contentText
	}
	calls := make([]model.ToolCall, 0, len(m.ToolCalls))
	for _, c := range m.ToolCalls {
		calls = append(calls, model.ToolCall{
			ID:   c.ID,
			Name: c.Function.Name,
			Args: c.Function.Arguments,
		})
	}
	if role == model.RoleAssistant {
		return model.MessageFromAssistantParts(text, m.ReasoningContent, calls), nil
	}
	parts := make([]model.Part, 0, 1)
	if strings.TrimSpace(text) != "" {
		parts = append(parts, model.NewTextPart(text))
	}
	return model.NewMessage(role, parts...), nil
}

func contentText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		var b strings.Builder
		for _, item := range typed {
			part, _ := item.(map[string]any)
			text, _ := part["text"].(string)
			if strings.TrimSpace(text) == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(text)
		}
		return b.String()
	default:
		return ""
	}
}

func normalizeOpenAICompatFinishReason(raw string) model.FinishReason {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return model.FinishReasonUnknown
	case "stop":
		return model.FinishReasonStop
	case "length", "max_tokens":
		return model.FinishReasonLength
	case "tool_calls", "function_call":
		return model.FinishReasonToolCalls
	case "content_filter":
		return model.FinishReasonContentFilter
	default:
		return model.FinishReason(strings.ToLower(strings.TrimSpace(raw)))
	}
}
