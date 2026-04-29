package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/sdk/model"
)

// isOllamaProvider returns true when the provider string identifies Ollama.
func isOllamaProvider(provider string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), "ollama")
}

type ollamaLLM struct {
	name                string
	provider            string
	baseURL             string
	client              *http.Client
	requestTimeout      time.Duration
	maxOutputTok        int
	contextWindowTokens int
}

type ollamaChatRequest struct {
	Model    string               `json:"model"`
	Messages []ollamaChatMessage  `json:"messages"`
	Tools    []openAICompatTool   `json:"tools,omitempty"`
	Stream   bool                 `json:"stream"`
	Think    *bool                `json:"think,omitempty"`
	Options  *ollamaRequestOption `json:"options,omitempty"`
}

type ollamaRequestOption struct {
	NumPredict int `json:"num_predict,omitempty"`
}

type ollamaChatMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content,omitempty"`
	Thinking  string           `json:"thinking,omitempty"`
	Images    []string         `json:"images,omitempty"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	ToolName  string           `json:"tool_name,omitempty"`
}

type ollamaToolCall struct {
	Function ollamaToolCallContent `json:"function"`
}

type ollamaToolCallContent struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type ollamaChatResponse struct {
	Model           string            `json:"model"`
	Message         ollamaChatMessage `json:"message"`
	Done            bool              `json:"done"`
	PromptEvalCount int               `json:"prompt_eval_count"`
	EvalCount       int               `json:"eval_count"`
}

// newOllama returns a native Ollama /api/chat client.
func newOllama(cfg Config, _ string) model.LLM {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if strings.HasSuffix(strings.ToLower(baseURL), "/v1") {
		baseURL = baseURL[:len(baseURL)-len("/v1")]
	}
	return &ollamaLLM{
		name:                cfg.Model,
		provider:            cfg.Provider,
		baseURL:             baseURL,
		client:              coalesceHTTPClient(cfg.HTTPClient),
		requestTimeout:      cfg.Timeout,
		maxOutputTok:        cfg.MaxOutputTok,
		contextWindowTokens: cfg.ContextWindowTokens,
	}
}

func (l *ollamaLLM) Name() string {
	return l.name
}

func (l *ollamaLLM) ProviderName() string {
	return l.provider
}

func (l *ollamaLLM) ContextWindowTokens() int {
	return l.contextWindowTokens
}

func (l *ollamaLLM) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		if req == nil {
			yield(nil, fmt.Errorf("model: request is nil"))
			return
		}

		payload := ollamaChatRequest{
			Model:    l.name,
			Messages: l.fromKernelMessages(req.Instructions, req.Messages),
			Tools:    fromKernelTools(model.FunctionToolDefinitions(req.Tools)),
			Stream:   req.Stream,
		}
		if think := ollamaThinkValue(req.Reasoning); think != nil {
			payload.Think = think
		}
		if l.maxOutputTok > 0 {
			payload.Options = &ollamaRequestOption{NumPredict: l.maxOutputTok}
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			yield(nil, err)
			return
		}

		runCtx := ctx
		cancel := func() {}
		if !req.Stream && l.requestTimeout > 0 {
			runCtx, cancel = context.WithTimeout(ctx, l.requestTimeout)
		}
		defer cancel()

		httpReq, err := http.NewRequestWithContext(runCtx, http.MethodPost, l.baseURL+"/api/chat", bytes.NewReader(raw))
		if err != nil {
			yield(nil, err)
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := l.client.Do(httpReq)
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			yield(nil, statusError(resp))
			return
		}

		if !req.Stream {
			var out ollamaChatResponse
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				yield(nil, err)
				return
			}
			msg, err := ollamaToKernelMessage(out.Message)
			if err != nil {
				yield(nil, err)
				return
			}
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      msg,
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					Model:        out.Model,
					Provider:     l.provider,
					Usage:        ollamaUsage(out),
				},
			}, nil)
			return
		}

		dec := json.NewDecoder(resp.Body)
		acc := ollamaAccumulator{
			toolCalls: []model.ToolCall{},
		}
		var (
			usage   model.Usage
			modelID = l.name
		)
		for {
			var chunk ollamaChatResponse
			if err := dec.Decode(&chunk); err != nil {
				if err == io.EOF {
					break
				}
				yield(nil, err)
				return
			}
			if strings.TrimSpace(chunk.Model) != "" {
				modelID = chunk.Model
			}
			if one := ollamaUsage(chunk); one.TotalTokens > 0 || one.PromptTokens > 0 || one.CompletionTokens > 0 {
				usage = one
			}
			if text := chunk.Message.Thinking; text != "" {
				acc.reasoning.WriteString(text)
				if !yield(&model.StreamEvent{
					Type:      model.StreamEventPartDelta,
					PartDelta: &model.PartDelta{Kind: model.PartKindReasoning, TextDelta: text},
				}, nil) {
					return
				}
			}
			if text := chunk.Message.Content; text != "" {
				acc.text.WriteString(text)
				if !yield(&model.StreamEvent{
					Type:      model.StreamEventPartDelta,
					PartDelta: &model.PartDelta{Kind: model.PartKindText, TextDelta: text},
				}, nil) {
					return
				}
			}
			if len(chunk.Message.ToolCalls) > 0 {
				calls, err := ollamaToolCallsToKernel(chunk.Message.ToolCalls)
				if err != nil {
					yield(nil, err)
					return
				}
				acc.toolCalls = append(acc.toolCalls, calls...)
			}
		}

		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.MessageFromAssistantParts(acc.text.String(), acc.reasoning.String(), dedupToolCalls(acc.toolCalls)),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				Model:        modelID,
				Provider:     l.provider,
				Usage:        usage,
			},
		}, nil)
	}
}

type ollamaAccumulator struct {
	text      strings.Builder
	reasoning strings.Builder
	toolCalls []model.ToolCall
}

func (l *ollamaLLM) fromKernelMessages(instructions []model.Part, messages []model.Message) []ollamaChatMessage {
	if len(instructions) > 0 {
		messages = append([]model.Message{model.NewMessage(model.RoleSystem, instructions...)}, messages...)
	}
	out := make([]ollamaChatMessage, 0, len(messages))
	for _, msg := range messages {
		out = append(out, l.fromKernelMessage(msg))
	}
	return out
}

func (l *ollamaLLM) fromKernelMessage(msg model.Message) ollamaChatMessage {
	if resp := msg.ToolResponse(); resp != nil {
		raw, _ := json.Marshal(resp.Result)
		return ollamaChatMessage{
			Role:     string(model.RoleTool),
			Content:  string(raw),
			ToolName: resp.Name,
		}
	}

	chat := ollamaChatMessage{
		Role:     string(msg.Role),
		Content:  msg.TextContent(),
		Thinking: strings.TrimSpace(msg.ReasoningText()),
	}
	if msg.Role == model.RoleUser {
		contentParts := model.ContentPartsFromParts(msg.Parts)
		chat.Content = msg.TextContent()
		for _, part := range contentParts {
			if part.Type == model.ContentPartImage && strings.TrimSpace(part.Data) != "" {
				chat.Images = append(chat.Images, part.Data)
			}
		}
	}
	calls := msg.ToolCalls()
	if len(calls) == 0 {
		return chat
	}
	chat.ToolCalls = make([]ollamaToolCall, 0, len(calls))
	for _, call := range calls {
		args := strings.TrimSpace(call.Args)
		if args == "" {
			args = "{}"
		}
		chat.ToolCalls = append(chat.ToolCalls, ollamaToolCall{
			Function: ollamaToolCallContent{
				Name:      call.Name,
				Arguments: json.RawMessage(args),
			},
		})
	}
	return chat
}

func ollamaThinkValue(cfg model.ReasoningConfig) *bool {
	effort := strings.ToLower(strings.TrimSpace(cfg.Effort))
	if effort == "" {
		return nil
	}
	value := effort != "none"
	return &value
}

func ollamaUsage(resp ollamaChatResponse) model.Usage {
	total := resp.PromptEvalCount + resp.EvalCount
	return model.Usage{
		PromptTokens:     resp.PromptEvalCount,
		CompletionTokens: resp.EvalCount,
		TotalTokens:      total,
	}
}

func ollamaToKernelMessage(msg ollamaChatMessage) (model.Message, error) {
	if len(msg.ToolCalls) == 0 {
		return model.MessageFromAssistantParts(msg.Content, msg.Thinking, nil), nil
	}
	calls, err := ollamaToolCallsToKernel(msg.ToolCalls)
	if err != nil {
		return model.Message{}, err
	}
	return model.MessageFromAssistantParts(msg.Content, msg.Thinking, calls), nil
}

func ollamaToolCallsToKernel(calls []ollamaToolCall) ([]model.ToolCall, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	out := make([]model.ToolCall, 0, len(calls))
	for idx, call := range calls {
		name := strings.TrimSpace(call.Function.Name)
		if name == "" {
			continue
		}
		args := strings.TrimSpace(string(call.Function.Arguments))
		if args == "" {
			args = "{}"
		}
		out = append(out, model.ToolCall{
			ID:   fmt.Sprintf("ollama-call-%d", idx),
			Name: name,
			Args: args,
		})
	}
	return out, nil
}
