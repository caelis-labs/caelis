package minimax

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"strings"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
)

const (
	defaultBaseURL  = "https://api.minimaxi.com/anthropic"
	defaultModel    = "MiniMax-M2"
	replayKindToken = "thinking_signature"
)

// Config defines one MiniMax provider instance.
type Config struct {
	Model           string
	BaseURL         string
	APIKey          string
	HeaderKey       string
	Headers         map[string]string
	HTTPClient      *http.Client
	Timeout         time.Duration
	MaxTokens       int
	ReasoningEffort string
}

// Provider is the MiniMax model implementation.
type Provider struct {
	name            string
	baseURL         string
	apiKey          string
	headerKey       string
	headers         map[string]string
	client          *anthropic.Client
	timeout         time.Duration
	maxTokens       int
	reasoningEffort string
}

// New returns one MiniMax provider implementing sdk/model.LLM.
func New(cfg Config) *Provider {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	modelName := strings.TrimSpace(cfg.Model)
	if modelName == "" {
		modelName = defaultModel
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	headerKey := strings.TrimSpace(cfg.HeaderKey)
	if headerKey == "" {
		headerKey = "Authorization"
	}
	headerValue := strings.TrimSpace(cfg.APIKey)
	if strings.EqualFold(headerKey, "Authorization") && headerValue != "" && !strings.HasPrefix(strings.ToLower(headerValue), "bearer ") {
		headerValue = "Bearer " + headerValue
	}
	opts := []option.RequestOption{
		option.WithBaseURL(baseURL),
		option.WithHTTPClient(coalesceHTTPClient(cfg.HTTPClient)),
		option.WithHeader(headerKey, headerValue),
	}
	for key, value := range cfg.Headers {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		opts = append(opts, option.WithHeader(key, value))
	}
	client := anthropic.NewClient(opts...)
	return &Provider{
		name:            modelName,
		baseURL:         baseURL,
		apiKey:          strings.TrimSpace(cfg.APIKey),
		headerKey:       headerKey,
		headers:         cloneHeaders(cfg.Headers),
		client:          &client,
		timeout:         cfg.Timeout,
		maxTokens:       maxTokens,
		reasoningEffort: strings.TrimSpace(cfg.ReasoningEffort),
	}
}

func (p *Provider) Name() string {
	return p.name
}

func (p *Provider) Generate(
	ctx context.Context,
	req *sdkmodel.Request,
) iter.Seq2[*sdkmodel.StreamEvent, error] {
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		if req == nil {
			yield(nil, fmt.Errorf("sdk/model/providers/minimax: request is nil"))
			return
		}
		params, err := p.buildRequest(req)
		if err != nil {
			yield(nil, err)
			return
		}
		if req.Stream {
			p.generateStreaming(ctx, params, yield)
			return
		}
		p.generateNonStreaming(ctx, params, yield)
	}
}

func (p *Provider) generateNonStreaming(
	ctx context.Context,
	params anthropic.MessageNewParams,
	yield func(*sdkmodel.StreamEvent, error) bool,
) {
	runCtx := ctx
	cancel := func() {}
	if p.timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, p.timeout)
	}
	defer cancel()

	resp, err := p.clientOrZero().Messages.New(runCtx, params)
	if err != nil {
		yield(nil, err)
		return
	}
	message, finishReason, rawFinish, usage, err := anthropicMessageToSDK(resp)
	if err != nil {
		yield(nil, err)
		return
	}
	yield(&sdkmodel.StreamEvent{
		Type: sdkmodel.StreamEventTurnDone,
		Response: &sdkmodel.Response{
			Message:         message,
			TurnComplete:    true,
			StepComplete:    true,
			Status:          sdkmodel.ResponseStatusCompleted,
			FinishReason:    finishReason,
			RawFinishReason: rawFinish,
			Model:           resp.Model,
			Provider:        "minimax",
			Usage:           usage,
		},
	}, nil)
}

func (p *Provider) generateStreaming(
	ctx context.Context,
	params anthropic.MessageNewParams,
	yield func(*sdkmodel.StreamEvent, error) bool,
) {
	stream := p.clientOrZero().Messages.NewStreaming(ctx, params)
	if stream == nil {
		yield(nil, fmt.Errorf("sdk/model/providers/minimax: nil stream"))
		return
	}
	defer stream.Close()

	acc := anthropic.Message{}
	for stream.Next() {
		event := stream.Current()
		if err := acc.Accumulate(event); err != nil {
			yield(nil, err)
			return
		}
		switch ev := event.AsAny().(type) {
		case anthropic.ContentBlockStartEvent:
			if !emitStartBlock(ev, yield) {
				return
			}
		case anthropic.ContentBlockDeltaEvent:
			if !emitDeltaBlock(ev, yield) {
				return
			}
		}
	}
	if err := stream.Err(); err != nil {
		yield(nil, err)
		return
	}
	message, finishReason, rawFinish, usage, err := anthropicMessageToSDK(&acc)
	if err != nil {
		yield(nil, err)
		return
	}
	yield(&sdkmodel.StreamEvent{
		Type: sdkmodel.StreamEventTurnDone,
		Response: &sdkmodel.Response{
			Message:         message,
			TurnComplete:    true,
			StepComplete:    true,
			Status:          sdkmodel.ResponseStatusCompleted,
			FinishReason:    finishReason,
			RawFinishReason: rawFinish,
			Model:           acc.Model,
			Provider:        "minimax",
			Usage:           usage,
		},
	}, nil)
}

func (p *Provider) buildRequest(req *sdkmodel.Request) (anthropic.MessageNewParams, error) {
	params := anthropic.MessageNewParams{
		Model:     p.name,
		MaxTokens: int64(p.maxTokens),
		Messages:  toAnthropicMessages(req.Messages),
		System:    toAnthropicSystem(req.Instructions),
		Tools:     toAnthropicTools(sdkmodel.FunctionToolDefinitions(req.Tools)),
	}
	if thinking := p.thinkingConfig(req.Reasoning); thinking != nil {
		params.Thinking = *thinking
	}
	return params, nil
}

func (p *Provider) thinkingConfig(
	reasoning sdkmodel.ReasoningConfig,
) *anthropic.ThinkingConfigParamUnion {
	budget := reasoning.BudgetTokens
	effort := strings.ToLower(strings.TrimSpace(reasoning.Effort))
	if effort == "" {
		effort = strings.ToLower(p.reasoningEffort)
	}
	if budget <= 0 {
		switch effort {
		case "low":
			budget = 1024
		case "medium":
			budget = 4096
		case "high":
			budget = 8192
		}
	}
	if budget <= 0 {
		budget = 4096
	}
	if budget < 1024 {
		budget = 1024
	}
	cfg := anthropic.ThinkingConfigParamOfEnabled(int64(budget))
	return &cfg
}

func (p *Provider) clientOrZero() *anthropic.Client {
	if p.client != nil {
		return p.client
	}
	client := anthropic.NewClient()
	return &client
}

func emitStartBlock(
	ev anthropic.ContentBlockStartEvent,
	yield func(*sdkmodel.StreamEvent, error) bool,
) bool {
	switch block := ev.ContentBlock.AsAny().(type) {
	case anthropic.TextBlock:
		return emitTextDelta(int(ev.Index), sdkmodel.PartKindText, block.Text, nil, yield)
	case anthropic.ThinkingBlock:
		return emitTextDelta(int(ev.Index), sdkmodel.PartKindReasoning, block.Thinking, replayMeta(strings.TrimSpace(block.Signature)), yield)
	default:
		return true
	}
}

func emitDeltaBlock(
	ev anthropic.ContentBlockDeltaEvent,
	yield func(*sdkmodel.StreamEvent, error) bool,
) bool {
	switch delta := ev.Delta.AsAny().(type) {
	case anthropic.TextDelta:
		return emitTextDelta(int(ev.Index), sdkmodel.PartKindText, delta.Text, nil, yield)
	case anthropic.ThinkingDelta:
		return emitTextDelta(int(ev.Index), sdkmodel.PartKindReasoning, delta.Thinking, nil, yield)
	case anthropic.SignatureDelta:
		return yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventPartDelta,
			PartDelta: &sdkmodel.PartDelta{
				Index:  int(ev.Index),
				Kind:   sdkmodel.PartKindReasoning,
				Replay: replayMeta(strings.TrimSpace(delta.Signature)),
			},
		}, nil)
	default:
		return true
	}
}

func emitTextDelta(
	index int,
	kind sdkmodel.PartKind,
	text string,
	replay *sdkmodel.ReplayMeta,
	yield func(*sdkmodel.StreamEvent, error) bool,
) bool {
	if text == "" && replay == nil {
		return true
	}
	return yield(&sdkmodel.StreamEvent{
		Type: sdkmodel.StreamEventPartDelta,
		PartDelta: &sdkmodel.PartDelta{
			Index:     index,
			Kind:      kind,
			TextDelta: text,
			Replay:    replay,
		},
	}, nil)
}

func anthropicMessageToSDK(
	resp *anthropic.Message,
) (sdkmodel.Message, sdkmodel.FinishReason, string, sdkmodel.Usage, error) {
	if resp == nil {
		return sdkmodel.Message{}, sdkmodel.FinishReasonUnknown, "", sdkmodel.Usage{}, fmt.Errorf("sdk/model/providers/minimax: nil response")
	}
	parts := make([]sdkmodel.Part, 0, len(resp.Content))
	for _, block := range resp.Content {
		switch variant := block.AsAny().(type) {
		case anthropic.TextBlock:
			parts = append(parts, sdkmodel.NewTextPart(variant.Text))
		case anthropic.ThinkingBlock:
			part := sdkmodel.NewReasoningPart(variant.Thinking, sdkmodel.ReasoningVisibilityVisible)
			if part.Reasoning != nil && strings.TrimSpace(variant.Signature) != "" {
				part.Reasoning.Replay = replayMeta(strings.TrimSpace(variant.Signature))
			}
			parts = append(parts, part)
		case anthropic.RedactedThinkingBlock:
			part := sdkmodel.NewReasoningPart("", sdkmodel.ReasoningVisibilityRedacted)
			if part.Reasoning != nil && strings.TrimSpace(variant.Data) != "" {
				raw, _ := json.Marshal(map[string]string{"data": variant.Data})
				part.Reasoning.ProviderDetails = map[string]json.RawMessage{"anthropic": raw}
			}
			parts = append(parts, part)
		case anthropic.ToolUseBlock:
			raw := append(json.RawMessage(nil), variant.Input...)
			parts = append(parts, sdkmodel.NewToolUsePart(variant.ID, variant.Name, raw))
		}
	}
	return sdkmodel.Message{
			Role:  sdkmodel.RoleAssistant,
			Parts: parts,
			Origin: &sdkmodel.MessageOrigin{
				Provider:        "minimax",
				Model:           resp.Model,
				RawFinishReason: string(resp.StopReason),
			},
		},
		normalizeFinishReason(resp.StopReason),
		string(resp.StopReason),
		sdkmodel.Usage{
			PromptTokens:     int(resp.Usage.InputTokens),
			CompletionTokens: int(resp.Usage.OutputTokens),
			TotalTokens:      int(resp.Usage.InputTokens + resp.Usage.OutputTokens),
		},
		nil
}

func normalizeFinishReason(reason anthropic.StopReason) sdkmodel.FinishReason {
	switch reason {
	case anthropic.StopReasonEndTurn, anthropic.StopReasonStopSequence, anthropic.StopReasonPauseTurn, anthropic.StopReasonRefusal:
		return sdkmodel.FinishReasonStop
	case anthropic.StopReasonMaxTokens:
		return sdkmodel.FinishReasonLength
	case anthropic.StopReasonToolUse:
		return sdkmodel.FinishReasonToolCalls
	default:
		return sdkmodel.FinishReasonUnknown
	}
}

func replayMeta(token string) *sdkmodel.ReplayMeta {
	if token == "" {
		return nil
	}
	return &sdkmodel.ReplayMeta{
		Provider: "minimax",
		Kind:     replayKindToken,
		Token:    token,
	}
}

func toAnthropicSystem(instructions []sdkmodel.Part) []anthropic.TextBlockParam {
	if len(instructions) == 0 {
		return nil
	}
	out := make([]anthropic.TextBlockParam, 0, len(instructions))
	for _, part := range instructions {
		if part.Text == nil || part.Text.Text == "" {
			continue
		}
		out = append(out, anthropic.TextBlockParam{Text: part.Text.Text})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func toAnthropicMessages(messages []sdkmodel.Message) []anthropic.MessageParam {
	if len(messages) == 0 {
		return nil
	}
	out := make([]anthropic.MessageParam, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case sdkmodel.RoleSystem:
			continue
		case sdkmodel.RoleUser:
			blocks := toAnthropicContentBlocks(msg.Parts, true)
			if len(blocks) > 0 {
				out = append(out, anthropic.NewUserMessage(blocks...))
			}
		case sdkmodel.RoleAssistant:
			blocks := toAnthropicContentBlocks(msg.Parts, false)
			if len(blocks) > 0 {
				out = append(out, anthropic.NewAssistantMessage(blocks...))
			}
		case sdkmodel.RoleTool:
			blocks := toAnthropicToolResultBlocks(msg.Parts)
			if len(blocks) > 0 {
				out = append(out, anthropic.NewUserMessage(blocks...))
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func toAnthropicContentBlocks(
	parts []sdkmodel.Part,
	userRole bool,
) []anthropic.ContentBlockParamUnion {
	if len(parts) == 0 {
		return nil
	}
	out := make([]anthropic.ContentBlockParamUnion, 0, len(parts))
	for _, part := range parts {
		switch part.Kind {
		case sdkmodel.PartKindText:
			if part.Text != nil {
				out = append(out, anthropic.NewTextBlock(part.Text.Text))
			}
		case sdkmodel.PartKindReasoning:
			if userRole || part.Reasoning == nil {
				continue
			}
			text := ""
			if part.Reasoning.VisibleText != nil {
				text = *part.Reasoning.VisibleText
			}
			token := ""
			if part.Reasoning.Replay != nil {
				token = strings.TrimSpace(part.Reasoning.Replay.Token)
			}
			if part.Reasoning.Visibility == sdkmodel.ReasoningVisibilityRedacted {
				if raw := anthropicProviderDetail(part.Reasoning.ProviderDetails, "data"); raw != "" {
					out = append(out, anthropic.NewRedactedThinkingBlock(raw))
					continue
				}
			}
			if text != "" || token != "" {
				out = append(out, anthropic.NewThinkingBlock(token, text))
			}
		case sdkmodel.PartKindToolUse:
			if userRole || part.ToolUse == nil {
				continue
			}
			out = append(out, anthropic.NewToolUseBlock(part.ToolUse.ID, jsonRawToAny(part.ToolUse.Input), part.ToolUse.Name))
		case sdkmodel.PartKindMedia:
			if !userRole || part.Media == nil || part.Media.Modality != sdkmodel.MediaModalityImage {
				continue
			}
			if part.Media.Source.Kind == sdkmodel.MediaSourceInline && part.Media.Source.Data != "" {
				out = append(out, anthropic.NewImageBlockBase64(part.Media.MimeType, part.Media.Source.Data))
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func toAnthropicToolResultBlocks(parts []sdkmodel.Part) []anthropic.ContentBlockParamUnion {
	if len(parts) == 0 {
		return nil
	}
	out := make([]anthropic.ContentBlockParamUnion, 0, len(parts))
	for _, part := range parts {
		if part.ToolResult == nil {
			continue
		}
		block := anthropic.ToolResultBlockParam{
			ToolUseID: part.ToolResult.ToolUseID,
			IsError:   anthropic.Bool(part.ToolResult.IsError),
			Content:   toAnthropicToolResultContent(part.ToolResult.Content),
		}
		out = append(out, anthropic.ContentBlockParamUnion{OfToolResult: &block})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func toAnthropicToolResultContent(parts []sdkmodel.Part) []anthropic.ToolResultBlockParamContentUnion {
	if len(parts) == 0 {
		return []anthropic.ToolResultBlockParamContentUnion{{OfText: &anthropic.TextBlockParam{Text: "{}"}}}
	}
	out := make([]anthropic.ToolResultBlockParamContentUnion, 0, len(parts))
	for _, part := range parts {
		switch part.Kind {
		case sdkmodel.PartKindText:
			if part.Text != nil {
				out = append(out, anthropic.ToolResultBlockParamContentUnion{
					OfText: &anthropic.TextBlockParam{Text: part.Text.Text},
				})
			}
		case sdkmodel.PartKindJSON:
			if raw := part.JSONValue(); len(raw) > 0 {
				out = append(out, anthropic.ToolResultBlockParamContentUnion{
					OfText: &anthropic.TextBlockParam{Text: string(raw)},
				})
			}
		}
	}
	if len(out) == 0 {
		return []anthropic.ToolResultBlockParamContentUnion{{OfText: &anthropic.TextBlockParam{Text: "{}"}}}
	}
	return out
}

func toAnthropicTools(
	tools []sdkmodel.ToolDefinition,
) []anthropic.ToolUnionParam {
	if len(tools) == 0 {
		return nil
	}
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		schema := anthropicToolInputSchema(tool.Parameters)
		entry := anthropic.ToolUnionParamOfTool(schema, tool.Name)
		if entry.OfTool != nil {
			entry.OfTool.Description = anthropic.String(strings.TrimSpace(tool.Description))
		}
		out = append(out, entry)
	}
	return out
}

func anthropicToolInputSchema(schema map[string]any) anthropic.ToolInputSchemaParam {
	if len(schema) == 0 {
		return anthropic.ToolInputSchemaParam{Type: "object"}
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return anthropic.ToolInputSchemaParam{Type: "object", ExtraFields: map[string]any{}}
	}
	var out anthropic.ToolInputSchemaParam
	if err := json.Unmarshal(raw, &out); err != nil {
		return anthropic.ToolInputSchemaParam{Type: "object", ExtraFields: map[string]any{}}
	}
	if out.Type == "" {
		out.Type = "object"
	}
	return out
}

func anthropicProviderDetail(details map[string]json.RawMessage, key string) string {
	if len(details) == 0 || strings.TrimSpace(key) == "" {
		return ""
	}
	raw, ok := details["anthropic"]
	if !ok || len(raw) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func jsonRawToAny(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return map[string]any{}
	}
	if value == nil {
		return map[string]any{}
	}
	return value
}

func cloneHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func coalesceHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{}
}
