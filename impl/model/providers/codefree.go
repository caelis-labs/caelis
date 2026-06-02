package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/google/uuid"
)

func newCodeFreeHTTPClient(responseHeaderTimeout time.Duration) *http.Client {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if ok {
		clone := transport.Clone()
		clone.TLSHandshakeTimeout = 45 * time.Second
		clone.ResponseHeaderTimeout = responseHeaderTimeout
		return &http.Client{Transport: clone}
	}
	return &http.Client{}
}

func newCodeFreeChatHTTPClient() *http.Client {
	return newCodeFreeHTTPClient(0)
}

func newCodeFreeControlHTTPClient() *http.Client {
	return newCodeFreeHTTPClient(90 * time.Second)
}

const (
	codeFreeDefaultBaseURL        = "https://www.srdcloud.cn"
	codeFreeChatCompletionsPath   = "/api/acbackend/codechat/v1/completions"
	codeFreeVersionCheckPath      = "/api/acbackend/modelmgr/v1/clients/CLI/versions/0.3.6"
	codeFreeUserAPIKeyPath        = "/api/acbackend/usermanager/v1/users/apikey"
	codeFreeOAuthAuthorizePath    = "/login/oauth/authorize"
	codeFreeOAuthTokenPath        = "/login/oauth/access_token"
	codeFreeOAuthRedirectPath     = "/login/oauth-srdcloud-redirect"
	codeFreeDefaultOAuthClientID  = "251384680635inwsxjcm"
	codeFreeDefaultClientVersion  = "0.3.6"
	codeFreeCredsPathEnv          = "CODEFREE_OAUTH_CREDS_PATH"
	codeFreeClientVersionEnv      = "CODEFREE_CLIENT_VERSION"
	codeFreeClientIDEnv           = "CODEFREE_OAUTH_CLIENT_ID"
	codeFreeClientSecretEnv       = "CODEFREE_OAUTH_CLIENT_SECRET"
	codeFreeClientAuthMethodEnv   = "CODEFREE_OAUTH_CLIENT_AUTH_METHOD"
	codeFreeAuthorizationValue    = "Bearer codefree"
	codeFreeDefaultClientType     = "codefree-cli"
	codeFreeDefaultSubservice     = "cli_chat"
	codeFreeStreamAcceptValue     = "application/json, text/event-stream"
	codeFreeAPIKeyDecryptKey      = "Xtpa6sS&+D.NAo%CP8LA:7pk"
	codeFreeAPIKeyDecryptIV       = "%1KJIrl3!XUxr04V"
	codeFreeDefaultCredentialFile = "oauth_creds.json"
	codeFreeCredentialDir         = "providers/codefree"
	codeFreeLegacyCredentialDir   = ".codefree-cli"
	codeFreeResponseSummaryLimit  = 2048
)

type codeFreeLLM struct {
	name                string
	provider            string
	baseURL             string
	client              *http.Client
	requestTimeout      time.Duration
	firstEventTimeout   time.Duration
	maxOutputTok        int
	contextWindowTokens int
	options             openAICompatOptions
}

var codeFreeCompatProfile = openAICompatProfile{
	DisableReasoning: true,
	StructuredOutput: openAICompatStructuredOutputJSONOutput,
}

func newCodeFree(cfg Config) model.LLM {
	return &codeFreeLLM{
		name:                strings.TrimSpace(cfg.Model),
		provider:            cfg.Provider,
		baseURL:             strings.TrimSpace(cfg.BaseURL),
		client:              coalesceCodeFreeChatHTTPClient(cfg.HTTPClient),
		requestTimeout:      cfg.Timeout,
		firstEventTimeout:   normalizeStreamFirstEventTimeout(cfg.StreamFirstEventTimeout),
		maxOutputTok:        cfg.MaxOutputTok,
		contextWindowTokens: cfg.ContextWindowTokens,
		options:             openAICompatOptionsForProfile(codeFreeCompatProfile),
	}
}

func coalesceCodeFreeChatHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return newCodeFreeChatHTTPClient()
}

func coalesceCodeFreeControlHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return newCodeFreeControlHTTPClientFunc()
}

var newCodeFreeControlHTTPClientFunc = newCodeFreeControlHTTPClient

func (l *codeFreeLLM) Name() string {
	return l.name
}

func (l *codeFreeLLM) ProviderName() string {
	return l.provider
}

func (l *codeFreeLLM) ContextWindowTokens() int {
	return l.contextWindowTokens
}

func (l *codeFreeLLM) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		if req == nil {
			yield(nil, fmt.Errorf("model: request is nil"))
			return
		}
		creds, err := loadCodeFreeCredentials(ctx)
		if err != nil {
			yield(nil, err)
			return
		}
		payload := openAICompatRequest{
			Model:       l.name,
			Messages:    l.fromKernelMessages(req.Instructions, req.Messages),
			Tools:       fromKernelTools(model.FunctionToolDefinitions(req.Tools)),
			Stream:      req.Stream,
			MaxTokens:   l.maxOutputTok,
			Temperature: codeFreeFloat64Ptr(0),
			TopP:        codeFreeFloat64Ptr(1),
		}
		applyOpenAICompatOutput(&payload, req.Output, l.options.StructuredOutput)
		if req.Stream {
			payload.StreamOptions = &openAICompatStreamOptions{IncludeUsage: true}
		}
		if l.options.ApplyReasoning != nil {
			l.options.ApplyReasoning(&payload, req.Reasoning)
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			yield(nil, err)
			return
		}

		if _, err := l.generateOnce(ctx, req, raw, creds, yield); err != nil {
			yield(nil, err)
			return
		}
	}
}

func (l *codeFreeLLM) generateOnce(
	ctx context.Context,
	req *model.Request,
	raw []byte,
	creds codeFreeCredentials,
	yield func(*model.StreamEvent, error) bool,
) (bool, error) {
	runCtx := ctx
	cancel := func() {}
	if !req.Stream && l.requestTimeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, l.requestTimeout)
	}
	defer cancel()

	httpReq, err := http.NewRequestWithContext(runCtx, http.MethodPost, codeFreeChatEndpoint(l.baseURL), bytes.NewReader(raw))
	if err != nil {
		return false, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if req.Stream {
		httpReq.Header.Set("Accept", codeFreeStreamAcceptValue)
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	applyDefaultAttributionHeaders(httpReq, APICodeFree)
	applyCodeFreeHeaders(httpReq, creds, l.name)

	resp, err := l.client.Do(httpReq)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return false, statusError(resp)
	}

	bodyReader := bufio.NewReader(resp.Body)
	if !req.Stream {
		return emitCodeFreeNonStreamTurnDone(bodyReader, l.provider, resp.Header.Get("Content-Type"), yield)
	}

	if !codeFreeResponseLooksLikeSSE(resp, bodyReader) {
		return emitCodeFreeJSONTurnDone(bodyReader, l.provider, l.name, resp.Header.Get("Content-Type"), yield)
	}

	acc := openAIStreamAccumulator{
		role:      model.RoleAssistant,
		toolCalls: map[int]*openAICompatToolCall{},
	}
	var usage model.Usage
	finishReason := model.FinishReasonUnknown
	emitted := false
	stopped := false
	if err := readSSEWithFirstEventTimeout(bodyReader, l.firstEventTimeout, func(data []byte) error {
		if err := codeFreeResponseError(data, resp.Header.Get("Content-Type")); err != nil {
			return err
		}
		var chunk openAICompatStreamChunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			return err
		}
		if chunk.Usage.hasAny() {
			usage = chunk.Usage.toKernelUsage()
		}
		if len(chunk.Choices) == 0 {
			return nil
		}
		if one := normalizeOpenAICompatFinishReason(chunk.Choices[0].FinishReason); one != model.FinishReasonUnknown {
			finishReason = one
		}
		delta := chunk.Choices[0].Delta
		if strings.TrimSpace(delta.Role) != "" {
			acc.role = model.Role(delta.Role)
		}
		if text, ok := delta.Content.(string); ok && text != "" {
			acc.text.WriteString(text)
			emitted = true
			if !yield(&model.StreamEvent{
				Type:      model.StreamEventPartDelta,
				PartDelta: &model.PartDelta{Kind: model.PartKindText, TextDelta: text},
			}, nil) {
				stopped = true
				return errStopSSE
			}
		}
		if delta.ReasoningContent != "" {
			acc.reasoning.WriteString(delta.ReasoningContent)
			emitted = true
			if !yield(&model.StreamEvent{
				Type:      model.StreamEventPartDelta,
				PartDelta: &model.PartDelta{Kind: model.PartKindReasoning, TextDelta: delta.ReasoningContent},
			}, nil) {
				stopped = true
				return errStopSSE
			}
		}
		for _, tc := range delta.ToolCalls {
			entry := acc.toolCalls[tc.Index]
			if entry == nil {
				entry = &openAICompatToolCall{}
				acc.toolCalls[tc.Index] = entry
			}
			if tc.ID != "" {
				entry.ID = tc.ID
			}
			if tc.Function.Name != "" {
				entry.Function.Name = tc.Function.Name
			}
			entry.Function.Arguments += tc.Function.Arguments
		}
		return nil
	}); err != nil {
		return emitted, err
	}
	if stopped {
		return emitted, nil
	}
	finalMsg, err := acc.message()
	if err != nil {
		return emitted, err
	}
	emitted = true
	yield(&model.StreamEvent{
		Type: model.StreamEventTurnDone,
		Response: &model.Response{
			Message:      finalMsg,
			TurnComplete: true,
			StepComplete: true,
			Status:       model.ResponseStatusCompleted,
			FinishReason: finishReason,
			Model:        l.name,
			Provider:     l.provider,
			Usage:        usage,
		},
	}, nil)
	return emitted, nil
}

func codeFreeFloat64Ptr(value float64) *float64 {
	return &value
}

func codeFreeResponseLooksLikeSSE(resp *http.Response, reader *bufio.Reader) bool {
	if reader == nil {
		return true
	}
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

func decodeCodeFreeJSONResponse(reader *bufio.Reader, contentType string) (openAICompatResponse, []byte, error) {
	if reader == nil {
		return openAICompatResponse{}, nil, fmt.Errorf("model: empty codefree response body")
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		return openAICompatResponse{}, raw, err
	}
	if strings.TrimSpace(string(raw)) == "" {
		return openAICompatResponse{}, raw, fmt.Errorf("model: empty codefree response body")
	}
	var out openAICompatResponse
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&out); err != nil {
		return openAICompatResponse{}, raw, fmt.Errorf("model: decode codefree JSON response: %w%s", err, codeFreeResponseContext(raw, contentType))
	}
	return out, raw, nil
}

func emitCodeFreeNonStreamTurnDone(reader *bufio.Reader, provider string, contentType string, yield func(*model.StreamEvent, error) bool) (bool, error) {
	out, rawBody, err := decodeCodeFreeJSONResponse(reader, contentType)
	if err != nil {
		return false, err
	}
	if len(out.Choices) == 0 {
		return false, codeFreeEmptyChoicesError(rawBody, contentType)
	}
	msg, err := toKernelMessage(out.Choices[0].Message)
	if err != nil {
		return false, err
	}
	yield(&model.StreamEvent{
		Type: model.StreamEventTurnDone,
		Response: &model.Response{
			Message:      msg,
			TurnComplete: true,
			StepComplete: true,
			Status:       model.ResponseStatusCompleted,
			FinishReason: normalizeOpenAICompatFinishReason(out.Choices[0].FinishReason),
			Model:        out.Model,
			Provider:     provider,
			Usage:        out.Usage.toKernelUsage(),
		},
	}, nil)
	return true, nil
}

func emitCodeFreeJSONTurnDone(reader *bufio.Reader, provider string, fallbackModel string, contentType string, yield func(*model.StreamEvent, error) bool) (bool, error) {
	out, rawBody, err := decodeCodeFreeJSONResponse(reader, contentType)
	if err != nil {
		return false, err
	}
	if len(out.Choices) == 0 {
		return false, codeFreeEmptyChoicesError(rawBody, contentType)
	}
	msg, err := toKernelMessage(out.Choices[0].Message)
	if err != nil {
		return false, err
	}
	modelName := strings.TrimSpace(out.Model)
	if modelName == "" {
		modelName = strings.TrimSpace(fallbackModel)
	}
	yield(&model.StreamEvent{
		Type: model.StreamEventTurnDone,
		Response: &model.Response{
			Message:      msg,
			TurnComplete: true,
			StepComplete: true,
			Status:       model.ResponseStatusCompleted,
			FinishReason: normalizeOpenAICompatFinishReason(out.Choices[0].FinishReason),
			Model:        modelName,
			Provider:     provider,
			Usage:        out.Usage.toKernelUsage(),
		},
	}, nil)
	return true, nil
}

func codeFreeEmptyChoicesError(raw []byte, contentType string) error {
	if err := codeFreeResponseError(raw, contentType); err != nil {
		return err
	}
	return fmt.Errorf("model: empty choices%s", codeFreeResponseContext(raw, contentType))
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
	if text := strings.TrimSpace(e.Message); text != "" {
		parts = append(parts, "message="+fmt.Sprintf("%q", text))
	}
	if trimmed := strings.TrimSpace(e.ContentType); trimmed != "" {
		parts = append(parts, "content-type="+fmt.Sprintf("%q", trimmed))
	}
	if summary := summarizeCodeFreeResponseBody(e.Raw); summary != "" {
		parts = append(parts, "body="+summary)
	}
	if len(parts) == 0 {
		return "model: " + label
	}
	return "model: " + label + " (" + strings.Join(parts, " ") + ")"
}

func (e *codeFreeProviderError) Retryable() bool {
	return e != nil && e.Transient
}

func (e *codeFreeProviderError) Backpressure() bool {
	return e != nil && e.Transient
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
		Transient:   isRetryableCodeFreeResponseCode(code),
	}
}

func codeFreeResponseErrorCode(payload map[string]any) (int, string, bool) {
	for _, key := range []string{"retCode", "retcode", "code"} {
		value, ok := payload[key]
		if !ok {
			continue
		}
		code, ok := codeFreeIntValue(value)
		if ok {
			return code, key, true
		}
	}
	return 0, "", false
}

func codeFreeIntValue(value any) (int, bool) {
	switch typed := value.(type) {
	case json.Number:
		n, err := typed.Int64()
		if err == nil {
			return int(n), true
		}
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case string:
		var out int
		if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%d", &out); err == nil {
			return out, true
		}
	}
	return 0, false
}

func codeFreeResponseErrorMessage(payload map[string]any) string {
	for _, key := range []string{"message", "msg", "errMsg", "error_description", "description"} {
		if text, ok := payload[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	switch typed := payload["error"].(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		for _, key := range []string{"message", "msg", "description"} {
			if text, ok := typed[key].(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

func isRetryableCodeFreeResponseCode(code int) bool {
	return code == 51
}

func codeFreeResponseContext(raw []byte, contentType string) string {
	parts := make([]string, 0, 2)
	if trimmed := strings.TrimSpace(contentType); trimmed != "" {
		parts = append(parts, "content-type="+fmt.Sprintf("%q", trimmed))
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
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return ""
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err == nil {
		if encoded, marshalErr := json.Marshal(redactCodeFreeResponseValue("", payload)); marshalErr == nil {
			return truncateCodeFreeResponseSummary(string(encoded))
		}
	}
	return truncateCodeFreeResponseSummary(fmt.Sprintf("%q", text))
}

func redactCodeFreeResponseValue(key string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			out[childKey] = redactCodeFreeResponseValue(childKey, childValue)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, childValue := range typed {
			out[i] = redactCodeFreeResponseValue(key, childValue)
		}
		return out
	case string:
		return redactCodeFreeSensitiveField(key, typed)
	default:
		return redactCodeFreeSensitiveValue(key, value)
	}
}

func truncateCodeFreeResponseSummary(summary string) string {
	runes := []rune(strings.TrimSpace(summary))
	if len(runes) <= codeFreeResponseSummaryLimit {
		return string(runes)
	}
	return string(runes[:codeFreeResponseSummaryLimit]) + "...[truncated]"
}

func (l *codeFreeLLM) fromKernelMessages(instructions []model.Part, msgs []model.Message) []openAICompatReqMsg {
	compat := openAICompatLLM{options: l.options}
	return compat.fromKernelMessages(instructions, msgs)
}

func applyCodeFreeHeaders(req *http.Request, creds codeFreeCredentials, modelName string) {
	if req == nil {
		return
	}
	req.Header.Set("Authorization", codeFreeAuthorizationValue)
	req.Header.Set("Subservice", codeFreeDefaultSubservice)
	req.Header.Set("Clienttype", codeFreeDefaultClientType)
	req.Header.Set("Clientversion", codeFreeClientVersion())
	req.Header.Set("Userid", creds.UserID)
	req.Header.Set("Apikey", creds.APIKey)
	req.Header.Set("Sessionid", uuid.NewString())
	if strings.TrimSpace(modelName) != "" {
		req.Header.Set("Modelname", strings.TrimSpace(modelName))
	}
}

func codeFreeClientVersion() string {
	if value := strings.TrimSpace(os.Getenv(codeFreeClientVersionEnv)); value != "" {
		return value
	}
	return codeFreeDefaultClientVersion
}

func codeFreeChatEndpoint(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = codeFreeDefaultBaseURL
	}
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
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = codeFreeDefaultBaseURL
	}
	if strings.HasSuffix(strings.ToLower(base), strings.ToLower(codeFreeVersionCheckPath)) {
		return base
	}
	return base + codeFreeVersionCheckPath
}
