package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/OnslaughtSnail/caelis/sdk/model"
)

const anthropicReplayKindThinkingSignature = "thinking_signature"

type anthropicSDKLLM struct {
	name                string
	provider            string
	baseURL             string
	token               string
	headers             map[string]string
	auth                AuthConfig
	client              *anthropic.Client
	requestTimeout      time.Duration
	maxOutputTok        int
	contextWindowTokens int
}

func newAnthropic(cfg Config, token string) model.LLM {
	maxTok := cfg.MaxOutputTok
	if maxTok <= 0 {
		maxTok = 1024
	}
	opts := make([]option.RequestOption, 0, 8+len(cfg.Headers))
	opts = append(opts, option.WithBaseURL(anthropicSDKBaseURL(cfg.BaseURL)))
	opts = append(opts, option.WithHTTPClient(coalesceHTTPClient(cfg.HTTPClient)))
	opts = append(opts, anthropicAuthOptions(cfg, token)...)
	for key, value := range cfg.Headers {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		opts = append(opts, option.WithHeader(key, value))
	}
	client := anthropic.NewClient(opts...)
	return &anthropicSDKLLM{
		name:                cfg.Model,
		provider:            cfg.Provider,
		baseURL:             anthropicSDKBaseURL(cfg.BaseURL),
		token:               token,
		headers:             cloneHeaders(cfg.Headers),
		auth:                cfg.Auth,
		client:              &client,
		requestTimeout:      cfg.Timeout,
		maxOutputTok:        maxTok,
		contextWindowTokens: cfg.ContextWindowTokens,
	}
}

func (l *anthropicSDKLLM) Name() string {
	return l.name
}

func (l *anthropicSDKLLM) ProviderName() string {
	return l.provider
}

func (l *anthropicSDKLLM) ContextWindowTokens() int {
	return l.contextWindowTokens
}

func (l *anthropicSDKLLM) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		if req == nil {
			yield(nil, fmt.Errorf("model: request is nil"))
			return
		}
		params, err := l.buildRequest(req)
		if err != nil {
			yield(nil, err)
			return
		}
		if req.Stream {
			l.generateStreaming(ctx, params, yield)
			return
		}
		l.generateNonStreaming(ctx, params, yield)
	}
}

func (l *anthropicSDKLLM) generateNonStreaming(ctx context.Context, params anthropic.MessageNewParams, yield func(*model.StreamEvent, error) bool) {
	cli := l.clientOrZero()
	runCtx := ctx
	cancel := func() {}
	if l.requestTimeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, l.requestTimeout)
	}
	defer cancel()

	resp, err := cli.Messages.New(runCtx, params)
	if err != nil {
		yield(nil, err)
		return
	}
	msg, finishReason, rawFinishReason, usage, err := anthropicResponseToMessage(l.provider, resp)
	if err != nil {
		yield(nil, err)
		return
	}
	yield(&model.StreamEvent{
		Type: model.StreamEventTurnDone,
		Response: &model.Response{
			Message:         msg,
			TurnComplete:    true,
			StepComplete:    true,
			Status:          model.ResponseStatusCompleted,
			FinishReason:    finishReason,
			RawFinishReason: rawFinishReason,
			Model:           resp.Model,
			Provider:        l.provider,
			Usage:           usage,
		},
	}, nil)
}

func (l *anthropicSDKLLM) generateStreaming(ctx context.Context, params anthropic.MessageNewParams, yield func(*model.StreamEvent, error) bool) {
	cli := l.clientOrZero()
	stream := cli.Messages.NewStreaming(ctx, params)
	if stream == nil {
		yield(nil, fmt.Errorf("providers: anthropic stream is nil"))
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
			if !l.emitStreamingStartBlock(ev, yield) {
				return
			}
		case anthropic.ContentBlockDeltaEvent:
			if !l.emitStreamingDeltaBlock(ev, yield) {
				return
			}
		}
	}
	if err := stream.Err(); err != nil {
		yield(nil, err)
		return
	}
	msg, finishReason, rawFinishReason, usage, err := anthropicMessageToKernel(l.provider, &acc)
	if err != nil {
		yield(nil, err)
		return
	}
	yield(&model.StreamEvent{
		Type: model.StreamEventTurnDone,
		Response: &model.Response{
			Message:         msg,
			TurnComplete:    true,
			StepComplete:    true,
			Status:          model.ResponseStatusCompleted,
			FinishReason:    finishReason,
			RawFinishReason: rawFinishReason,
			Model:           acc.Model,
			Provider:        l.provider,
			Usage:           usage,
		},
	}, nil)
}

func (l *anthropicSDKLLM) emitStreamingStartBlock(ev anthropic.ContentBlockStartEvent, yield func(*model.StreamEvent, error) bool) bool {
	switch block := ev.ContentBlock.AsAny().(type) {
	case anthropic.TextBlock:
		return l.emitStreamingTextDelta(int(ev.Index), model.PartKindText, block.Text, nil, yield)
	case anthropic.ThinkingBlock:
		replay := anthropicReplayMeta(l.provider, strings.TrimSpace(block.Signature))
		return l.emitStreamingTextDelta(int(ev.Index), model.PartKindReasoning, block.Thinking, replay, yield)
	default:
		return true
	}
}

func (l *anthropicSDKLLM) emitStreamingDeltaBlock(ev anthropic.ContentBlockDeltaEvent, yield func(*model.StreamEvent, error) bool) bool {
	switch delta := ev.Delta.AsAny().(type) {
	case anthropic.TextDelta:
		return l.emitStreamingTextDelta(int(ev.Index), model.PartKindText, delta.Text, nil, yield)
	case anthropic.ThinkingDelta:
		return l.emitStreamingTextDelta(int(ev.Index), model.PartKindReasoning, delta.Thinking, nil, yield)
	case anthropic.SignatureDelta:
		return yield(&model.StreamEvent{
			Type: model.StreamEventPartDelta,
			PartDelta: &model.PartDelta{
				Index:  int(ev.Index),
				Kind:   model.PartKindReasoning,
				Replay: anthropicReplayMeta(l.provider, strings.TrimSpace(delta.Signature)),
			},
		}, nil)
	default:
		return true
	}
}

func (l *anthropicSDKLLM) emitStreamingTextDelta(index int, kind model.PartKind, text string, replay *model.ReplayMeta, yield func(*model.StreamEvent, error) bool) bool {
	if text == "" && replay == nil {
		return true
	}
	return yield(&model.StreamEvent{
		Type: model.StreamEventPartDelta,
		PartDelta: &model.PartDelta{
			Index:     index,
			Kind:      kind,
			TextDelta: text,
			Replay:    replay,
		},
	}, nil)
}

func anthropicReplayMeta(provider string, token string) *model.ReplayMeta {
	if token == "" {
		return nil
	}
	return &model.ReplayMeta{
		Provider: provider,
		Kind:     anthropicReplayKindThinkingSignature,
		Token:    token,
	}
}

func (l *anthropicSDKLLM) buildRequest(req *model.Request) (anthropic.MessageNewParams, error) {
	params := anthropic.MessageNewParams{
		Model:     l.name,
		MaxTokens: int64(l.maxOutputTok),
		Messages:  toAnthropicMessages(req.Messages),
		System:    toAnthropicSystem(req.Instructions),
		Tools:     toAnthropicTools(model.FunctionToolDefinitions(req.Tools)),
	}
	if thinking := anthropicThinkingConfig(l.provider, req.Reasoning); thinking != nil {
		params.Thinking = *thinking
	}
	return params, nil
}

func (l *anthropicSDKLLM) clientOrZero() anthropic.Client {
	if l.client != nil {
		return *l.client
	}
	return anthropic.NewClient()
}

func anthropicAuthOptions(cfg Config, token string) []option.RequestOption {
	auth := cfg.Auth
	if strings.TrimSpace(token) == "" {
		return nil
	}
	if key := strings.TrimSpace(auth.HeaderKey); key != "" {
		value := token
		if prefix := strings.TrimSpace(auth.Prefix); prefix != "" {
			value = prefix + " " + token
		}
		return []option.RequestOption{option.WithHeader(key, value)}
	}
	switch auth.Type {
	case AuthBearerToken, AuthOAuthToken:
		return []option.RequestOption{option.WithAuthToken(token)}
	case AuthNone:
		return nil
	default:
		return []option.RequestOption{option.WithAPIKey(token)}
	}
}

func anthropicThinkingConfig(provider string, reasoning model.ReasoningConfig) *anthropic.ThinkingConfigParamUnion {
	budget := reasoning.BudgetTokens
	effort := strings.ToLower(strings.TrimSpace(reasoning.Effort))
	if budget <= 0 {
		switch effort {
		case "low":
			budget = 1024
		case "high":
			budget = 8192
		case "medium":
			budget = 4096
		}
	}
	if strings.EqualFold(strings.TrimSpace(provider), "minimax") && budget <= 0 {
		budget = 4096
	}
	if budget <= 0 {
		return nil
	}
	if budget < 1024 {
		budget = 1024
	}
	cfg := anthropic.ThinkingConfigParamOfEnabled(int64(budget))
	return &cfg
}

func toAnthropicSystem(instructions []model.Part) []anthropic.TextBlockParam {
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

func toAnthropicMessages(messages []model.Message) []anthropic.MessageParam {
	if len(messages) == 0 {
		return nil
	}
	out := make([]anthropic.MessageParam, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case model.RoleSystem:
			continue
		case model.RoleUser:
			blocks := toAnthropicContentBlocks(msg.Parts, true)
			if len(blocks) > 0 {
				out = append(out, anthropic.NewUserMessage(blocks...))
			}
		case model.RoleAssistant:
			blocks := toAnthropicContentBlocks(msg.Parts, false)
			if len(blocks) > 0 {
				out = append(out, anthropic.NewAssistantMessage(blocks...))
			}
		case model.RoleTool:
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

func toAnthropicContentBlocks(parts []model.Part, userRole bool) []anthropic.ContentBlockParamUnion {
	if len(parts) == 0 {
		return nil
	}
	out := make([]anthropic.ContentBlockParamUnion, 0, len(parts))
	for _, part := range parts {
		switch part.Kind {
		case model.PartKindText:
			if part.Text != nil {
				out = append(out, anthropic.NewTextBlock(part.Text.Text))
			}
		case model.PartKindReasoning:
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
			switch part.Reasoning.Visibility {
			case model.ReasoningVisibilityRedacted:
				if raw := anthropicProviderDetail(part.Reasoning.ProviderDetails, "data"); raw != "" {
					out = append(out, anthropic.NewRedactedThinkingBlock(raw))
				} else if text != "" || token != "" {
					out = append(out, anthropic.NewThinkingBlock(token, text))
				}
			default:
				if text != "" || token != "" {
					out = append(out, anthropic.NewThinkingBlock(token, text))
				}
			}
		case model.PartKindToolUse:
			if userRole || part.ToolUse == nil {
				continue
			}
			out = append(out, anthropic.NewToolUseBlock(part.ToolUse.ID, jsonRawToAny(part.ToolUse.Input), part.ToolUse.Name))
		case model.PartKindMedia:
			if !userRole || part.Media == nil || part.Media.Modality != model.MediaModalityImage {
				continue
			}
			if part.Media.Source.Kind == model.MediaSourceInline && part.Media.Source.Data != "" {
				out = append(out, anthropic.NewImageBlockBase64(part.Media.MimeType, part.Media.Source.Data))
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func toAnthropicToolResultBlocks(parts []model.Part) []anthropic.ContentBlockParamUnion {
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

func toAnthropicToolResultContent(parts []model.Part) []anthropic.ToolResultBlockParamContentUnion {
	if len(parts) == 0 {
		return []anthropic.ToolResultBlockParamContentUnion{{OfText: &anthropic.TextBlockParam{Text: "{}"}}}
	}
	out := make([]anthropic.ToolResultBlockParamContentUnion, 0, len(parts))
	for _, part := range parts {
		switch part.Kind {
		case model.PartKindText:
			if part.Text != nil {
				out = append(out, anthropic.ToolResultBlockParamContentUnion{
					OfText: &anthropic.TextBlockParam{Text: part.Text.Text},
				})
			}
		case model.PartKindJSON:
			if raw := part.JSONValue(); len(raw) > 0 {
				out = append(out, anthropic.ToolResultBlockParamContentUnion{
					OfText: &anthropic.TextBlockParam{Text: string(raw)},
				})
			}
		case model.PartKindMedia:
			if part.Media == nil || part.Media.Modality != model.MediaModalityImage {
				continue
			}
			if part.Media.Source.Kind != model.MediaSourceInline || part.Media.Source.Data == "" {
				continue
			}
			out = append(out, anthropic.ToolResultBlockParamContentUnion{
				OfImage: &anthropic.ImageBlockParam{
					Source: anthropic.ImageBlockParamSourceUnion{
						OfBase64: &anthropic.Base64ImageSourceParam{
							Data:      part.Media.Source.Data,
							MediaType: anthropic.Base64ImageSourceMediaType(part.Media.MimeType),
						},
					},
				},
			})
		case model.PartKindFileRef:
			if part.FileRef != nil {
				refText := strings.TrimSpace(part.FileRef.URI)
				if refText == "" {
					refText = strings.TrimSpace(part.FileRef.LocalRef)
				}
				if refText == "" {
					refText = strings.TrimSpace(part.FileRef.FileID)
				}
				if refText != "" {
					out = append(out, anthropic.ToolResultBlockParamContentUnion{
						OfText: &anthropic.TextBlockParam{Text: refText},
					})
				}
			}
		}
	}
	if len(out) == 0 {
		return []anthropic.ToolResultBlockParamContentUnion{{OfText: &anthropic.TextBlockParam{Text: "{}"}}}
	}
	return out
}

func toAnthropicTools(tools []model.ToolDefinition) []anthropic.ToolUnionParam {
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

func anthropicResponseToMessage(provider string, resp *anthropic.Message) (model.Message, model.FinishReason, string, model.Usage, error) {
	if resp == nil {
		return model.Message{}, model.FinishReasonUnknown, "", model.Usage{}, fmt.Errorf("providers: anthropic response is nil")
	}
	msg, finishReason, rawFinishReason, usage, err := anthropicMessageToKernel(provider, resp)
	if err != nil {
		return model.Message{}, model.FinishReasonUnknown, "", model.Usage{}, err
	}
	return msg, finishReason, rawFinishReason, usage, nil
}

func anthropicMessageToKernel(provider string, resp *anthropic.Message) (model.Message, model.FinishReason, string, model.Usage, error) {
	if resp == nil {
		return model.Message{}, model.FinishReasonUnknown, "", model.Usage{}, fmt.Errorf("providers: anthropic message is nil")
	}
	parts := make([]model.Part, 0, len(resp.Content))
	for _, block := range resp.Content {
		switch variant := block.AsAny().(type) {
		case anthropic.TextBlock:
			parts = append(parts, model.NewTextPart(variant.Text))
		case anthropic.ThinkingBlock:
			part := model.NewReasoningPart(variant.Thinking, model.ReasoningVisibilityVisible)
			if part.Reasoning != nil && strings.TrimSpace(variant.Signature) != "" {
				part.Reasoning.Replay = &model.ReplayMeta{
					Provider: provider,
					Kind:     anthropicReplayKindThinkingSignature,
					Token:    variant.Signature,
				}
			}
			parts = append(parts, part)
		case anthropic.RedactedThinkingBlock:
			part := model.NewReasoningPart("", model.ReasoningVisibilityRedacted)
			if part.Reasoning != nil && strings.TrimSpace(variant.Data) != "" {
				raw, _ := json.Marshal(map[string]string{"data": variant.Data})
				part.Reasoning.ProviderDetails = map[string]json.RawMessage{"anthropic": raw}
			}
			parts = append(parts, part)
		case anthropic.ToolUseBlock:
			raw := append(json.RawMessage(nil), variant.Input...)
			part := model.NewToolUsePart(variant.ID, variant.Name, raw)
			parts = append(parts, part)
		}
	}
	out := model.Message{
		Role:  model.RoleAssistant,
		Parts: parts,
		Origin: &model.MessageOrigin{
			Provider:        provider,
			Model:           resp.Model,
			RawFinishReason: string(resp.StopReason),
		},
	}
	return out, normalizeAnthropicFinishReason(resp.StopReason), string(resp.StopReason), anthropicUsageToKernel(resp.Usage), nil
}

func anthropicUsageToKernel(usage anthropic.Usage) model.Usage {
	return model.Usage{
		PromptTokens:     int(usage.InputTokens),
		CompletionTokens: int(usage.OutputTokens),
		TotalTokens:      int(usage.InputTokens + usage.OutputTokens),
	}
}

func normalizeAnthropicFinishReason(reason anthropic.StopReason) model.FinishReason {
	switch reason {
	case anthropic.StopReasonEndTurn, anthropic.StopReasonStopSequence, anthropic.StopReasonPauseTurn, anthropic.StopReasonRefusal:
		return model.FinishReasonStop
	case anthropic.StopReasonMaxTokens:
		return model.FinishReasonLength
	case anthropic.StopReasonToolUse:
		return model.FinishReasonToolCalls
	default:
		return model.FinishReasonUnknown
	}
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
