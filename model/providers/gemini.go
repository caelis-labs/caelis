package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/model"
)

const (
	defaultGeminiBaseURL    = "https://generativelanguage.googleapis.com"
	defaultGeminiAPIVersion = "v1beta"
	geminiProviderName      = "gemini"
)

const geminiThoughtSignaturePrefix = "b64:"

// GeminiConfig holds configuration for Google's Gemini API.
type GeminiConfig struct {
	Name                    string
	BaseURL                 string
	Token                   string
	Model                   string
	Headers                 map[string]string
	HTTPClient              *http.Client
	Timeout                 time.Duration
	StreamFirstEventTimeout time.Duration
	MaxOutputTok            int
}

// GeminiProvider implements model.LLM using the native Gemini generateContent API.
type GeminiProvider struct {
	name              string
	provider          string
	baseURL           string
	apiVersion        string
	token             string
	model             string
	headers           map[string]string
	client            *http.Client
	requestTimeout    time.Duration
	firstEventTimeout time.Duration
	maxOutputTokens   int
}

func NewGemini(cfg GeminiConfig) *GeminiProvider {
	baseURL, version := splitGeminiBaseURL(cfg.BaseURL)
	modelName := strings.TrimSpace(cfg.Model)
	return &GeminiProvider{
		name:              firstNonEmpty(strings.TrimSpace(cfg.Name), modelName),
		provider:          geminiProviderName,
		baseURL:           baseURL,
		apiVersion:        version,
		token:             strings.TrimSpace(cfg.Token),
		model:             modelName,
		headers:           cloneHeaders(cfg.Headers),
		client:            coalesceHTTPClient(cfg.HTTPClient),
		requestTimeout:    cfg.Timeout,
		firstEventTimeout: normalizeStreamFirstEventTimeout(cfg.StreamFirstEventTimeout),
		maxOutputTokens:   cfg.MaxOutputTok,
	}
}

func (p *GeminiProvider) Name() string { return p.model }

func (p *GeminiProvider) Generate(ctx context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	return func(yield func(model.ResponseEvent, error) bool) {
		if ctx == nil {
			ctx = context.Background()
		}
		body, err := p.buildRequest(req)
		if err != nil {
			yield(model.ResponseEvent{}, err)
			return
		}
		data, err := json.Marshal(body)
		if err != nil {
			yield(model.ResponseEvent{}, fmt.Errorf("marshal request: %w", err))
			return
		}

		runCtx := ctx
		cancel := func() {}
		if !requestWantsStream(req) && p.requestTimeout > 0 {
			runCtx, cancel = context.WithTimeout(ctx, p.requestTimeout)
		}
		defer cancel()

		method := "generateContent"
		if requestWantsStream(req) {
			method = "streamGenerateContent"
		}
		httpReq, err := http.NewRequestWithContext(runCtx, http.MethodPost, p.endpoint(method), bytes.NewReader(data))
		if err != nil {
			yield(model.ResponseEvent{}, fmt.Errorf("create request: %w", err))
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		applyConfiguredHeaders(httpReq, p.headers)
		q := httpReq.URL.Query()
		if p.token != "" {
			q.Set("key", p.token)
		}
		if requestWantsStream(req) {
			q.Set("alt", "sse")
		}
		httpReq.URL.RawQuery = q.Encode()

		resp, err := p.client.Do(httpReq)
		if err != nil {
			yield(model.ResponseEvent{}, fmt.Errorf("http request: %w", err))
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			yield(model.ResponseEvent{}, statusError(resp))
			return
		}

		if requestWantsStream(req) {
			p.generateStream(resp, yield)
			return
		}

		var payload geminiResponse
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			yield(model.ResponseEvent{}, fmt.Errorf("decode response: %w", err))
			return
		}
		p.emitResponse(payload, yield)
	}
}

func (p *GeminiProvider) generateStream(resp *http.Response, yield func(model.ResponseEvent, error) bool) {
	err := readSSEWithFirstEventTimeout(resp.Body, p.firstEventTimeout, func(data []byte) error {
		var payload geminiResponse
		if err := json.Unmarshal(data, &payload); err != nil {
			if !yield(model.ResponseEvent{}, fmt.Errorf("SSE parse error: %w", err)) {
				return errStopSSE
			}
			return nil
		}
		if !p.emitResponse(payload, yield) {
			return errStopSSE
		}
		return nil
	})
	if err != nil {
		yield(model.ResponseEvent{}, err)
	}
}

func (p *GeminiProvider) emitResponse(resp geminiResponse, yield func(model.ResponseEvent, error) bool) bool {
	usage := resp.UsageMetadata.toModelUsage()
	for _, candidate := range resp.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				evt := model.ResponseEvent{}
				if part.Thought {
					evt.ReasoningDelta = part.Text
				} else {
					evt.TextDelta = part.Text
				}
				if !yield(evt, nil) {
					return false
				}
			}
			if part.FunctionCall != nil {
				evt := model.ResponseEvent{ToolCall: &model.ToolCallDelta{
					CallID: part.FunctionCall.ID,
					Name:   part.FunctionCall.Name,
					Args:   cloneProviderMap(part.FunctionCall.Args),
				}}
				if token := encodeGeminiAPIThoughtSignature(part.ThoughtSignature); token != "" {
					evt.Metadata = map[string]any{"thought_signature": token}
				}
				if !yield(evt, nil) {
					return false
				}
			}
		}
	}
	if resp.UsageMetadata.hasAny() {
		if !yield(model.ResponseEvent{Usage: &usage}, nil) {
			return false
		}
	}
	return true
}

func (p *GeminiProvider) buildRequest(req model.Request) (map[string]any, error) {
	payload := map[string]any{
		"contents":         p.geminiContents(req.Messages),
		"generationConfig": p.generationConfig(req),
	}
	if system := geminiSystemInstruction(req.Messages); len(system) > 0 {
		payload["systemInstruction"] = map[string]any{"parts": system}
	}
	if tools := geminiTools(req.Tools); len(tools) > 0 {
		payload["tools"] = tools
	}
	return payload, nil
}

func (p *GeminiProvider) generationConfig(req model.Request) map[string]any {
	cfg := map[string]any{}
	if p.maxOutputTokens > 0 {
		cfg["maxOutputTokens"] = p.maxOutputTokens
	}
	if req.MaxTokens > 0 {
		cfg["maxOutputTokens"] = req.MaxTokens
	}
	if req.Output != nil {
		if req.Output.MaxOutputTokens > 0 {
			cfg["maxOutputTokens"] = req.Output.MaxOutputTokens
		}
		switch req.Output.Mode {
		case model.OutputModeJSON:
			cfg["responseMimeType"] = "application/json"
		case model.OutputModeSchema:
			cfg["responseMimeType"] = "application/json"
			if len(req.Output.JSONSchema) > 0 {
				cfg["responseSchema"] = cloneProviderMap(req.Output.JSONSchema)
			}
		}
	}
	if req.Temperature != nil {
		cfg["temperature"] = *req.Temperature
	}
	if thinking := geminiThinkingConfig(p.model, req.Reasoning); len(thinking) > 0 {
		cfg["thinkingConfig"] = thinking
	}
	return cfg
}

func (p *GeminiProvider) geminiContents(messages []model.Message) []map[string]any {
	contents := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == model.RoleSystem {
			continue
		}
		parts := geminiMessageParts(msg)
		if len(parts) == 0 {
			continue
		}
		role := "user"
		if msg.Role == model.RoleAssistant {
			role = "model"
		}
		contents = append(contents, map[string]any{"role": role, "parts": parts})
	}
	return contents
}

func geminiSystemInstruction(messages []model.Message) []map[string]any {
	var parts []map[string]any
	for _, msg := range messages {
		if msg.Role != model.RoleSystem {
			continue
		}
		for _, part := range msg.Content {
			if strings.TrimSpace(part.Text) != "" {
				parts = append(parts, map[string]any{"text": part.Text})
			}
		}
	}
	return parts
}

func geminiMessageParts(msg model.Message) []map[string]any {
	parts := make([]map[string]any, 0, len(msg.Content))
	for _, part := range msg.Content {
		if part.Text != "" {
			parts = append(parts, map[string]any{"text": part.Text})
		}
		if part.Reasoning != nil && part.Reasoning.Visibility != model.ReasoningVisibilityRedacted && part.Reasoning.Text != "" {
			parts = append(parts, map[string]any{"text": part.Reasoning.Text, "thought": true})
		}
		if part.InlineData != nil && len(part.InlineData.Data) > 0 {
			parts = append(parts, map[string]any{"inlineData": map[string]any{
				"mimeType": part.InlineData.MIMEType,
				"data":     base64.StdEncoding.EncodeToString(part.InlineData.Data),
			}})
		}
		if part.ToolUse != nil {
			toolPart := geminiFunctionCallPart(part.ToolUse)
			if len(toolPart) > 0 {
				parts = append(parts, toolPart)
			}
		}
		if part.ToolResult != nil {
			parts = append(parts, geminiFunctionResponsePart(part.ToolResult))
		}
	}
	return parts
}

func geminiFunctionCallPart(tool *model.ToolUse) map[string]any {
	if tool == nil {
		return nil
	}
	token, _ := tool.ProviderMeta["thought_signature"].(string)
	raw := decodeGeminiThoughtSignature(token)
	if len(raw) == 0 {
		return nil
	}
	args, err := geminiToolArgs(tool)
	if err != nil {
		args = map[string]any{}
	}
	return map[string]any{
		"functionCall": map[string]any{
			"id":   tool.CallID,
			"name": tool.Name,
			"args": args,
		},
		"thoughtSignature": base64.StdEncoding.EncodeToString(raw),
	}
}

func geminiFunctionResponsePart(result *model.ToolResult) map[string]any {
	response := map[string]any{}
	if strings.TrimSpace(result.Content) != "" {
		if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
			response = map[string]any{"content": result.Content}
		}
	}
	if result.IsError {
		response["error"] = true
	}
	return map[string]any{"functionResponse": map[string]any{
		"id":       result.CallID,
		"name":     result.CallID,
		"response": response,
	}}
}

func geminiToolArgs(tool *model.ToolUse) (map[string]any, error) {
	if len(tool.Args) > 0 {
		return cloneProviderMap(tool.Args), nil
	}
	return toolArgsMap(tool.ArgJSON)
}

func geminiTools(tools []model.ToolSpec) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	declarations := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		item := map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  schemaToMap(tool.Schema),
		}
		declarations = append(declarations, item)
	}
	return []map[string]any{{"functionDeclarations": declarations}}
}

func geminiThinkingConfig(modelName string, reasoning model.ReasoningConfig) map[string]any {
	effort := strings.ToLower(strings.TrimSpace(reasoning.Effort))
	if effort == "" && !reasoning.Include && reasoning.BudgetTokens <= 0 {
		return nil
	}
	includeThoughts := reasoning.Include || effort != "" && effort != "none"
	if geminiUsesThinkingBudget(modelName) {
		budget := reasoning.BudgetTokens
		if budget <= 0 {
			budget = geminiThinkingBudget(effort)
		}
		if effort == "none" {
			budget = 0
			includeThoughts = false
		}
		return map[string]any{"includeThoughts": includeThoughts, "thinkingBudget": budget}
	}
	if effort == "none" {
		return map[string]any{"includeThoughts": false, "thinkingLevel": "LOW"}
	}
	return map[string]any{"includeThoughts": includeThoughts, "thinkingLevel": geminiThinkingLevel(effort)}
}

func geminiUsesThinkingBudget(modelName string) bool {
	major, ok := geminiMajorVersion(modelName)
	return !ok || major < 3
}

func geminiMajorVersion(modelName string) (int, bool) {
	lower := strings.ToLower(modelName)
	idx := strings.Index(lower, "gemini-")
	if idx < 0 {
		return 0, false
	}
	rest := lower[idx+len("gemini-"):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	major, err := strconv.Atoi(rest[:end])
	return major, err == nil
}

func geminiThinkingBudget(effort string) int {
	switch effort {
	case "minimal":
		return 512
	case "low":
		return 1024
	case "medium":
		return 4096
	case "high", "xhigh":
		return 8192
	case "none":
		return 0
	default:
		return 0
	}
}

func geminiThinkingLevel(effort string) string {
	switch effort {
	case "minimal":
		return "MINIMAL"
	case "low":
		return "LOW"
	case "medium":
		return "MEDIUM"
	case "high", "xhigh":
		return "HIGH"
	default:
		return "LOW"
	}
}

func (p *GeminiProvider) endpoint(method string) string {
	u, _ := url.Parse(p.baseURL)
	u.Path = path.Join(u.Path, p.apiVersion, "models", p.model+":"+method)
	return u.String()
}

func splitGeminiBaseURL(raw string) (string, string) {
	base := normalizeProviderBaseURL(raw, defaultGeminiBaseURL)
	version := defaultGeminiAPIVersion
	u, err := url.Parse(base)
	if err != nil {
		return base, version
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 {
		return strings.TrimRight(u.String(), "/"), version
	}
	last := parts[len(parts)-1]
	if isGeminiAPIVersion(last) {
		version = last
		parts = parts[:len(parts)-1]
		u.Path = strings.TrimRight("/"+strings.Join(parts, "/"), "/")
	}
	return strings.TrimRight(u.String(), "/"), version
}

func isGeminiAPIVersion(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "v1" || strings.HasPrefix(value, "v1beta") || strings.HasPrefix(value, "v1alpha")
}

func encodeGeminiThoughtSignature(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	return geminiThoughtSignaturePrefix + base64.StdEncoding.EncodeToString(raw)
}

func decodeGeminiThoughtSignature(encoded string) []byte {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil
	}
	if strings.HasPrefix(encoded, geminiThoughtSignaturePrefix) {
		payload := strings.TrimPrefix(encoded, geminiThoughtSignaturePrefix)
		raw, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return []byte(payload)
		}
		return raw
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err == nil && len(raw) > 0 {
		return raw
	}
	return []byte(encoded)
}

func encodeGeminiAPIThoughtSignature(encoded string) string {
	raw := decodeGeminiThoughtSignature(encoded)
	return encodeGeminiThoughtSignature(raw)
}

type geminiResponse struct {
	Candidates    []geminiCandidate `json:"candidates"`
	UsageMetadata geminiUsage       `json:"usageMetadata"`
}

type geminiCandidate struct {
	Content geminiContent `json:"content"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string              `json:"text"`
	Thought          bool                `json:"thought"`
	FunctionCall     *geminiFunctionCall `json:"functionCall"`
	ThoughtSignature string              `json:"thoughtSignature"`
}

type geminiFunctionCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type geminiUsage struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
}

func (u geminiUsage) hasAny() bool {
	return u.PromptTokenCount != 0 ||
		u.CachedContentTokenCount != 0 ||
		u.CandidatesTokenCount != 0 ||
		u.ThoughtsTokenCount != 0 ||
		u.TotalTokenCount != 0
}

func (u geminiUsage) toModelUsage() model.Usage {
	total := u.TotalTokenCount
	if total == 0 && (u.PromptTokenCount != 0 || u.CandidatesTokenCount != 0) {
		total = u.PromptTokenCount + u.CandidatesTokenCount
	}
	return model.Usage{
		PromptTokens:      u.PromptTokenCount,
		CachedInputTokens: u.CachedContentTokenCount,
		CompletionTokens:  u.CandidatesTokenCount,
		ReasoningTokens:   u.ThoughtsTokenCount,
		TotalTokens:       total,
	}
}

func DiscoverGeminiModels(ctx context.Context, cfg GeminiConfig) ([]RemoteModel, error) {
	if ctx == nil {
		return nil, fmt.Errorf("providers: context is required")
	}
	runCtx := ctx
	var cancel context.CancelFunc
	if cfg.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	client := coalesceHTTPClient(cfg.HTTPClient)
	baseURL, version := splitGeminiBaseURL(cfg.BaseURL)
	u, _ := url.Parse(baseURL)
	u.Path = path.Join(u.Path, version, "models")
	q := u.Query()
	if strings.TrimSpace(cfg.Token) != "" {
		q.Set("key", strings.TrimSpace(cfg.Token))
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(runCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	applyConfiguredHeaders(req, cfg.Headers)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, statusError(resp)
	}
	var payload struct {
		Models []struct {
			Name                       string   `json:"name"`
			InputTokenLimit            int      `json:"inputTokenLimit"`
			OutputTokenLimit           int      `json:"outputTokenLimit"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	models := make([]RemoteModel, 0, len(payload.Models))
	for _, item := range payload.Models {
		name := strings.TrimPrefix(strings.TrimSpace(item.Name), "models/")
		if name == "" {
			continue
		}
		models = append(models, RemoteModel{
			Name:                name,
			ContextWindowTokens: item.InputTokenLimit,
			MaxOutputTokens:     item.OutputTokenLimit,
			Capabilities:        appendUniqueStrings(nil, item.SupportedGenerationMethods...),
		})
	}
	return normalizeRemoteModels(models), nil
}
