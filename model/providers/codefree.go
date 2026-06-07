package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/model"
	"github.com/google/uuid"
)

const (
	defaultCodeFreeBaseURL      = "https://www.srdcloud.cn"
	codeFreeChatCompletionsPath = "/api/acbackend/codechat/v1/completions"
	codeFreeVersionCheckPath    = "/api/acbackend/modelmgr/v1/clients/CLI/versions/0.3.6"
	codeFreeAuthorizationValue  = "Bearer codefree"
	codeFreeDefaultSubservice   = "cli_chat"
	codeFreeDefaultClientType   = "codefree-cli"
	codeFreeDefaultVersion      = "0.3.6"
	codeFreeClientVersionEnv    = "CODEFREE_CLIENT_VERSION"
	codeFreeStreamAcceptValue   = "application/json, text/event-stream"
	codeFreeSummaryLimit        = 2048
)

// CodeFreeCredentials are the model-visible credentials required by CodeFree chat APIs.
type CodeFreeCredentials struct {
	UserID string
	APIKey string
}

// CodeFreeConfig configures CodeFree chat-completions access.
type CodeFreeConfig struct {
	Name                    string
	BaseURL                 string
	Model                   string
	Credentials             CodeFreeCredentials
	Headers                 map[string]string
	HTTPClient              *http.Client
	Timeout                 time.Duration
	StreamFirstEventTimeout time.Duration
	MaxOutputTok            int
}

type CodeFreeProvider struct {
	name              string
	provider          string
	baseURL           string
	model             string
	credentials       CodeFreeCredentials
	headers           map[string]string
	client            *http.Client
	requestTimeout    time.Duration
	firstEventTimeout time.Duration
	maxOutputTokens   int
}

func NewCodeFree(cfg CodeFreeConfig) *CodeFreeProvider {
	return &CodeFreeProvider{
		name:              cfg.Name,
		provider:          "codefree",
		baseURL:           normalizeCodeFreeBaseURL(cfg.BaseURL),
		model:             strings.TrimSpace(cfg.Model),
		credentials:       CodeFreeCredentials{UserID: strings.TrimSpace(cfg.Credentials.UserID), APIKey: strings.TrimSpace(cfg.Credentials.APIKey)},
		headers:           cloneHeaders(cfg.Headers),
		client:            coalesceHTTPClient(cfg.HTTPClient),
		requestTimeout:    cfg.Timeout,
		firstEventTimeout: normalizeStreamFirstEventTimeout(cfg.StreamFirstEventTimeout),
		maxOutputTokens:   cfg.MaxOutputTok,
	}
}

func DiscoverCodeFreeModels(ctx context.Context, cfg CodeFreeConfig) ([]RemoteModel, error) {
	if ctx == nil {
		return nil, fmt.Errorf("providers: context is required")
	}
	runCtx := ctx
	cancel := func() {}
	if cfg.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
	}
	defer cancel()
	req, err := http.NewRequestWithContext(runCtx, http.MethodGet, codeFreeVersionEndpoint(cfg.BaseURL), nil)
	if err != nil {
		return nil, err
	}
	applyLayer4CodeFreeHeaders(req, cfg.Credentials, strings.TrimSpace(cfg.Model))
	applyConfiguredHeaders(req, cfg.Headers)
	resp, err := coalesceHTTPClient(cfg.HTTPClient).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, statusError(resp)
	}
	var payload struct {
		Data []struct {
			ModelName       string `json:"modelName"`
			ModelType       string `json:"modelType"`
			MaxTokens       int    `json:"maxTokens"`
			MaxOutputTokens int    `json:"maxOutputTokens"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	models := make([]RemoteModel, 0, len(payload.Data))
	for _, item := range payload.Data {
		name := strings.TrimSpace(item.ModelName)
		if name == "" {
			continue
		}
		caps := []string{"text"}
		if strings.EqualFold(strings.TrimSpace(item.ModelType), "chat") {
			caps = appendUniqueStrings(caps, "tools")
		}
		models = append(models, RemoteModel{
			Name:                name,
			ContextWindowTokens: item.MaxTokens,
			MaxOutputTokens:     item.MaxOutputTokens,
			Capabilities:        caps,
		})
	}
	return normalizeRemoteModels(models), nil
}

func (p *CodeFreeProvider) Name() string { return p.model }

func (p *CodeFreeProvider) Generate(ctx context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	return func(yield func(model.ResponseEvent, error) bool) {
		if err := validateCodeFreeCredentials(p.credentials); err != nil {
			yield(model.ResponseEvent{}, err)
			return
		}
		stream := requestWantsStream(req)
		payload := p.buildRequest(req, stream)
		raw, err := json.Marshal(payload)
		if err != nil {
			yield(model.ResponseEvent{}, fmt.Errorf("marshal request: %w", err))
			return
		}
		for attempt := 0; attempt <= p.streamBackpressureRetries(stream); attempt++ {
			if err := p.generateOnce(ctx, raw, stream, yield); err != nil {
				if stream && attempt < p.streamBackpressureRetries(stream) && codeFreeIsBackpressure(err) {
					continue
				}
				yield(model.ResponseEvent{}, err)
			}
			return
		}
	}
}

func (p *CodeFreeProvider) generateOnce(ctx context.Context, raw []byte, stream bool, yield func(model.ResponseEvent, error) bool) error {
	runCtx := ctx
	cancel := func() {}
	if !stream && p.requestTimeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, p.requestTimeout)
	}
	defer cancel()
	httpReq, err := http.NewRequestWithContext(runCtx, http.MethodPost, codeFreeChatEndpoint(p.baseURL), bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if stream {
		httpReq.Header.Set("Accept", codeFreeStreamAcceptValue)
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	applyLayer4CodeFreeHeaders(httpReq, p.credentials, p.model)
	applyConfiguredHeaders(httpReq, p.headers)
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return statusError(resp)
	}
	reader := bufio.NewReader(resp.Body)
	if !stream || !codeFreeLooksLikeSSE(resp, reader) {
		return p.emitJSONResponse(reader, resp.Header.Get("Content-Type"), yield)
	}
	return p.emitSSEResponse(reader, resp.Header.Get("Content-Type"), yield)
}

func (p *CodeFreeProvider) streamBackpressureRetries(stream bool) int {
	if !stream {
		return 0
	}
	return 1
}

func (p *CodeFreeProvider) buildRequest(req model.Request, stream bool) map[string]any {
	payload := &openAIRequestPayload{
		Model:    p.model,
		Messages: make([]map[string]any, 0, len(req.Messages)),
		Stream:   stream,
	}
	for _, msg := range req.Messages {
		payload.Messages = append(payload.Messages, (&OpenAIProvider{}).buildMessage(msg))
	}
	if p.maxOutputTokens > 0 {
		payload.MaxTokens = p.maxOutputTokens
	}
	if req.MaxTokens > 0 {
		payload.MaxTokens = req.MaxTokens
	}
	applyJSONObjectOutput(payload, req.Output)
	if len(req.Tools) > 0 {
		payload.Tools = make([]map[string]any, 0, len(req.Tools))
		for _, tool := range req.Tools {
			payload.Tools = append(payload.Tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        tool.Name,
					"description": tool.Description,
					"parameters":  schemaToMap(tool.Schema),
				},
			})
		}
	}
	body := payload.toMap()
	body["temperature"] = float64(0)
	body["top_p"] = float64(1)
	if stream {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	return body
}

func (p *CodeFreeProvider) emitJSONResponse(reader *bufio.Reader, contentType string, yield func(model.ResponseEvent, error) bool) error {
	out, raw, err := decodeCodeFreeResponse(reader, contentType)
	if err != nil {
		return err
	}
	if len(out.Choices) == 0 {
		return codeFreeEmptyChoicesError(raw, contentType)
	}
	choice := out.Choices[0]
	if choice.Message.Content != "" {
		if !yield(model.ResponseEvent{TextDelta: choice.Message.Content}, nil) {
			return nil
		}
	}
	usage := choiceUsage(out.Usage)
	yield(model.ResponseEvent{FinishReason: choice.FinishReason, Usage: usage}, nil)
	return nil
}

func (p *CodeFreeProvider) emitSSEResponse(reader *bufio.Reader, contentType string, yield func(model.ResponseEvent, error) bool) error {
	var usage *model.Usage
	var finish string
	if err := readSSEWithFirstEventTimeout(reader, p.firstEventTimeout, func(data []byte) error {
		if err := codeFreeResponseError(data, contentType); err != nil {
			return err
		}
		var chunk codeFreeStreamChunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			return err
		}
		if chunk.Usage.hasAny() {
			u := chunk.Usage.toModelUsage()
			usage = &u
		}
		if len(chunk.Choices) == 0 {
			return nil
		}
		choice := chunk.Choices[0]
		if choice.FinishReason != "" {
			finish = choice.FinishReason
		}
		if choice.Delta.Content != "" {
			if !yield(model.ResponseEvent{TextDelta: choice.Delta.Content}, nil) {
				return errStopSSE
			}
		}
		if choice.Delta.ReasoningContent != "" {
			if !yield(model.ResponseEvent{ReasoningDelta: choice.Delta.ReasoningContent}, nil) {
				return errStopSSE
			}
		}
		return nil
	}); err != nil {
		return err
	}
	yield(model.ResponseEvent{FinishReason: finish, Usage: usage}, nil)
	return nil
}

type codeFreeResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage openAICompatUsage `json:"usage"`
}

type codeFreeStreamChunk struct {
	Choices []struct {
		Delta struct {
			Role             string `json:"role"`
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage openAICompatUsage `json:"usage"`
}

func decodeCodeFreeResponse(reader *bufio.Reader, contentType string) (codeFreeResponse, []byte, error) {
	raw, err := io.ReadAll(reader)
	if err != nil {
		return codeFreeResponse{}, raw, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return codeFreeResponse{}, raw, fmt.Errorf("model: empty codefree response body")
	}
	if err := codeFreeResponseError(raw, contentType); err != nil {
		return codeFreeResponse{}, raw, err
	}
	var out codeFreeResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return codeFreeResponse{}, raw, fmt.Errorf("model: decode codefree JSON response: %w%s", err, codeFreeResponseContext(raw, contentType))
	}
	return out, raw, nil
}

func choiceUsage(usage openAICompatUsage) *model.Usage {
	if !usage.hasAny() {
		return nil
	}
	u := usage.toModelUsage()
	return &u
}

func validateCodeFreeCredentials(creds CodeFreeCredentials) error {
	if strings.TrimSpace(creds.UserID) == "" {
		return fmt.Errorf("providers: codefree credentials missing user id")
	}
	if strings.TrimSpace(creds.APIKey) == "" {
		return fmt.Errorf("providers: codefree credentials missing api key")
	}
	return nil
}

func applyLayer4CodeFreeHeaders(req *http.Request, creds CodeFreeCredentials, modelName string) {
	req.Header.Set("Authorization", codeFreeAuthorizationValue)
	req.Header.Set("Subservice", codeFreeDefaultSubservice)
	req.Header.Set("Clienttype", codeFreeDefaultClientType)
	req.Header.Set("Clientversion", codeFreeClientVersion())
	req.Header.Set("Userid", strings.TrimSpace(creds.UserID))
	req.Header.Set("Apikey", strings.TrimSpace(creds.APIKey))
	req.Header.Set("Sessionid", uuid.NewString())
	if strings.TrimSpace(modelName) != "" {
		req.Header.Set("Modelname", strings.TrimSpace(modelName))
	}
}

func requestWantsStream(req model.Request) bool {
	if req.Metadata == nil {
		return false
	}
	value, ok := req.Metadata["stream"]
	if !ok {
		return false
	}
	enabled, _ := value.(bool)
	return enabled
}

func codeFreeLooksLikeSSE(resp *http.Response, reader *bufio.Reader) bool {
	sample, _ := reader.Peek(1024)
	trimmed := strings.TrimSpace(string(sample))
	switch {
	case strings.HasPrefix(trimmed, "data:"), strings.HasPrefix(trimmed, "event:"):
		return true
	case strings.HasPrefix(trimmed, "{"), strings.HasPrefix(trimmed, "["):
		return false
	}
	contentType := ""
	if resp != nil {
		contentType = strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	}
	return strings.Contains(contentType, "text/event-stream")
}

func codeFreeChatEndpoint(baseURL string) string {
	base := normalizeCodeFreeBaseURL(baseURL)
	lower := strings.ToLower(base)
	switch {
	case strings.HasSuffix(lower, strings.ToLower(codeFreeChatCompletionsPath)):
		return base
	case strings.HasSuffix(lower, strings.TrimSuffix(strings.ToLower(codeFreeChatCompletionsPath), "/completions")):
		return base + "/completions"
	default:
		return base + codeFreeChatCompletionsPath
	}
}

func codeFreeVersionEndpoint(baseURL string) string {
	return normalizeCodeFreeBaseURL(baseURL) + codeFreeVersionCheckPath
}

func normalizeCodeFreeBaseURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return defaultCodeFreeBaseURL
	}
	return base
}

func codeFreeClientVersion() string {
	if value := strings.TrimSpace(os.Getenv(codeFreeClientVersionEnv)); value != "" {
		return value
	}
	return codeFreeDefaultVersion
}

type codeFreeProviderError struct {
	CodeField   string
	Code        int
	Message     string
	ContentType string
	Raw         []byte
	Transient   bool
}

func (e *codeFreeProviderError) Error() string {
	label := "codefree error"
	if e.Transient {
		label = "codefree server overloaded"
	}
	parts := make([]string, 0, 4)
	if e.CodeField != "" && e.Code != 0 {
		parts = append(parts, fmt.Sprintf("%s=%d", e.CodeField, e.Code))
	}
	if strings.TrimSpace(e.Message) != "" {
		parts = append(parts, "message="+fmt.Sprintf("%q", strings.TrimSpace(e.Message)))
	}
	if strings.TrimSpace(e.ContentType) != "" {
		parts = append(parts, "content-type="+fmt.Sprintf("%q", strings.TrimSpace(e.ContentType)))
	}
	if summary := summarizeCodeFreeResponseBody(e.Raw); summary != "" {
		parts = append(parts, "body="+summary)
	}
	return "model: " + label + " (" + strings.Join(parts, " ") + ")"
}

func (e *codeFreeProviderError) Retryable() bool {
	return e != nil && e.Transient
}

func (e *codeFreeProviderError) Backpressure() bool {
	return e != nil && e.Transient
}

func codeFreeIsBackpressure(err error) bool {
	var providerErr *codeFreeProviderError
	return errors.As(err, &providerErr) && providerErr.Backpressure()
}

func codeFreeResponseError(raw []byte, contentType string) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil || len(payload) == 0 {
		return nil
	}
	code, field, ok := codeFreeResponseErrorCode(payload)
	if !ok || code == 0 {
		return nil
	}
	return &codeFreeProviderError{
		CodeField:   field,
		Code:        code,
		Message:     codeFreeResponseErrorMessage(payload),
		ContentType: contentType,
		Raw:         append([]byte(nil), raw...),
		Transient:   code == 51,
	}
}

func codeFreeResponseErrorCode(payload map[string]any) (int, string, bool) {
	for _, key := range []string{"retCode", "retcode", "code"} {
		if value, ok := payload[key]; ok {
			code, ok := toIntCodeFree(value)
			if ok {
				return code, key, true
			}
		}
	}
	return 0, "", false
}

func toIntCodeFree(value any) (int, bool) {
	switch typed := value.(type) {
	case json.Number:
		n, err := typed.Int64()
		return int(n), err == nil
	case float64:
		return int(typed), true
	case string:
		var n int
		_, err := fmt.Sscanf(strings.TrimSpace(typed), "%d", &n)
		return n, err == nil
	default:
		return 0, false
	}
}

func codeFreeResponseErrorMessage(payload map[string]any) string {
	for _, key := range []string{"message", "msg", "errMsg", "error_description", "description"} {
		if text, ok := payload[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func codeFreeEmptyChoicesError(raw []byte, contentType string) error {
	if err := codeFreeResponseError(raw, contentType); err != nil {
		return err
	}
	return fmt.Errorf("model: empty choices%s", codeFreeResponseContext(raw, contentType))
}

func codeFreeResponseContext(raw []byte, contentType string) string {
	var parts []string
	if strings.TrimSpace(contentType) != "" {
		parts = append(parts, "content-type="+fmt.Sprintf("%q", strings.TrimSpace(contentType)))
	}
	if summary := summarizeCodeFreeResponseBody(raw); summary != "" {
		parts = append(parts, "body="+summary)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, " ") + ")"
}

func summarizeCodeFreeResponseBody(raw []byte) string {
	var payload any
	if err := json.Unmarshal(raw, &payload); err == nil {
		if encoded, err := json.Marshal(redactCodeFreeValue("", payload)); err == nil {
			return truncateCodeFreeSummary(string(encoded))
		}
	}
	return truncateCodeFreeSummary(fmt.Sprintf("%q", strings.TrimSpace(string(raw))))
}

func redactCodeFreeValue(key string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			out[childKey] = redactCodeFreeValue(childKey, childValue)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, childValue := range typed {
			out[i] = redactCodeFreeValue(key, childValue)
		}
		return out
	case string:
		if isCodeFreeSensitiveKey(key) {
			return "[redacted]"
		}
		return typed
	default:
		return typed
	}
}

func isCodeFreeSensitiveKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(key, "key") || strings.Contains(key, "token") || strings.Contains(key, "session") || strings.Contains(key, "user")
}

func truncateCodeFreeSummary(summary string) string {
	runes := []rune(strings.TrimSpace(summary))
	if len(runes) <= codeFreeSummaryLimit {
		return string(runes)
	}
	return string(runes[:codeFreeSummaryLimit]) + "...[truncated]"
}
