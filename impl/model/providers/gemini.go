package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"google.golang.org/genai"
)

var errGeminiNoCandidates = errors.New("model: empty candidates")

const geminiThoughtSignaturePrefix = "b64:"

type geminiLLM struct {
	name                string
	provider            string
	token               string
	httpOptions         genai.HTTPOptions
	httpClient          *http.Client
	requestTimeout      time.Duration
	maxOutputTok        int
	contextWindowTokens int
}

func newGemini(cfg Config, token string) model.LLM {
	return &geminiLLM{
		name:                cfg.Model,
		provider:            cfg.Provider,
		token:               token,
		httpOptions:         buildGeminiHTTPOptions(cfg.BaseURL, cfg.Headers),
		httpClient:          cfg.HTTPClient,
		requestTimeout:      cfg.Timeout,
		maxOutputTok:        cfg.MaxOutputTok,
		contextWindowTokens: cfg.ContextWindowTokens,
	}
}

func (l *geminiLLM) Name() string {
	return l.name
}

func (l *geminiLLM) ProviderName() string {
	return l.provider
}

func (l *geminiLLM) ContextWindowTokens() int {
	return l.contextWindowTokens
}

func (l *geminiLLM) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		if req == nil {
			yield(nil, fmt.Errorf("model: request is nil"))
			return
		}

		system, contents, err := toGeminiContents(req.Instructions, req.Messages)
		if err != nil {
			yield(nil, err)
			return
		}

		googleSearchEnabled := geminiGoogleSearchEnabled(l.name, req.Tools)
		cfg := &genai.GenerateContentConfig{
			Tools: toGeminiTools(req.Tools, googleSearchEnabled),
		}
		if googleSearchEnabled {
			includeServerSideTools := true
			cfg.ToolConfig = &genai.ToolConfig{
				IncludeServerSideToolInvocations: &includeServerSideTools,
			}
		}
		if strings.TrimSpace(system) != "" {
			cfg.SystemInstruction = &genai.Content{
				Parts: []*genai.Part{genai.NewPartFromText(system)},
			}
		}
		if l.maxOutputTok > 0 {
			cfg.MaxOutputTokens = int32(l.maxOutputTok)
		}
		applyGeminiOutput(cfg, req.Output)
		if thinkingCfg := toGeminiThinkingConfig(l.name, req.Reasoning); thinkingCfg != nil {
			cfg.ThinkingConfig = thinkingCfg
		}

		runCtx := ctx
		cancel := func() {}
		// Keep stream requests bounded by caller context only.
		if !req.Stream && l.requestTimeout > 0 {
			runCtx, cancel = context.WithTimeout(ctx, l.requestTimeout)
		}
		defer cancel()

		client, err := l.newClient(runCtx)
		if err != nil {
			yield(nil, err)
			return
		}

		if !req.Stream {
			out, err := client.Models.GenerateContent(runCtx, l.name, contents, cfg)
			if err != nil {
				yield(nil, err)
				return
			}
			msg, usage, err := geminiResponseToMessage(out)
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
					Model:        l.name,
					Provider:     l.provider,
					Usage:        usage,
				},
			}, nil)
			return
		}

		acc := geminiAccumulator{
			role: model.RoleAssistant,
		}
		var usage model.Usage
		for out, err := range client.Models.GenerateContentStream(runCtx, l.name, contents, cfg) {
			if err != nil {
				yield(nil, err)
				return
			}
			if out == nil {
				continue
			}
			usage = mergeGeminiUsage(usage, geminiUsageFromResponse(out))

			msg, _, convErr := geminiResponseToMessage(out)
			if convErr != nil {
				if errors.Is(convErr, errGeminiNoCandidates) {
					continue
				}
				yield(nil, convErr)
				return
			}

			if msg.Role != "" {
				acc.role = msg.Role
			}
			for _, part := range msg.Parts {
				if delta := geminiStreamPartDelta(part); delta != nil {
					if !yield(&model.StreamEvent{
						Type:      model.StreamEventPartDelta,
						PartDelta: delta,
					}, nil) {
						return
					}
				}
				acc.addPart(part)
			}
		}

		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      acc.message(),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				Model:        l.name,
				Provider:     l.provider,
				Usage:        usage,
			},
		}, nil)
	}
}

func applyGeminiOutput(cfg *genai.GenerateContentConfig, output *model.OutputSpec) {
	if cfg == nil || output == nil {
		return
	}
	if output.MaxOutputTokens > 0 {
		cfg.MaxOutputTokens = int32(output.MaxOutputTokens)
	}
	switch output.Mode {
	case model.OutputModeJSON:
		cfg.ResponseMIMEType = "application/json"
	case model.OutputModeSchema:
		cfg.ResponseMIMEType = "application/json"
		if schema := geminiSchemaFromMap(output.JSONSchema); schema != nil {
			cfg.ResponseSchema = schema
		}
	}
}

func geminiSchemaFromMap(in map[string]any) *genai.Schema {
	if len(in) == 0 {
		return nil
	}
	out := &genai.Schema{}
	if typ, _ := in["type"].(string); typ != "" {
		out.Type = geminiSchemaType(typ)
	}
	if description, _ := in["description"].(string); description != "" {
		out.Description = description
	}
	if enum := stringSliceFromAny(in["enum"]); len(enum) > 0 {
		out.Enum = enum
	}
	if required := stringSliceFromAny(in["required"]); len(required) > 0 {
		out.Required = required
	}
	if properties, _ := in["properties"].(map[string]any); len(properties) > 0 {
		out.Properties = map[string]*genai.Schema{}
		for key, value := range properties {
			nested, _ := value.(map[string]any)
			if schema := geminiSchemaFromMap(nested); schema != nil {
				out.Properties[key] = schema
			}
		}
	}
	if item, _ := in["items"].(map[string]any); len(item) > 0 {
		out.Items = geminiSchemaFromMap(item)
	}
	return out
}

func geminiSchemaType(value string) genai.Type {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "object":
		return genai.TypeObject
	case "array":
		return genai.TypeArray
	case "string":
		return genai.TypeString
	case "number":
		return genai.TypeNumber
	case "integer":
		return genai.TypeInteger
	case "boolean":
		return genai.TypeBoolean
	case "null":
		return genai.TypeNULL
	default:
		return genai.TypeUnspecified
	}
}

func stringSliceFromAny(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, _ := item.(string)
			text = strings.TrimSpace(text)
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func (l *geminiLLM) newClient(ctx context.Context) (*genai.Client, error) {
	return genai.NewClient(ctx, &genai.ClientConfig{
		Backend:     genai.BackendGeminiAPI,
		APIKey:      l.token,
		HTTPOptions: l.httpOptions,
		HTTPClient:  l.httpClient,
	})
}

type geminiAccumulator struct {
	role  model.Role
	parts []model.Part
}

func (a *geminiAccumulator) addPart(part model.Part) {
	switch part.Kind {
	case model.PartKindText:
		a.addTextPart(part)
	case model.PartKindReasoning:
		a.addReasoningPart(part)
	case model.PartKindToolUse:
		if !a.mergeToolUsePart(part) {
			a.parts = append(a.parts, part)
		}
	default:
		a.parts = append(a.parts, part)
	}
}

func (a *geminiAccumulator) addTextPart(part model.Part) {
	if part.Text == nil || part.Text.Text == "" {
		return
	}
	if len(a.parts) > 0 {
		last := &a.parts[len(a.parts)-1]
		if last.Kind == model.PartKindText && last.Text != nil {
			last.Text.Text += part.Text.Text
			return
		}
	}
	a.parts = append(a.parts, part)
}

func (a *geminiAccumulator) addReasoningPart(part model.Part) {
	if part.Reasoning == nil {
		return
	}
	if !geminiIsPlainVisibleReasoningPart(part) {
		a.parts = append(a.parts, part)
		return
	}
	if len(a.parts) > 0 {
		last := &a.parts[len(a.parts)-1]
		if geminiIsPlainVisibleReasoningPart(*last) {
			*last.Reasoning.VisibleText += *part.Reasoning.VisibleText
			return
		}
	}
	a.parts = append(a.parts, part)
}

func (a *geminiAccumulator) mergeToolUsePart(part model.Part) bool {
	incoming, ok := geminiToolCallFromPart(part)
	if !ok {
		return false
	}
	key := callKey(incoming)
	for i, existingPart := range a.parts {
		existing, ok := geminiToolCallFromPart(existingPart)
		if !ok || callKey(existing) != key {
			continue
		}
		a.parts[i] = geminiToolUsePartFromCall(mergeToolCall(existing, incoming))
		return true
	}
	return false
}

func (a *geminiAccumulator) message() model.Message {
	return model.NewMessage(a.role, a.parts...)
}

func geminiStreamPartDelta(part model.Part) *model.PartDelta {
	switch part.Kind {
	case model.PartKindText:
		if part.Text == nil || part.Text.Text == "" {
			return nil
		}
		return &model.PartDelta{Kind: model.PartKindText, TextDelta: part.Text.Text}
	case model.PartKindReasoning:
		text := geminiVisibleReasoningText(part)
		if text == "" {
			return nil
		}
		return &model.PartDelta{Kind: model.PartKindReasoning, TextDelta: text}
	default:
		return nil
	}
}

func geminiVisibleReasoningText(part model.Part) string {
	if part.Reasoning == nil || part.Reasoning.VisibleText == nil {
		return ""
	}
	return *part.Reasoning.VisibleText
}

func geminiIsPlainVisibleReasoningPart(part model.Part) bool {
	if part.Kind != model.PartKindReasoning || part.Reasoning == nil || part.Reasoning.VisibleText == nil {
		return false
	}
	return part.Reasoning.Replay == nil && len(part.Reasoning.ProviderDetails) == 0
}

func geminiToolCallFromPart(part model.Part) (model.ToolCall, bool) {
	if part.Kind != model.PartKindToolUse || part.ToolUse == nil {
		return model.ToolCall{}, false
	}
	call := model.ToolCall{
		ID:               part.ToolUse.ID,
		Name:             part.ToolUse.Name,
		Args:             toolArgsRawFromMessagePart(part),
		ThoughtSignature: geminiReplayToken(part.ToolUse.Replay),
	}
	if strings.TrimSpace(call.ID) == "" && strings.TrimSpace(call.Name) == "" {
		return model.ToolCall{}, false
	}
	return call, true
}

func geminiToolUsePartFromCall(call model.ToolCall) model.Part {
	part := model.NewToolUsePart(call.ID, call.Name, json.RawMessage(strings.TrimSpace(call.Args)))
	if part.ToolUse != nil && strings.TrimSpace(call.ThoughtSignature) != "" {
		part.ToolUse.Replay = &model.ReplayMeta{
			Provider: geminiReplayProvider,
			Token:    call.ThoughtSignature,
		}
	}
	return part
}

func buildGeminiHTTPOptions(baseURL string, headers map[string]string) genai.HTTPOptions {
	root, version := splitGeminiBaseURL(baseURL)
	opts := genai.HTTPOptions{}
	if root != "" {
		opts.BaseURL = root
	}
	if version != "" {
		opts.APIVersion = version
	}
	if len(headers) > 0 {
		hdr := http.Header{}
		for k, v := range headers {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			hdr.Set(k, v)
		}
		if len(hdr) > 0 {
			opts.Headers = hdr
		}
	}
	return opts
}

func splitGeminiBaseURL(baseURL string) (root string, apiVersion string) {
	trimmed := strings.TrimSpace(baseURL)
	trimmed = strings.TrimRight(trimmed, "/")
	if trimmed == "" {
		return "", ""
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return trimmed, ""
	}
	path := strings.Trim(u.Path, "/")
	if path == "" {
		return trimmed, ""
	}
	segments := strings.Split(path, "/")
	last := segments[len(segments)-1]
	if !looksLikeAPIVersion(last) {
		return trimmed, ""
	}
	apiVersion = last
	segments = segments[:len(segments)-1]
	u.Path = strings.Join(segments, "/")
	root = strings.TrimRight(u.String(), "/")
	if root == "" {
		root = trimmed
	}
	return root, apiVersion
}

func looksLikeAPIVersion(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	if len(v) < 2 || v[0] != 'v' {
		return false
	}
	i := 1
	for i < len(v) && v[i] >= '0' && v[i] <= '9' {
		i++
	}
	if i == 1 {
		return false
	}
	if i == len(v) {
		return true
	}
	rest := v[i:]
	if rest == "alpha" || rest == "beta" {
		return true
	}
	if strings.HasPrefix(rest, "alpha") || strings.HasPrefix(rest, "beta") {
		num := strings.TrimPrefix(strings.TrimPrefix(rest, "alpha"), "beta")
		if num == "" {
			return true
		}
		_, err := strconv.Atoi(num)
		return err == nil
	}
	return false
}

func toGeminiThinkingConfig(modelName string, reasoning model.ReasoningConfig) *genai.ThinkingConfig {
	effort := strings.ToLower(strings.TrimSpace(reasoning.Effort))
	disabled := effort == "none"
	explicit := effort != "" || reasoning.BudgetTokens > 0
	if !explicit {
		return nil
	}

	// Gemini 2.x and earlier use token budget; Gemini 3+ uses thinking level.
	if geminiUsesThinkingBudget(modelName) {
		budget := geminiThinkingBudgetForReasoning(effort, reasoning.BudgetTokens)
		if disabled {
			budget = 0
		}
		value := int32(budget)
		return &genai.ThinkingConfig{
			IncludeThoughts: !disabled,
			ThinkingBudget:  &value,
		}
	}

	level := resolveGeminiThinkingLevel(reasoning)
	if level == genai.ThinkingLevelUnspecified {
		return nil
	}
	return &genai.ThinkingConfig{
		IncludeThoughts: !disabled,
		ThinkingLevel:   level,
	}
}

func geminiUsesThinkingBudget(modelName string) bool {
	major, ok := geminiMajorVersion(modelName)
	return ok && major < 3
}

func geminiMajorVersion(modelName string) (int, bool) {
	name := strings.ToLower(strings.TrimSpace(modelName))
	if name == "" {
		return 0, false
	}
	if slash := strings.LastIndex(name, "/"); slash >= 0 {
		name = name[slash+1:]
	}
	_, tail, found := strings.Cut(name, "gemini-")
	if !found {
		return 0, false
	}
	end := 0
	for end < len(tail) && tail[end] >= '0' && tail[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	major, err := strconv.Atoi(tail[:end])
	if err != nil {
		return 0, false
	}
	return major, true
}

func geminiThinkingBudgetForReasoning(level string, explicitBudget int) int {
	switch level {
	case "minimal":
		return 512
	case "low":
		return 1024
	case "medium":
		return 4096
	case "high", "xhigh":
		return 8192
	}
	if explicitBudget > 0 {
		return explicitBudget
	}
	return 4096
}

func resolveGeminiThinkingLevel(reasoning model.ReasoningConfig) genai.ThinkingLevel {
	switch strings.ToLower(strings.TrimSpace(reasoning.Effort)) {
	case "none", "minimal":
		return genai.ThinkingLevelMinimal
	case "low":
		return genai.ThinkingLevelLow
	case "medium":
		return genai.ThinkingLevelMedium
	case "high", "xhigh":
		return genai.ThinkingLevelHigh
	}
	return genai.ThinkingLevelUnspecified
}

func geminiUsageFromResponse(out *genai.GenerateContentResponse) model.Usage {
	if out == nil || out.UsageMetadata == nil {
		return model.Usage{}
	}
	return model.Usage{
		PromptTokens:      int(out.UsageMetadata.PromptTokenCount),
		CachedInputTokens: int(out.UsageMetadata.CachedContentTokenCount),
		CompletionTokens:  int(out.UsageMetadata.CandidatesTokenCount),
		ReasoningTokens:   int(out.UsageMetadata.ThoughtsTokenCount),
		TotalTokens:       int(out.UsageMetadata.TotalTokenCount),
	}
}

func mergeGeminiUsage(existing, next model.Usage) model.Usage {
	if next.PromptTokens != 0 {
		existing.PromptTokens = next.PromptTokens
	}
	if next.CachedInputTokens != 0 {
		existing.CachedInputTokens = next.CachedInputTokens
	}
	if next.CompletionTokens != 0 {
		existing.CompletionTokens = next.CompletionTokens
	}
	if next.ReasoningTokens != 0 {
		existing.ReasoningTokens = next.ReasoningTokens
	}
	if next.TotalTokens != 0 {
		existing.TotalTokens = next.TotalTokens
	}
	return existing
}

func geminiResponseToMessage(out *genai.GenerateContentResponse) (model.Message, model.Usage, error) {
	usage := geminiUsageFromResponse(out)
	if out == nil || len(out.Candidates) == 0 || out.Candidates[0] == nil || out.Candidates[0].Content == nil {
		return model.Message{}, usage, errGeminiNoCandidates
	}

	parts := make([]model.Part, 0, len(out.Candidates[0].Content.Parts))
	for _, part := range out.Candidates[0].Content.Parts {
		if part == nil {
			continue
		}
		if part.FunctionCall != nil {
			callID := strings.TrimSpace(part.FunctionCall.ID)
			if callID == "" {
				callID = part.FunctionCall.Name
			}
			toolPart := model.NewToolUsePart(callID, part.FunctionCall.Name, json.RawMessage(toolArgsRaw(part.FunctionCall.Args)))
			if token := encodeGeminiThoughtSignature(part.ThoughtSignature); token != "" && toolPart.ToolUse != nil {
				toolPart.ToolUse.Replay = &model.ReplayMeta{
					Provider: geminiReplayProvider,
					Token:    token,
				}
			}
			parts = append(parts, toolPart)
			continue
		}
		if part.ToolCall != nil {
			if replayPart, ok := geminiServerToolReplayPart(geminiReplayKindServerToolCall, part.ToolCall); ok {
				parts = append(parts, replayPart)
			}
			continue
		}
		if part.ToolResponse != nil {
			if replayPart, ok := geminiServerToolReplayPart(geminiReplayKindServerToolResponse, part.ToolResponse); ok {
				parts = append(parts, replayPart)
			}
			continue
		}
		if strings.TrimSpace(part.Text) != "" {
			if part.Thought {
				parts = append(parts, model.NewReasoningPart(part.Text, model.ReasoningVisibilityVisible))
			} else {
				parts = append(parts, model.NewTextPart(part.Text))
			}
		}
	}
	return model.NewMessage(model.RoleAssistant, parts...), usage, nil
}

func toGeminiContents(instructions []model.Part, messages []model.Message) (string, []*genai.Content, error) {
	systemLines := make([]string, 0, 2)
	if system := strings.TrimSpace(model.NewMessage(model.RoleSystem, instructions...).TextContent()); system != "" {
		systemLines = append(systemLines, system)
	}
	out := make([]*genai.Content, 0, len(messages))

	for _, m := range messages {
		switch m.Role {
		case model.RoleSystem:
			if text := strings.TrimSpace(m.TextContent()); text != "" {
				systemLines = append(systemLines, text)
			}
		case model.RoleUser:
			contentParts := model.ContentPartsFromParts(m.Parts)
			parts := make([]*genai.Part, 0, max(1, len(contentParts)))
			if len(contentParts) > 0 {
				for _, cp := range contentParts {
					switch cp.Type {
					case model.ContentPartText:
						parts = append(parts, genai.NewPartFromText(cp.Text))
					case model.ContentPartImage:
						data, err := decodeBase64Image(cp.Data)
						if err != nil {
							return "", nil, err
						}
						parts = append(parts, &genai.Part{
							InlineData: &genai.Blob{
								MIMEType: cp.MimeType,
								Data:     data,
							},
						})
					}
				}
			} else {
				parts = append(parts, genai.NewPartFromText(m.TextContent()))
			}
			out = append(out, &genai.Content{Role: "user", Parts: parts})
		case model.RoleAssistant:
			parts := geminiAssistantContentParts(m)
			if len(parts) > 0 {
				out = append(out, &genai.Content{Role: "model", Parts: parts})
			}
		case model.RoleTool:
			resp := m.ToolResponse()
			if resp == nil {
				continue
			}
			part := genai.NewPartFromFunctionResponse(resp.Name, resp.Result)
			if strings.TrimSpace(resp.ID) != "" && part != nil && part.FunctionResponse != nil {
				part.FunctionResponse.ID = resp.ID
			}
			out = append(out, &genai.Content{Role: "user", Parts: []*genai.Part{part}})
		}
	}
	return strings.Join(systemLines, "\n\n"), out, nil
}

func decodeBase64Image(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("model: invalid empty image content")
	}
	if data, err := base64.StdEncoding.DecodeString(raw); err == nil {
		return data, nil
	}
	if data, err := base64.RawStdEncoding.DecodeString(raw); err == nil {
		return data, nil
	}
	if data, err := base64.URLEncoding.DecodeString(raw); err == nil {
		return data, nil
	}
	if data, err := base64.RawURLEncoding.DecodeString(raw); err == nil {
		return data, nil
	}
	return nil, fmt.Errorf("model: invalid base64 image content")
}

func encodeGeminiThoughtSignature(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	return geminiThoughtSignaturePrefix + base64.StdEncoding.EncodeToString(raw)
}

func decodeGeminiThoughtSignature(encoded string) []byte {
	if encoded == "" {
		return nil
	}
	if payload, ok := strings.CutPrefix(encoded, geminiThoughtSignaturePrefix); ok {
		data, err := base64.StdEncoding.DecodeString(payload)
		if err == nil {
			return data
		}
	}
	// Backward compatibility: historical records stored plain text.
	return []byte(encoded)
}
