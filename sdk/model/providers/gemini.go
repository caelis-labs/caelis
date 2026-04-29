package providers

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/sdk/model"
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

		cfg := &genai.GenerateContentConfig{
			Tools: toGeminiTools(model.FunctionToolDefinitions(req.Tools)),
		}
		if strings.TrimSpace(system) != "" {
			cfg.SystemInstruction = &genai.Content{
				Parts: []*genai.Part{genai.NewPartFromText(system)},
			}
		}
		if l.maxOutputTok > 0 {
			cfg.MaxOutputTokens = int32(l.maxOutputTok)
		}
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
			role:      model.RoleAssistant,
			toolCalls: []model.ToolCall{},
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
			usage = geminiUsageFromResponse(out)

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
			if reasoning := msg.ReasoningText(); reasoning != "" {
				acc.reasoning.WriteString(reasoning)
				if !yield(&model.StreamEvent{
					Type:      model.StreamEventPartDelta,
					PartDelta: &model.PartDelta{Kind: model.PartKindReasoning, TextDelta: reasoning},
				}, nil) {
					return
				}
			}
			if text := msg.TextContent(); text != "" {
				acc.text.WriteString(text)
				if !yield(&model.StreamEvent{
					Type:      model.StreamEventPartDelta,
					PartDelta: &model.PartDelta{Kind: model.PartKindText, TextDelta: text},
				}, nil) {
					return
				}
			}
			if calls := msg.ToolCalls(); len(calls) > 0 {
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
				Model:        l.name,
				Provider:     l.provider,
				Usage:        usage,
			},
		}, nil)
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
	role      model.Role
	text      strings.Builder
	reasoning strings.Builder
	toolCalls []model.ToolCall
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
		PromptTokens:     int(out.UsageMetadata.PromptTokenCount),
		CompletionTokens: int(out.UsageMetadata.CandidatesTokenCount),
		TotalTokens:      int(out.UsageMetadata.TotalTokenCount),
	}
}

func geminiResponseToMessage(out *genai.GenerateContentResponse) (model.Message, model.Usage, error) {
	usage := geminiUsageFromResponse(out)
	if out == nil || len(out.Candidates) == 0 || out.Candidates[0] == nil || out.Candidates[0].Content == nil {
		return model.Message{}, usage, errGeminiNoCandidates
	}

	calls := make([]model.ToolCall, 0, len(out.Candidates[0].Content.Parts))
	textParts := make([]string, 0, len(out.Candidates[0].Content.Parts))
	reasoningParts := make([]string, 0, len(out.Candidates[0].Content.Parts))
	for _, part := range out.Candidates[0].Content.Parts {
		if part == nil {
			continue
		}
		if part.FunctionCall != nil {
			callID := strings.TrimSpace(part.FunctionCall.ID)
			if callID == "" {
				callID = part.FunctionCall.Name
			}
			calls = append(calls, model.ToolCall{
				ID:               callID,
				Name:             part.FunctionCall.Name,
				Args:             toolArgsRaw(part.FunctionCall.Args),
				ThoughtSignature: encodeGeminiThoughtSignature(part.ThoughtSignature),
			})
			continue
		}
		if strings.TrimSpace(part.Text) != "" {
			if part.Thought {
				reasoningParts = append(reasoningParts, part.Text)
			} else {
				textParts = append(textParts, part.Text)
			}
		}
	}
	return model.MessageFromAssistantParts(strings.TrimSpace(strings.Join(textParts, "\n")), strings.TrimSpace(strings.Join(reasoningParts, "\n")), calls), usage, nil
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
			calls := m.ToolCalls()
			parts := make([]*genai.Part, 0, len(calls)+1)
			if text := strings.TrimSpace(m.TextContent()); text != "" {
				parts = append(parts, genai.NewPartFromText(text))
			}
			for _, call := range calls {
				// Gemini tool loop requires thought signature in functionCall parts.
				// Skip legacy tool calls without signature to avoid request rejection.
				if call.ThoughtSignature == "" {
					continue
				}
				part := genai.NewPartFromFunctionCall(call.Name, toolArgsMap(call.Args))
				part.ThoughtSignature = decodeGeminiThoughtSignature(call.ThoughtSignature)
				parts = append(parts, part)
			}
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

func toGeminiTools(tools []model.ToolDefinition) []*genai.Tool {
	if len(tools) == 0 {
		return nil
	}
	declarations := make([]*genai.FunctionDeclaration, 0, len(tools))
	for _, one := range tools {
		declarations = append(declarations, &genai.FunctionDeclaration{
			Name:                 one.Name,
			Description:          one.Description,
			ParametersJsonSchema: one.Parameters,
		})
	}
	return []*genai.Tool{{FunctionDeclarations: declarations}}
}

func dedupToolCalls(calls []model.ToolCall) []model.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	index := map[string]int{}
	out := make([]model.ToolCall, 0, len(calls))
	for _, call := range calls {
		key := callKey(call)
		if pos, exists := index[key]; exists {
			out[pos] = mergeToolCall(out[pos], call)
			continue
		}
		index[key] = len(out)
		out = append(out, call)
	}
	return out
}

func callKey(call model.ToolCall) string {
	callID := strings.TrimSpace(call.ID)
	if callID != "" {
		return callID + "|" + call.Name
	}
	if strings.TrimSpace(call.Args) == "" {
		return call.Name
	}
	return call.Name + "|" + strings.TrimSpace(call.Args)
}

func mergeToolCall(oldCall model.ToolCall, newCall model.ToolCall) model.ToolCall {
	merged := oldCall
	if strings.TrimSpace(merged.ID) == "" {
		merged.ID = newCall.ID
	}
	if strings.TrimSpace(newCall.Name) != "" {
		merged.Name = newCall.Name
	}
	if merged.ThoughtSignature == "" && newCall.ThoughtSignature != "" {
		merged.ThoughtSignature = newCall.ThoughtSignature
	}
	if strings.TrimSpace(newCall.Args) != "" {
		merged.Args = newCall.Args
	}
	return merged
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
