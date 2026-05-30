// Package codefree provides a core-native CodeFree model provider.
package codefree

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/internal/version"
)

const defaultBaseURL = "https://www.srdcloud.cn"

const (
	chatCompletionsPath   = "/api/acbackend/codechat/v1/completions"
	versionCheckPath      = "/api/acbackend/modelmgr/v1/clients/CLI/versions/0.3.6"
	credsPathEnv          = "CODEFREE_OAUTH_CREDS_PATH"
	clientVersionEnv      = "CODEFREE_CLIENT_VERSION"
	defaultClientVersion  = "0.3.6"
	defaultCredentialFile = "oauth_creds.json"
	credentialDir         = "providers/codefree"
	authorizationValue    = "Bearer codefree"
	defaultClientType     = "codefree-cli"
	defaultSubservice     = "cli_chat"
	apiKeyDecryptKey      = "Xtpa6sS&+D.NAo%CP8LA:7pk"
	apiKeyDecryptIV       = "%1KJIrl3!XUxr04V"
)

type Config struct {
	ID              string
	BaseURL         string
	DefaultBaseURL  string
	Model           string
	MaxOutputTokens int
	CredentialPath  string
	Headers         map[string]string
	HTTPClient      *http.Client
}

type Provider struct {
	id              string
	baseURL         string
	model           string
	maxOutputTokens int
	credentialPath  string
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
		return nil, fmt.Errorf("model/codefree: invalid base url %q", baseURL)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	id := strings.TrimSpace(cfg.ID)
	if id == "" {
		id = "codefree"
	}
	return &Provider{
		id:              id,
		baseURL:         baseURL,
		model:           strings.TrimSpace(cfg.Model),
		maxOutputTokens: cfg.MaxOutputTokens,
		credentialPath:  strings.TrimSpace(cfg.CredentialPath),
		headers:         maps.Clone(cfg.Headers),
		client:          client,
	}, nil
}

func (p *Provider) ID() string {
	if p == nil || strings.TrimSpace(p.id) == "" {
		return "codefree"
	}
	return p.id
}

func (p *Provider) Models(ctx context.Context) ([]model.ModelInfo, error) {
	if p == nil {
		return nil, errors.New("model/codefree: provider is nil")
	}
	creds, err := loadCredentials(p.credentialPath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, versionEndpoint(p.baseURL), nil)
	if err != nil {
		return nil, err
	}
	p.setHeaders(req, creds)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, responseError("list models", resp)
	}
	var payload struct {
		Data []struct {
			ModelName       string `json:"modelName"`
			ModelType       string `json:"modelType"`
			MaxOutputTokens int    `json:"maxOutputTokens"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	out := make([]model.ModelInfo, 0, len(payload.Data))
	for _, item := range payload.Data {
		name := strings.TrimSpace(item.ModelName)
		if name == "" {
			continue
		}
		out = append(out, model.ModelInfo{
			ID:                  name,
			Name:                name,
			Provider:            p.ID(),
			ContextWindowTokens: 128000,
			MaxOutputTokens:     item.MaxOutputTokens,
			SupportsToolCalls:   true,
			SupportsImages:      true,
			SupportsJSON:        true,
			ReasoningEfforts:    nil,
		})
	}
	return out, nil
}

func (p *Provider) Stream(ctx context.Context, req model.Request) (model.Stream, error) {
	if p == nil {
		return nil, errors.New("model/codefree: provider is nil")
	}
	creds, err := loadCredentials(p.credentialPath)
	if err != nil {
		return nil, err
	}
	payload, err := p.chatRequest(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, chatEndpoint(p.baseURL), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	p.setHeaders(httpReq, creds)
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, responseError("chat completion", resp)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if err := responseBodyError(raw); err != nil {
		return nil, err
	}
	var completion chatCompletionResponse
	if err := json.Unmarshal(raw, &completion); err != nil {
		return nil, fmt.Errorf("model/codefree: decode chat completion: %w", err)
	}
	response, err := p.modelResponse(payload.Model, completion, raw)
	if err != nil {
		return nil, err
	}
	return &model.StaticStream{Events: []model.StreamEvent{{
		Type:     model.StreamTurnDone,
		Response: &response,
	}}}, nil
}

func (p *Provider) chatRequest(req model.Request) (chatCompletionRequest, error) {
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		modelID = p.model
	}
	if modelID == "" {
		return chatCompletionRequest{}, errors.New("model/codefree: model is required")
	}
	messages := make([]chatMessage, 0, len(req.Instructions)+len(req.Messages))
	for _, text := range req.Instructions {
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			messages = append(messages, chatMessage{Role: "system", Content: trimmed})
		}
	}
	for _, message := range req.Messages {
		converted, ok := chatMessageFromCore(message)
		if ok {
			messages = append(messages, converted)
		}
	}
	if len(messages) == 0 {
		return chatCompletionRequest{}, errors.New("model/codefree: at least one message is required")
	}
	payload := chatCompletionRequest{
		Model:          modelID,
		Messages:       messages,
		Tools:          chatTools(req.Tools),
		Stream:         false,
		MaxTokens:      p.maxOutputTokens,
		Temperature:    float64Ptr(0),
		TopP:           float64Ptr(1),
		ResponseFormat: outputResponseFormat(req.Output),
	}
	return payload, nil
}

func (p *Provider) modelResponse(modelID string, completion chatCompletionResponse, raw []byte) (model.Response, error) {
	if len(completion.Choices) == 0 {
		return model.Response{}, emptyChoicesError(raw)
	}
	choice := completion.Choices[0]
	message := coreMessageFromChat(choice.Message)
	message.Role = model.RoleAssistant
	message.Origin = &model.Origin{
		Provider:        p.ID(),
		Model:           firstNonEmpty(completion.Model, modelID),
		RawFinishReason: choice.FinishReason,
		CreatedAt:       unixTime(completion.Created),
	}
	usage := usageFromChat(completion.Usage)
	message.Usage = &usage
	return model.Response{
		Message: message,
		Status:  model.ResponseCompleted,
		Usage:   &usage,
		Origin:  message.Origin,
	}, nil
}

func (p *Provider) setHeaders(req *http.Request, creds credentials) {
	if req == nil {
		return
	}
	setHeaderDefault(req.Header, "User-Agent", caelisUserAgent())
	req.Header.Set("Authorization", authorizationValue)
	req.Header.Set("Subservice", defaultSubservice)
	req.Header.Set("Clienttype", defaultClientType)
	req.Header.Set("Clientversion", clientVersion())
	req.Header.Set("Userid", creds.UserID)
	req.Header.Set("Apikey", creds.APIKey)
	req.Header.Set("Sessionid", uuid.NewString())
	if strings.TrimSpace(p.model) != "" {
		req.Header.Set("Modelname", strings.TrimSpace(p.model))
	}
	for key, value := range p.headers {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			req.Header.Set(key, value)
		}
	}
}

type chatCompletionRequest struct {
	Model          string              `json:"model"`
	Messages       []chatMessage       `json:"messages"`
	Tools          []chatTool          `json:"tools,omitempty"`
	Stream         bool                `json:"stream"`
	MaxTokens      int                 `json:"max_tokens,omitempty"`
	Temperature    *float64            `json:"temperature,omitempty"`
	TopP           *float64            `json:"top_p,omitempty"`
	ResponseFormat *chatResponseFormat `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type chatToolCall struct {
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type chatResponseFormat struct {
	Type string `json:"type"`
}

type chatCompletionResponse struct {
	Model   string `json:"model"`
	Created int64  `json:"created"`
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage chatUsage `json:"usage"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func chatMessageFromCore(message model.Message) (chatMessage, bool) {
	switch message.Role {
	case model.RoleSystem:
		return chatMessage{Role: "system", Content: contentText(message.Parts)}, true
	case model.RoleUser:
		return chatMessage{Role: "user", Content: userContent(message.Parts)}, true
	case model.RoleAssistant:
		out := chatMessage{Role: "assistant"}
		if text := contentText(message.Parts); text != "" {
			out.Content = text
		}
		for _, call := range message.ToolCalls() {
			out.ToolCalls = append(out.ToolCalls, chatToolCall{
				ID:   strings.TrimSpace(call.ID),
				Type: "function",
				Function: chatToolFunction{
					Name:      strings.TrimSpace(call.Name),
					Arguments: strings.TrimSpace(string(call.Input)),
				},
			})
		}
		return out, out.Content != nil || len(out.ToolCalls) > 0
	case model.RoleTool:
		result := firstToolResult(message.Parts)
		if result == nil {
			return chatMessage{}, false
		}
		return chatMessage{
			Role:       "tool",
			ToolCallID: strings.TrimSpace(result.ToolCallID),
			Content:    contentText(result.Content),
		}, true
	default:
		return chatMessage{}, false
	}
}

func coreMessageFromChat(message chatMessage) model.Message {
	out := model.Message{Role: model.RoleAssistant}
	if text := chatContentText(message.Content); text != "" {
		out.Parts = append(out.Parts, model.NewTextPart(text))
	}
	for _, call := range message.ToolCalls {
		out.Parts = append(out.Parts, model.Part{
			Kind: model.PartToolUse,
			ToolUse: &model.ToolCall{
				ID:    strings.TrimSpace(call.ID),
				Name:  strings.TrimSpace(call.Function.Name),
				Input: normalizeToolInput(call.Function.Arguments),
			},
		})
	}
	return out
}

func userContent(parts []model.Part) any {
	if len(parts) == 0 {
		return ""
	}
	items := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		switch part.Kind {
		case model.PartText:
			if part.Text != nil && strings.TrimSpace(part.Text.Text) != "" {
				items = append(items, map[string]any{"type": "text", "text": part.Text.Text})
			}
		case model.PartJSON:
			if part.JSON != nil && len(part.JSON.Value) > 0 {
				items = append(items, map[string]any{"type": "text", "text": strings.TrimSpace(string(part.JSON.Value))})
			}
		case model.PartMedia:
			if part.Media == nil || part.Media.Modality != model.MediaImage {
				continue
			}
			if url := imageURL(part.Media); url != "" {
				items = append(items, map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": url,
					},
				})
			}
		case model.PartFileRef:
			if part.FileRef != nil {
				if text := strings.TrimSpace(firstNonEmpty(part.FileRef.URI, part.FileRef.LocalRef, part.FileRef.Name)); text != "" {
					items = append(items, map[string]any{"type": "text", "text": text})
				}
			}
		}
	}
	if len(items) == 0 {
		return ""
	}
	if len(items) == 1 {
		if text, ok := items[0]["text"].(string); ok && items[0]["type"] == "text" {
			return text
		}
	}
	return items
}

func imageURL(part *model.MediaPart) string {
	if part == nil {
		return ""
	}
	switch part.Source.Kind {
	case model.MediaURL:
		return strings.TrimSpace(part.Source.URI)
	case model.MediaInline:
		data := strings.TrimSpace(part.Source.Data)
		if data == "" {
			return ""
		}
		if strings.HasPrefix(data, "data:") {
			return data
		}
		mime := strings.TrimSpace(part.MimeType)
		if mime == "" {
			mime = "image/png"
		}
		return "data:" + mime + ";base64," + data
	default:
		return strings.TrimSpace(part.Source.URI)
	}
}

func firstToolResult(parts []model.Part) *model.ToolResultPart {
	for _, part := range parts {
		if part.Kind == model.PartToolResult && part.ToolResult != nil {
			result := *part.ToolResult
			result.Content = model.CloneParts(part.ToolResult.Content)
			return &result
		}
	}
	return nil
}

func contentText(parts []model.Part) string {
	if len(parts) == 0 {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part.Kind {
		case model.PartText:
			if part.Text != nil && strings.TrimSpace(part.Text.Text) != "" {
				texts = append(texts, strings.TrimSpace(part.Text.Text))
			}
		case model.PartJSON:
			if part.JSON != nil && len(part.JSON.Value) > 0 {
				texts = append(texts, strings.TrimSpace(string(part.JSON.Value)))
			}
		case model.PartToolResult:
			if part.ToolResult != nil {
				if text := contentText(part.ToolResult.Content); text != "" {
					texts = append(texts, text)
				}
			}
		}
	}
	return strings.Join(texts, "\n")
}

func chatContentText(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		texts := make([]string, 0, len(typed))
		for _, item := range typed {
			if obj, ok := item.(map[string]any); ok {
				if text, ok := obj["text"].(string); ok && strings.TrimSpace(text) != "" {
					texts = append(texts, strings.TrimSpace(text))
				}
			}
		}
		return strings.Join(texts, "\n")
	default:
		return ""
	}
}

func chatTools(specs []model.ToolSpec) []chatTool {
	if len(specs) == 0 {
		return nil
	}
	out := make([]chatTool, 0, len(specs))
	for _, spec := range specs {
		if spec.Kind != "" && spec.Kind != model.ToolSpecFunction {
			continue
		}
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			continue
		}
		out = append(out, chatTool{
			Type: "function",
			Function: chatFunction{
				Name:        name,
				Description: strings.TrimSpace(spec.Description),
				Parameters:  maps.Clone(spec.InputSchema),
			},
		})
	}
	return out
}

func outputResponseFormat(output *model.OutputSpec) *chatResponseFormat {
	if output == nil {
		return nil
	}
	switch output.Mode {
	case model.OutputJSON, model.OutputSchema:
		return &chatResponseFormat{Type: "json_object"}
	default:
		return nil
	}
}

func normalizeToolInput(input string) json.RawMessage {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(raw)) {
		return append(json.RawMessage(nil), raw...)
	}
	wrapped, _ := json.Marshal(map[string]any{"raw": raw})
	return wrapped
}

func usageFromChat(in chatUsage) model.Usage {
	total := in.TotalTokens
	if total == 0 {
		total = in.PromptTokens + in.CompletionTokens
	}
	return model.Usage{
		InputTokens:  in.PromptTokens,
		OutputTokens: in.CompletionTokens,
		TotalTokens:  total,
	}
}

type cachedCredentials struct {
	UserID                    string `json:"id_token"`
	APIKey                    string `json:"apikey"`
	AccessToken               string `json:"access_token,omitempty"`
	RefreshToken              string `json:"refresh_token,omitempty"`
	BaseURL                   string `json:"base_url,omitempty"`
	TokenType                 string `json:"token_type,omitempty"`
	ExpiresIn                 int64  `json:"expires_in,omitempty"`
	RefreshTokenExpiresIn     int64  `json:"refresh_token_expires_in,omitempty"`
	ExpiresAtUnixMilli        int64  `json:"expires_at,omitempty"`
	RefreshExpiresAtUnixMilli int64  `json:"refresh_expires_at,omitempty"`
	ObtainedAtUnixMilli       int64  `json:"obtained_at,omitempty"`
}

type credentials struct {
	UserID string
	APIKey string
	Path   string
}

func loadCredentials(configuredPath string) (credentials, error) {
	path, err := resolveCredentialPath(configuredPath)
	if err != nil {
		return credentials{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return credentials{}, fmt.Errorf("model/codefree: read credentials %q: %w", path, err)
	}
	var cached cachedCredentials
	if err := json.Unmarshal(raw, &cached); err != nil {
		return credentials{}, fmt.Errorf("model/codefree: decode credentials %q: %w", path, err)
	}
	userID := strings.TrimSpace(cached.UserID)
	if userID == "" {
		return credentials{}, fmt.Errorf("model/codefree: credentials missing id_token")
	}
	apiKey, err := decryptAPIKey(cached.APIKey)
	if err != nil {
		return credentials{}, err
	}
	return credentials{UserID: userID, APIKey: apiKey, Path: path}, nil
}

func resolveCredentialPath(configuredPath string) (string, error) {
	if path := strings.TrimSpace(configuredPath); path != "" {
		return path, nil
	}
	if path := strings.TrimSpace(os.Getenv(credsPathEnv)); path != "" {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("model/codefree: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".caelis", filepath.FromSlash(credentialDir), defaultCredentialFile), nil
}

func decryptAPIKey(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("model/codefree: credentials missing encrypted api key")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return "", fmt.Errorf("model/codefree: decode encrypted api key: %w", err)
	}
	block, err := aes.NewCipher([]byte(apiKeyDecryptKey))
	if err != nil {
		return "", fmt.Errorf("model/codefree: init api key cipher: %w", err)
	}
	if len(ciphertext) == 0 || len(ciphertext)%block.BlockSize() != 0 {
		return "", fmt.Errorf("model/codefree: invalid encrypted api key length")
	}
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, []byte(apiKeyDecryptIV)).CryptBlocks(plain, ciphertext)
	plain, err = trimPKCS7Padding(plain, block.BlockSize())
	if err != nil {
		return "", fmt.Errorf("model/codefree: unpad api key: %w", err)
	}
	apiKey := strings.TrimSpace(string(plain))
	if apiKey == "" {
		return "", fmt.Errorf("model/codefree: decrypted api key is empty")
	}
	return apiKey, nil
}

func trimPKCS7Padding(buf []byte, blockSize int) ([]byte, error) {
	if len(buf) == 0 || blockSize <= 0 || len(buf)%blockSize != 0 {
		return nil, fmt.Errorf("invalid pkcs7 buffer")
	}
	pad := int(buf[len(buf)-1])
	if pad == 0 || pad > blockSize || pad > len(buf) {
		return nil, fmt.Errorf("invalid pkcs7 padding")
	}
	for _, b := range buf[len(buf)-pad:] {
		if int(b) != pad {
			return nil, fmt.Errorf("invalid pkcs7 padding")
		}
	}
	return buf[:len(buf)-pad], nil
}

func responseBodyError(raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil || len(payload) == 0 {
		return nil
	}
	code, ok := responseCode(payload)
	if !ok || code == 0 {
		return nil
	}
	return fmt.Errorf("model/codefree: provider error code=%d message=%q", code, responseMessage(payload))
}

func responseCode(payload map[string]any) (int, bool) {
	for _, key := range []string{"retCode", "retcode", "code"} {
		value, ok := payload[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case json.Number:
			n, err := typed.Int64()
			return int(n), err == nil
		case float64:
			return int(typed), true
		case string:
			var out int
			if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%d", &out); err == nil {
				return out, true
			}
		}
	}
	return 0, false
}

func responseMessage(payload map[string]any) string {
	for _, key := range []string{"message", "msg", "errMsg", "error_description", "description"} {
		if text, ok := payload[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func emptyChoicesError(raw []byte) error {
	if err := responseBodyError(raw); err != nil {
		return err
	}
	return fmt.Errorf("model/codefree: response contains no choices")
}

func chatEndpoint(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = defaultBaseURL
	}
	lower := strings.ToLower(base)
	switch {
	case strings.HasSuffix(lower, strings.ToLower(chatCompletionsPath)):
		return base
	case strings.HasSuffix(lower, strings.TrimSuffix(strings.ToLower(chatCompletionsPath), "/completions")):
		return base + "/completions"
	default:
		return base + chatCompletionsPath
	}
}

func versionEndpoint(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = defaultBaseURL
	}
	if strings.HasSuffix(strings.ToLower(base), strings.ToLower(versionCheckPath)) {
		return base
	}
	return base + versionCheckPath
}

func responseError(operation string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	text := strings.TrimSpace(string(body))
	if text == "" {
		text = resp.Status
	}
	return fmt.Errorf("model/codefree: %s failed: %s", operation, text)
}

func clientVersion() string {
	if value := strings.TrimSpace(os.Getenv(clientVersionEnv)); value != "" {
		return value
	}
	return defaultClientVersion
}

func float64Ptr(value float64) *float64 {
	return &value
}

func unixTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(value, 0).UTC()
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
