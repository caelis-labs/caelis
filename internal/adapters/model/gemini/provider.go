// Package gemini provides a core-native Gemini API model provider.
package gemini

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/internal/version"
)

const defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"
const replayKindThoughtSignature = "thought_signature"
const thoughtSignaturePrefix = "b64:"
const streamAcceptValue = "text/event-stream"

type Config struct {
	ID              string
	BaseURL         string
	DefaultBaseURL  string
	APIKey          string
	AuthHeader      string
	Model           string
	MaxOutputTokens int
	Headers         map[string]string
	HTTPClient      *http.Client
}

type Provider struct {
	id              string
	baseURL         string
	apiKey          string
	authHeader      string
	model           string
	maxOutputTokens int
	headers         map[string]string
	client          *http.Client
}

func New(cfg Config) (*Provider, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(firstNonEmpty(cfg.BaseURL, cfg.DefaultBaseURL, defaultBaseURL)), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("model/gemini: invalid base url %q", baseURL)
	}
	authHeader := strings.TrimSpace(cfg.AuthHeader)
	if authHeader == "" {
		authHeader = "x-goog-api-key"
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	id := strings.TrimSpace(cfg.ID)
	if id == "" {
		id = "gemini"
	}
	return &Provider{
		id:              id,
		baseURL:         baseURL,
		apiKey:          strings.TrimSpace(cfg.APIKey),
		authHeader:      authHeader,
		model:           strings.TrimSpace(cfg.Model),
		maxOutputTokens: cfg.MaxOutputTokens,
		headers:         maps.Clone(cfg.Headers),
		client:          client,
	}, nil
}

func (p *Provider) ID() string {
	if p == nil || strings.TrimSpace(p.id) == "" {
		return "gemini"
	}
	return p.id
}

func (p *Provider) Models(ctx context.Context) ([]model.ModelInfo, error) {
	if p == nil {
		return nil, errors.New("model/gemini: provider is nil")
	}
	var out []model.ModelInfo
	pageToken := ""
	for i := 0; i < 5; i++ {
		endpoint := p.baseURL + "/models"
		query := url.Values{"pageSize": []string{"1000"}}
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		endpoint += "?" + query.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		p.setHeaders(req)
		resp, err := p.client.Do(req)
		if err != nil {
			return nil, err
		}
		var payload listModelsResponse
		readErr := decodeResponse(resp, "list models", &payload)
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		for _, item := range payload.Models {
			id := normalizeModelName(item.Name)
			if id == "" {
				continue
			}
			out = append(out, model.ModelInfo{
				ID:                  id,
				Name:                firstNonEmpty(item.DisplayName, id),
				Provider:            p.ID(),
				ContextWindowTokens: item.InputTokenLimit,
				MaxOutputTokens:     item.OutputTokenLimit,
				SupportsToolCalls:   supportsGenerateContent(item.SupportedGenerationMethods),
				SupportsImages:      true,
				SupportsJSON:        true,
				ReasoningEfforts:    []string{"none", "low", "medium", "high"},
			})
		}
		pageToken = strings.TrimSpace(payload.NextPageToken)
		if pageToken == "" {
			break
		}
	}
	return out, nil
}

func (p *Provider) Stream(ctx context.Context, req model.Request) (model.Stream, error) {
	if p == nil {
		return nil, errors.New("model/gemini: provider is nil")
	}
	payload, modelID, err := p.generateRequest(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	endpoint := p.baseURL + "/models/" + url.PathEscape(modelID) + ":generateContent"
	if req.Stream {
		endpoint = p.baseURL + "/models/" + url.PathEscape(modelID) + ":streamGenerateContent?alt=sse"
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if req.Stream {
		httpReq.Header.Set("Accept", streamAcceptValue)
	}
	p.setHeaders(httpReq)
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := responseError("generate content", resp)
		_ = resp.Body.Close()
		return nil, err
	}
	bodyReader := bufio.NewReader(resp.Body)
	if req.Stream && responseLooksLikeSSE(resp, bodyReader) {
		return newGenerateSSEStream(bodyReader, resp.Body, p.ID(), modelID), nil
	}
	defer resp.Body.Close()
	var completion generateContentResponse
	if err := json.NewDecoder(bodyReader).Decode(&completion); err != nil {
		return nil, err
	}
	response, err := p.modelResponse(modelID, completion)
	if err != nil {
		return nil, err
	}
	return &model.StaticStream{Events: []model.StreamEvent{{
		Type:     model.StreamTurnDone,
		Response: &response,
	}}}, nil
}

func (p *Provider) generateRequest(req model.Request) (generateContentRequest, string, error) {
	modelID := normalizeRequestModelID(req.Model)
	if modelID == "" {
		modelID = normalizeRequestModelID(p.model)
	}
	if modelID == "" {
		return generateContentRequest{}, "", errors.New("model/gemini: model is required")
	}
	system := append([]string(nil), req.Instructions...)
	contents := make([]content, 0, len(req.Messages))
	for _, message := range req.Messages {
		if message.Role == model.RoleSystem {
			if text := contentText(message.Parts); text != "" {
				system = append(system, text)
			}
			continue
		}
		converted, ok := contentFromCore(message)
		if ok {
			contents = append(contents, converted)
		}
	}
	if len(contents) == 0 {
		return generateContentRequest{}, "", errors.New("model/gemini: at least one message is required")
	}
	cfg := &generationConfig{
		MaxOutputTokens: p.maxOutputTokens,
		ThinkingConfig:  thinkingConfig(modelID, req.Reasoning),
	}
	applyOutputConfig(cfg, req.Output)
	if cfg.empty() {
		cfg = nil
	}
	payload := generateContentRequest{
		Contents:         contents,
		GenerationConfig: cfg,
		Tools:            toolParams(req.Tools),
	}
	if text := systemText(system); text != "" {
		payload.SystemInstruction = &content{Parts: []part{{Text: text}}}
	}
	return payload, modelID, nil
}

func contentFromCore(message model.Message) (content, bool) {
	switch message.Role {
	case model.RoleUser:
		parts := userParts(message.Parts)
		return content{Role: "user", Parts: parts}, len(parts) > 0
	case model.RoleAssistant:
		parts := assistantParts(message.Parts)
		return content{Role: "model", Parts: parts}, len(parts) > 0
	case model.RoleTool:
		parts := toolResultParts(message.Parts)
		return content{Role: "user", Parts: parts}, len(parts) > 0
	default:
		return content{}, false
	}
}

func (p *Provider) modelResponse(modelID string, completion generateContentResponse) (model.Response, error) {
	if len(completion.Candidates) == 0 || len(completion.Candidates[0].Content.Parts) == 0 {
		return model.Response{}, errors.New("model/gemini: response contains no candidates")
	}
	candidate := completion.Candidates[0]
	message := model.Message{
		Role: model.RoleAssistant,
		Origin: &model.Origin{
			Provider:        p.ID(),
			Model:           firstNonEmpty(completion.ModelVersion, modelID),
			RawFinishReason: candidate.FinishReason,
		},
	}
	for _, item := range candidate.Content.Parts {
		if item.FunctionCall != nil {
			callID := strings.TrimSpace(item.FunctionCall.ID)
			if callID == "" {
				callID = strings.TrimSpace(item.FunctionCall.Name)
			}
			call := model.ToolCall{
				ID:    callID,
				Name:  strings.TrimSpace(item.FunctionCall.Name),
				Input: normalizeToolInput(item.FunctionCall.Args),
			}
			if replay := replayFromThoughtSignature(p.ID(), item.ThoughtSignature); replay != nil {
				call.Replay = replay
			}
			message.Parts = append(message.Parts, model.Part{Kind: model.PartToolUse, ToolUse: &call})
			continue
		}
		if strings.TrimSpace(item.Text) == "" {
			continue
		}
		if item.Thought {
			message.Parts = append(message.Parts, model.NewReasoningPart(item.Text, model.ReasoningVisible))
		} else {
			message.Parts = append(message.Parts, model.NewTextPart(item.Text))
		}
	}
	usage := usageFromResponse(completion.UsageMetadata)
	message.Usage = &usage
	return model.Response{
		Message: message,
		Status:  model.ResponseCompleted,
		Usage:   &usage,
		Origin:  message.Origin,
	}, nil
}

func (p *Provider) setHeaders(req *http.Request) {
	if req == nil {
		return
	}
	setHeaderDefault(req.Header, "User-Agent", caelisUserAgent())
	for key, value := range p.headers {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			req.Header.Set(key, value)
		}
	}
	if p.apiKey == "" {
		return
	}
	if strings.EqualFold(p.authHeader, "Authorization") {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
		return
	}
	req.Header.Set(p.authHeader, p.apiKey)
}

type generateContentRequest struct {
	Contents          []content         `json:"contents"`
	SystemInstruction *content          `json:"systemInstruction,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
	Tools             []toolParam       `json:"tools,omitempty"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type part struct {
	Text             string            `json:"text,omitempty"`
	Thought          bool              `json:"thought,omitempty"`
	ThoughtSignature string            `json:"thoughtSignature,omitempty"`
	InlineData       *blob             `json:"inlineData,omitempty"`
	FileData         *fileData         `json:"fileData,omitempty"`
	FunctionCall     *functionCall     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponse `json:"functionResponse,omitempty"`
}

type blob struct {
	MIMEType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
}

type fileData struct {
	MIMEType string `json:"mimeType,omitempty"`
	FileURI  string `json:"fileUri,omitempty"`
}

type functionCall struct {
	ID   string         `json:"id,omitempty"`
	Name string         `json:"name,omitempty"`
	Args map[string]any `json:"args,omitempty"`
}

type functionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name,omitempty"`
	Response map[string]any `json:"response,omitempty"`
}

type generationConfig struct {
	MaxOutputTokens int                    `json:"maxOutputTokens,omitempty"`
	ResponseMIME    string                 `json:"responseMimeType,omitempty"`
	ResponseSchema  map[string]any         `json:"responseSchema,omitempty"`
	ThinkingConfig  *thinkingConfigPayload `json:"thinkingConfig,omitempty"`
}

type thinkingConfigPayload struct {
	IncludeThoughts bool   `json:"includeThoughts,omitempty"`
	ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
}

type toolParam struct {
	FunctionDeclarations []functionDeclaration `json:"functionDeclarations,omitempty"`
}

type functionDeclaration struct {
	Name                 string         `json:"name"`
	Description          string         `json:"description,omitempty"`
	ParametersJSONSchema map[string]any `json:"parametersJsonSchema,omitempty"`
}

type generateContentResponse struct {
	ModelVersion  string      `json:"modelVersion,omitempty"`
	Candidates    []candidate `json:"candidates,omitempty"`
	UsageMetadata usage       `json:"usageMetadata,omitempty"`
}

type candidate struct {
	Content      content `json:"content,omitempty"`
	FinishReason string  `json:"finishReason,omitempty"`
}

type usage struct {
	PromptTokenCount        int `json:"promptTokenCount,omitempty"`
	CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
	CandidatesTokenCount    int `json:"candidatesTokenCount,omitempty"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount,omitempty"`
	TotalTokenCount         int `json:"totalTokenCount,omitempty"`
}

type generateSSEStream struct {
	body   io.Closer
	events <-chan generateStreamItem
	once   sync.Once
}

type generateStreamItem struct {
	event model.StreamEvent
	err   error
}

func newGenerateSSEStream(reader *bufio.Reader, body io.ReadCloser, providerID string, modelID string) model.Stream {
	events := make(chan generateStreamItem, 32)
	stream := &generateSSEStream{body: body, events: events}
	go readGenerateSSE(reader, body, providerID, modelID, events)
	return stream
}

func (s *generateSSEStream) Recv() (model.StreamEvent, error) {
	if s == nil || s.events == nil {
		return model.StreamEvent{}, io.EOF
	}
	item, ok := <-s.events
	if !ok {
		return model.StreamEvent{}, io.EOF
	}
	if item.err != nil {
		return model.StreamEvent{}, item.err
	}
	return item.event, nil
}

func (s *generateSSEStream) Close() error {
	if s == nil || s.body == nil {
		return nil
	}
	var err error
	s.once.Do(func() {
		err = s.body.Close()
	})
	return err
}

func readGenerateSSE(reader *bufio.Reader, body io.Closer, providerID string, modelID string, out chan<- generateStreamItem) {
	defer close(out)
	defer body.Close()
	acc := newGenerateStreamAccumulator(providerID, modelID)
	err := readSSE(reader, func(data []byte) error {
		if err := streamChunkError(data); err != nil {
			return err
		}
		var chunk generateContentResponse
		if err := json.Unmarshal(data, &chunk); err != nil {
			return err
		}
		for _, event := range acc.apply(chunk) {
			if !sendGenerateStreamItem(out, generateStreamItem{event: event}) {
				return errStopSSE
			}
		}
		return nil
	})
	if err != nil {
		_ = sendGenerateStreamItem(out, generateStreamItem{err: err})
		return
	}
	response, err := acc.response()
	if err != nil {
		_ = sendGenerateStreamItem(out, generateStreamItem{err: err})
		return
	}
	_ = sendGenerateStreamItem(out, generateStreamItem{event: model.StreamEvent{
		Type:     model.StreamTurnDone,
		Response: &response,
	}})
}

func sendGenerateStreamItem(out chan<- generateStreamItem, item generateStreamItem) bool {
	out <- item
	return true
}

type generateStreamAccumulator struct {
	providerID   string
	modelID      string
	modelVersion string
	finishReason string
	parts        []part
	usage        usage
}

func newGenerateStreamAccumulator(providerID string, modelID string) *generateStreamAccumulator {
	return &generateStreamAccumulator{
		providerID: providerID,
		modelID:    strings.TrimSpace(modelID),
	}
}

func (a *generateStreamAccumulator) apply(chunk generateContentResponse) []model.StreamEvent {
	if a == nil {
		return nil
	}
	if strings.TrimSpace(chunk.ModelVersion) != "" {
		a.modelVersion = strings.TrimSpace(chunk.ModelVersion)
	}
	if chunk.UsageMetadata.hasAny() {
		a.usage = chunk.UsageMetadata
	}
	if len(chunk.Candidates) == 0 {
		return nil
	}
	candidate := chunk.Candidates[0]
	if strings.TrimSpace(candidate.FinishReason) != "" {
		a.finishReason = strings.TrimSpace(candidate.FinishReason)
	}
	var events []model.StreamEvent
	for _, item := range candidate.Content.Parts {
		a.parts = append(a.parts, item)
		if item.Text == "" {
			continue
		}
		if item.Thought {
			events = append(events, reasoningDeltaEvent(item.Text))
		} else {
			events = append(events, textDeltaEvent(item.Text))
		}
	}
	return events
}

func (a *generateStreamAccumulator) response() (model.Response, error) {
	completion := generateContentResponse{
		ModelVersion: firstNonEmpty(a.modelVersion, a.modelID),
		Candidates: []candidate{{
			Content:      content{Role: "model", Parts: a.parts},
			FinishReason: strings.TrimSpace(a.finishReason),
		}},
		UsageMetadata: a.usage,
	}
	provider := &Provider{id: a.providerID}
	return provider.modelResponse(a.modelID, completion)
}

func textDeltaEvent(text string) model.StreamEvent {
	return model.StreamEvent{
		Type:  model.StreamPartDelta,
		Delta: text,
		Part:  ptrPart(model.NewTextPart(text)),
	}
}

func reasoningDeltaEvent(text string) model.StreamEvent {
	return model.StreamEvent{
		Type:  model.StreamPartDelta,
		Delta: text,
		Part:  ptrPart(model.NewReasoningPart(text, model.ReasoningVisible)),
	}
}

func ptrPart(part model.Part) *model.Part {
	return &part
}

func (u usage) hasAny() bool {
	return u.PromptTokenCount != 0 ||
		u.CachedContentTokenCount != 0 ||
		u.CandidatesTokenCount != 0 ||
		u.ThoughtsTokenCount != 0 ||
		u.TotalTokenCount != 0
}

type listModelsResponse struct {
	Models []struct {
		Name                       string   `json:"name"`
		DisplayName                string   `json:"displayName"`
		InputTokenLimit            int      `json:"inputTokenLimit"`
		OutputTokenLimit           int      `json:"outputTokenLimit"`
		SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
	} `json:"models"`
	NextPageToken string `json:"nextPageToken"`
}

func userParts(parts []model.Part) []part {
	if len(parts) == 0 {
		return nil
	}
	out := make([]part, 0, len(parts))
	for _, item := range parts {
		switch item.Kind {
		case model.PartText:
			if item.Text != nil && strings.TrimSpace(item.Text.Text) != "" {
				out = append(out, part{Text: item.Text.Text})
			}
		case model.PartJSON:
			if item.JSON != nil && len(item.JSON.Value) > 0 {
				out = append(out, part{Text: strings.TrimSpace(string(item.JSON.Value))})
			}
		case model.PartMedia:
			if item.Media != nil {
				if converted, ok := mediaPart(item.Media); ok {
					out = append(out, converted)
				}
			}
		case model.PartFileRef:
			if item.FileRef != nil {
				if converted, ok := fileRefPart(item.FileRef); ok {
					out = append(out, converted)
				}
			}
		}
	}
	return out
}

func assistantParts(parts []model.Part) []part {
	if len(parts) == 0 {
		return nil
	}
	out := make([]part, 0, len(parts))
	for _, item := range parts {
		switch item.Kind {
		case model.PartText:
			if item.Text != nil && strings.TrimSpace(item.Text.Text) != "" {
				out = append(out, part{Text: item.Text.Text})
			}
		case model.PartReasoning:
			if item.Reasoning != nil && strings.TrimSpace(item.Reasoning.VisibleText) != "" {
				out = append(out, part{Text: item.Reasoning.VisibleText, Thought: true})
			}
		case model.PartToolUse:
			if item.ToolUse == nil {
				continue
			}
			signature := thoughtSignatureToken(item.ToolUse.Replay)
			if signature == "" {
				continue
			}
			out = append(out, part{
				ThoughtSignature: signature,
				FunctionCall: &functionCall{
					ID:   strings.TrimSpace(item.ToolUse.ID),
					Name: strings.TrimSpace(item.ToolUse.Name),
					Args: toolArgsMap(item.ToolUse.Input),
				},
			})
		}
	}
	return out
}

func toolResultParts(parts []model.Part) []part {
	if len(parts) == 0 {
		return nil
	}
	out := make([]part, 0, len(parts))
	for _, item := range parts {
		if item.Kind != model.PartToolResult || item.ToolResult == nil {
			continue
		}
		result := *item.ToolResult
		out = append(out, part{FunctionResponse: &functionResponse{
			ID:       strings.TrimSpace(result.ToolCallID),
			Name:     strings.TrimSpace(result.Name),
			Response: functionResponsePayload(result),
		}})
	}
	return out
}

func mediaPart(item *model.MediaPart) (part, bool) {
	if item == nil {
		return part{}, false
	}
	mimeType := strings.TrimSpace(item.MimeType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	switch item.Source.Kind {
	case model.MediaInline:
		data := strings.TrimSpace(item.Source.Data)
		if data == "" {
			return part{}, false
		}
		if mediaType, payload, ok := strings.Cut(strings.TrimPrefix(data, "data:"), ";base64,"); ok {
			if strings.TrimSpace(mediaType) != "" {
				mimeType = strings.TrimSpace(mediaType)
			}
			data = strings.TrimSpace(payload)
		}
		return part{InlineData: &blob{MIMEType: mimeType, Data: data}}, true
	case model.MediaURL:
		uri := strings.TrimSpace(item.Source.URI)
		if uri == "" {
			return part{}, false
		}
		return part{FileData: &fileData{MIMEType: mimeType, FileURI: uri}}, true
	default:
		return part{}, false
	}
}

func fileRefPart(item *model.FileRefPart) (part, bool) {
	if item == nil {
		return part{}, false
	}
	if uri := strings.TrimSpace(item.URI); uri != "" {
		return part{FileData: &fileData{MIMEType: item.MimeType, FileURI: uri}}, true
	}
	if text := strings.TrimSpace(firstNonEmpty(item.LocalRef, item.FileID, item.Name)); text != "" {
		return part{Text: text}, true
	}
	return part{}, false
}

func toolParams(specs []model.ToolSpec) []toolParam {
	if len(specs) == 0 {
		return nil
	}
	declarations := make([]functionDeclaration, 0, len(specs))
	for _, spec := range specs {
		if spec.Kind != "" && spec.Kind != model.ToolSpecFunction {
			continue
		}
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			continue
		}
		schema := maps.Clone(spec.InputSchema)
		if len(schema) == 0 {
			schema = map[string]any{"type": "object"}
		}
		declarations = append(declarations, functionDeclaration{
			Name:                 name,
			Description:          strings.TrimSpace(spec.Description),
			ParametersJSONSchema: schema,
		})
	}
	if len(declarations) == 0 {
		return nil
	}
	return []toolParam{{FunctionDeclarations: declarations}}
}

func functionResponsePayload(result model.ToolResultPart) map[string]any {
	value := toolResultValue(result.Content)
	if result.IsError {
		return map[string]any{"error": value}
	}
	return map[string]any{"output": value}
}

func toolResultValue(parts []model.Part) any {
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 && parts[0].Kind == model.PartJSON && parts[0].JSON != nil && len(parts[0].JSON.Value) > 0 {
		var value any
		if err := json.Unmarshal(parts[0].JSON.Value, &value); err == nil {
			return value
		}
	}
	if text := contentText(parts); text != "" {
		return text
	}
	raw, err := json.Marshal(parts)
	if err != nil {
		return ""
	}
	return string(raw)
}

func contentText(parts []model.Part) string {
	if len(parts) == 0 {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, item := range parts {
		switch item.Kind {
		case model.PartText:
			if item.Text != nil && strings.TrimSpace(item.Text.Text) != "" {
				texts = append(texts, strings.TrimSpace(item.Text.Text))
			}
		case model.PartJSON:
			if item.JSON != nil && len(item.JSON.Value) > 0 {
				texts = append(texts, strings.TrimSpace(string(item.JSON.Value)))
			}
		case model.PartToolResult:
			if item.ToolResult != nil {
				if text := contentText(item.ToolResult.Content); text != "" {
					texts = append(texts, text)
				}
			}
		case model.PartFileRef:
			if item.FileRef != nil {
				if text := strings.TrimSpace(firstNonEmpty(item.FileRef.URI, item.FileRef.LocalRef, item.FileRef.FileID, item.FileRef.Name)); text != "" {
					texts = append(texts, text)
				}
			}
		}
	}
	return strings.Join(texts, "\n")
}

func systemText(values []string) string {
	if len(values) == 0 {
		return ""
	}
	lines := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return strings.Join(lines, "\n\n")
}

func applyOutputConfig(cfg *generationConfig, output *model.OutputSpec) {
	if cfg == nil || output == nil {
		return
	}
	switch output.Mode {
	case model.OutputJSON:
		cfg.ResponseMIME = "application/json"
	case model.OutputSchema:
		cfg.ResponseMIME = "application/json"
		cfg.ResponseSchema = maps.Clone(output.JSONSchema)
	}
}

func (cfg *generationConfig) empty() bool {
	return cfg == nil ||
		(cfg.MaxOutputTokens <= 0 && strings.TrimSpace(cfg.ResponseMIME) == "" &&
			len(cfg.ResponseSchema) == 0 && cfg.ThinkingConfig == nil)
}

func thinkingConfig(modelID string, reasoning model.ReasoningConfig) *thinkingConfigPayload {
	effort := strings.ToLower(strings.TrimSpace(reasoning.Effort))
	disabled := effort == "none"
	explicit := effort != "" || reasoning.Budget > 0
	if !explicit {
		return nil
	}
	if geminiUsesThinkingBudget(modelID) {
		budget := thinkingBudget(effort, reasoning.Budget)
		if disabled {
			budget = 0
		}
		return &thinkingConfigPayload{IncludeThoughts: !disabled, ThinkingBudget: &budget}
	}
	level := thinkingLevel(effort)
	if level == "" {
		return nil
	}
	return &thinkingConfigPayload{IncludeThoughts: !disabled, ThinkingLevel: level}
}

func geminiUsesThinkingBudget(modelID string) bool {
	major, ok := geminiMajorVersion(modelID)
	return ok && major < 3
}

func geminiMajorVersion(modelID string) (int, bool) {
	name := normalizeRequestModelID(modelID)
	_, tail, found := strings.Cut(strings.ToLower(name), "gemini-")
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
	return major, err == nil
}

func thinkingBudget(effort string, explicit int) int {
	if explicit > 0 {
		return explicit
	}
	switch effort {
	case "minimal":
		return 512
	case "low":
		return 1024
	case "medium":
		return 4096
	case "high", "xhigh":
		return 8192
	default:
		return 4096
	}
}

func thinkingLevel(effort string) string {
	switch effort {
	case "none", "minimal":
		return "MINIMAL"
	case "low":
		return "LOW"
	case "medium":
		return "MEDIUM"
	case "high", "xhigh":
		return "HIGH"
	default:
		return ""
	}
}

func usageFromResponse(in usage) model.Usage {
	total := in.TotalTokenCount
	if total == 0 {
		total = in.PromptTokenCount + in.CandidatesTokenCount + in.ThoughtsTokenCount
	}
	return model.Usage{
		InputTokens:       in.PromptTokenCount,
		CachedInputTokens: in.CachedContentTokenCount,
		OutputTokens:      in.CandidatesTokenCount,
		ReasoningTokens:   in.ThoughtsTokenCount,
		TotalTokens:       total,
	}
}

func normalizeToolInput(args map[string]any) json.RawMessage {
	if len(args) == 0 {
		return json.RawMessage(`{}`)
	}
	raw, err := json.Marshal(args)
	if err != nil || !json.Valid(raw) {
		return json.RawMessage(`{}`)
	}
	return raw
}

func toolArgsMap(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err == nil && value != nil {
		return value
	}
	return map[string]any{}
}

func replayFromThoughtSignature(provider string, signature string) *model.ReplayMeta {
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return nil
	}
	return &model.ReplayMeta{
		Provider: provider,
		Kind:     replayKindThoughtSignature,
		Token:    thoughtSignaturePrefix + signature,
	}
}

func thoughtSignatureToken(replay *model.ReplayMeta) string {
	if replay == nil || strings.TrimSpace(replay.Kind) != replayKindThoughtSignature {
		return ""
	}
	token := strings.TrimSpace(replay.Token)
	if payload, ok := strings.CutPrefix(token, thoughtSignaturePrefix); ok {
		return strings.TrimSpace(payload)
	}
	return ""
}

func normalizeRequestModelID(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "gemini/")
	value = strings.TrimPrefix(value, "models/")
	return strings.TrimSpace(value)
}

func normalizeModelName(value string) string {
	return normalizeRequestModelID(value)
}

func supportsGenerateContent(methods []string) bool {
	if len(methods) == 0 {
		return true
	}
	for _, method := range methods {
		if strings.EqualFold(strings.TrimSpace(method), "generateContent") {
			return true
		}
	}
	return false
}

func decodeResponse(resp *http.Response, operation string, out any) error {
	if resp == nil {
		return errors.New("model/gemini: response is nil")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseError(operation, resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return err
	}
	return nil
}

func responseError(operation string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
			Code    int    `json:"code"`
		} `json:"error"`
	}
	providerErr := model.ProviderError{
		Provider:   "gemini",
		Operation:  operation,
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Body:       strings.TrimSpace(string(body)),
	}
	if err := json.Unmarshal(body, &payload); err == nil && strings.TrimSpace(payload.Error.Message) != "" {
		providerErr.Message = payload.Error.Message
		providerErr.Type = payload.Error.Status
		if payload.Error.Code != 0 {
			providerErr.Code = fmt.Sprint(payload.Error.Code)
		}
	}
	return model.NewProviderError(providerErr)
}

func streamChunkError(raw []byte) error {
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
			Code    int    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || strings.TrimSpace(payload.Error.Message) == "" {
		return nil
	}
	providerErr := model.ProviderError{
		Provider:  "gemini",
		Operation: "generate content",
		Message:   strings.TrimSpace(payload.Error.Message),
		Type:      strings.TrimSpace(payload.Error.Status),
		Body:      strings.TrimSpace(string(raw)),
	}
	if payload.Error.Code != 0 {
		providerErr.Code = fmt.Sprint(payload.Error.Code)
	}
	return model.NewProviderError(providerErr)
}

func responseLooksLikeSSE(resp *http.Response, reader *bufio.Reader) bool {
	contentType := ""
	if resp != nil {
		contentType = strings.ToLower(resp.Header.Get("Content-Type"))
	}
	if reader == nil {
		return strings.Contains(contentType, "text/event-stream")
	}
	sample, _ := reader.Peek(1)
	trimmed := strings.TrimSpace(string(sample))
	switch {
	case strings.HasPrefix(trimmed, "d"), strings.HasPrefix(trimmed, "e"):
		return true
	case strings.HasPrefix(trimmed, "{"), strings.HasPrefix(trimmed, "["):
		return false
	}
	return strings.Contains(contentType, "text/event-stream")
}

var errStopSSE = errors.New("model/gemini: stop sse")

func readSSE(reader io.Reader, onData func([]byte) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var dataLines [][]byte
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := bytes.Join(dataLines, []byte("\n"))
		dataLines = dataLines[:0]
		chunk := strings.TrimSpace(string(payload))
		if chunk == "" {
			return nil
		}
		if chunk == "[DONE]" {
			return errStopSSE
		}
		return onData([]byte(chunk))
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			if err := flush(); err != nil {
				if errors.Is(err, errStopSSE) {
					return nil
				}
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))))
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("model/gemini: sse scanner: %w", err)
	}
	if err := flush(); err != nil && !errors.Is(err, errStopSSE) {
		return err
	}
	return nil
}

func caelisUserAgent() string {
	value := strings.TrimSpace(version.String())
	value = strings.TrimPrefix(value, "v")
	if value == "" {
		value = "dev"
	}
	return "caelis/" + value
}

func setHeaderDefault(headers http.Header, key string, value string) {
	if headers == nil || strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
		return
	}
	if strings.TrimSpace(headers.Get(key)) != "" {
		return
	}
	headers.Set(key, value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

var _ model.Provider = (*Provider)(nil)
