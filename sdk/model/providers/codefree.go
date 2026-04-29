package providers

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/sdk/model"
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
	codeFreeAPIKeyDecryptKey      = "Xtpa6sS&+D.NAo%CP8LA:7pk"
	codeFreeAPIKeyDecryptIV       = "%1KJIrl3!XUxr04V"
	codeFreeDefaultCredentialFile = "oauth_creds.json"
	codeFreeCredentialDir         = "providers/codefree"
	codeFreeLegacyCredentialDir   = ".codefree-cli"
)

type codeFreeLLM struct {
	name                string
	provider            string
	baseURL             string
	client              *http.Client
	requestTimeout      time.Duration
	maxOutputTok        int
	contextWindowTokens int
	options             openAICompatOptions
}

type codeFreeCachedCredentials struct {
	AccessToken               string `json:"access_token,omitempty"`
	RefreshToken              string `json:"refresh_token,omitempty"`
	UserID                    string `json:"id_token"`
	APIKey                    string `json:"apikey"`
	BaseURL                   string `json:"baseUrl,omitempty"`
	TokenType                 string `json:"token_type,omitempty"`
	ExpiresIn                 int64  `json:"expires_in,omitempty"`
	RefreshTokenExpiresIn     int64  `json:"refresh_token_expires_in,omitempty"`
	ObtainedAtUnixMilli       int64  `json:"obtained_at_unix_ms,omitempty"`
	ExpiresAtUnixMilli        int64  `json:"expires_at_unix_ms,omitempty"`
	RefreshExpiresAtUnixMilli int64  `json:"refresh_expires_at_unix_ms,omitempty"`
}

type codeFreeCredentials struct {
	UserID           string
	APIKey           string
	AccessToken      string
	RefreshToken     string
	BaseURL          string
	TokenType        string
	ExpiresAt        time.Time
	RefreshExpiresAt time.Time
	CredentialPath   string
}

type codeFreeStoredCredentials struct {
	Cached  codeFreeCachedCredentials
	Path    string
	ModTime time.Time
}

var codeFreeCredentialMu sync.Mutex

func newCodeFree(cfg Config) model.LLM {
	options := defaultOpenAICompatOptions()
	options.ApplyReasoning = nil
	return &codeFreeLLM{
		name:                strings.TrimSpace(cfg.Model),
		provider:            cfg.Provider,
		baseURL:             strings.TrimSpace(cfg.BaseURL),
		client:              coalesceCodeFreeChatHTTPClient(cfg.HTTPClient),
		requestTimeout:      cfg.Timeout,
		maxOutputTok:        cfg.MaxOutputTok,
		contextWindowTokens: cfg.ContextWindowTokens,
		options:             options,
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
			Model:     l.name,
			Messages:  l.fromKernelMessages(req.Instructions, req.Messages),
			Tools:     fromKernelTools(model.FunctionToolDefinitions(req.Tools)),
			Stream:    req.Stream,
			MaxTokens: l.maxOutputTok,
		}
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

		runCtx := ctx
		cancel := func() {}
		if !req.Stream && l.requestTimeout > 0 {
			runCtx, cancel = context.WithTimeout(ctx, l.requestTimeout)
		}
		defer cancel()

		httpReq, err := http.NewRequestWithContext(runCtx, http.MethodPost, codeFreeChatEndpoint(l.baseURL), bytes.NewReader(raw))
		if err != nil {
			yield(nil, err)
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "application/json")
		applyCodeFreeHeaders(httpReq, creds, l.name)

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

		bodyReader := bufio.NewReader(resp.Body)
		if !req.Stream {
			var out openAICompatResponse
			if err := json.NewDecoder(bodyReader).Decode(&out); err != nil {
				yield(nil, err)
				return
			}
			if len(out.Choices) == 0 {
				yield(nil, fmt.Errorf("model: empty choices"))
				return
			}
			msg, err := toKernelMessage(out.Choices[0].Message)
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
					FinishReason: normalizeOpenAICompatFinishReason(out.Choices[0].FinishReason),
					Model:        out.Model,
					Provider:     l.provider,
					Usage: model.Usage{
						PromptTokens:     out.Usage.PromptTokens,
						CompletionTokens: out.Usage.CompletionTokens,
						TotalTokens:      out.Usage.TotalTokens,
					},
				},
			}, nil)
			return
		}

		if !codeFreeResponseLooksLikeSSE(resp, bodyReader) {
			if err := emitCodeFreeJSONTurnDone(bodyReader, l.provider, l.name, yield); err != nil {
				yield(nil, err)
			}
			return
		}

		acc := openAIStreamAccumulator{
			role:      model.RoleAssistant,
			toolCalls: map[int]*openAICompatToolCall{},
		}
		var usage model.Usage
		finishReason := model.FinishReasonUnknown
		stopped := false
		if err := readSSE(bodyReader, func(data []byte) error {
			var chunk openAICompatStreamChunk
			if err := json.Unmarshal(data, &chunk); err != nil {
				return err
			}
			if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 || chunk.Usage.TotalTokens > 0 {
				usage = model.Usage{
					PromptTokens:     chunk.Usage.PromptTokens,
					CompletionTokens: chunk.Usage.CompletionTokens,
					TotalTokens:      chunk.Usage.TotalTokens,
				}
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
			yield(nil, err)
			return
		}
		if stopped {
			return
		}
		finalMsg, err := acc.message()
		if err != nil {
			yield(nil, err)
			return
		}
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
	}
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

func emitCodeFreeJSONTurnDone(reader *bufio.Reader, provider string, fallbackModel string, yield func(*model.StreamEvent, error) bool) error {
	if reader == nil {
		return fmt.Errorf("model: empty codefree response body")
	}
	var out openAICompatResponse
	if err := json.NewDecoder(reader).Decode(&out); err != nil {
		return err
	}
	if len(out.Choices) == 0 {
		return fmt.Errorf("model: empty choices")
	}
	msg, err := toKernelMessage(out.Choices[0].Message)
	if err != nil {
		return err
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
			Usage: model.Usage{
				PromptTokens:     out.Usage.PromptTokens,
				CompletionTokens: out.Usage.CompletionTokens,
				TotalTokens:      out.Usage.TotalTokens,
			},
		},
	}, nil)
	return nil
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

func loadCodeFreeCredentials(ctx context.Context) (codeFreeCredentials, error) {
	codeFreeCredentialMu.Lock()
	defer codeFreeCredentialMu.Unlock()

	stored, err := readCodeFreeStoredCredentials()
	if err != nil {
		return codeFreeCredentials{}, err
	}
	if needsCodeFreeRefresh(stored.Cached, stored.ModTime) && strings.TrimSpace(stored.Cached.RefreshToken) != "" {
		refreshed, err := refreshCodeFreeStoredCredentials(ctx, stored)
		if err == nil {
			stored = refreshed
		} else if !canUseCodeFreeStoredCredentials(stored.Cached) {
			return codeFreeCredentials{}, err
		}
	}
	return finalizeCodeFreeCredentials(stored)
}

func decryptCodeFreeAPIKey(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("providers: codefree credentials missing encrypted api key")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return "", fmt.Errorf("providers: decode codefree encrypted api key: %w", err)
	}
	block, err := aes.NewCipher([]byte(codeFreeAPIKeyDecryptKey))
	if err != nil {
		return "", fmt.Errorf("providers: init codefree api key cipher: %w", err)
	}
	if len(ciphertext) == 0 || len(ciphertext)%block.BlockSize() != 0 {
		return "", fmt.Errorf("providers: invalid codefree encrypted api key length")
	}
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, []byte(codeFreeAPIKeyDecryptIV)).CryptBlocks(plain, ciphertext)
	plain, err = trimPKCS7Padding(plain, block.BlockSize())
	if err != nil {
		return "", fmt.Errorf("providers: unpad codefree api key: %w", err)
	}
	apiKey := strings.TrimSpace(string(plain))
	if apiKey == "" {
		return "", fmt.Errorf("providers: decrypted codefree api key is empty")
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

func resolveCodeFreeCredentialPath() (string, error) {
	path := strings.TrimSpace(os.Getenv(codeFreeCredsPathEnv))
	if path != "" {
		return path, nil
	}
	primary, _, err := resolveCodeFreeDefaultCredentialPaths()
	if err != nil {
		return "", err
	}
	return primary, nil
}

func resolveCodeFreeDefaultCredentialPaths() (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("providers: resolve codefree home dir: %w", err)
	}
	primary := filepath.Join(home, ".caelis", filepath.FromSlash(codeFreeCredentialDir), codeFreeDefaultCredentialFile)
	legacy := filepath.Join(home, codeFreeLegacyCredentialDir, codeFreeDefaultCredentialFile)
	return primary, legacy, nil
}

func readCodeFreeStoredCredentials() (codeFreeStoredCredentials, error) {
	path, err := resolveCodeFreeCredentialPath()
	if err != nil {
		return codeFreeStoredCredentials{}, err
	}
	stored, err := readCodeFreeStoredCredentialsAtPath(path)
	if err == nil {
		return stored, nil
	}
	if strings.TrimSpace(os.Getenv(codeFreeCredsPathEnv)) != "" || !errors.Is(err, os.ErrNotExist) {
		return codeFreeStoredCredentials{}, err
	}
	primary, legacy, resolveErr := resolveCodeFreeDefaultCredentialPaths()
	if resolveErr != nil {
		return codeFreeStoredCredentials{}, resolveErr
	}
	if filepath.Clean(path) != filepath.Clean(primary) {
		return codeFreeStoredCredentials{}, err
	}
	imported, importErr := importLegacyCodeFreeStoredCredentials(primary, legacy)
	if importErr == nil {
		return imported, nil
	}
	return codeFreeStoredCredentials{}, err
}

func importLegacyCodeFreeStoredCredentials(primary string, legacy string) (codeFreeStoredCredentials, error) {
	if filepath.Clean(primary) == filepath.Clean(legacy) {
		return codeFreeStoredCredentials{}, fmt.Errorf("providers: codefree credential import source and destination are identical")
	}
	raw, err := os.ReadFile(legacy)
	if err != nil {
		return codeFreeStoredCredentials{}, fmt.Errorf("providers: read legacy codefree credentials %q: %w", legacy, err)
	}
	info, err := os.Stat(legacy)
	if err != nil {
		return codeFreeStoredCredentials{}, fmt.Errorf("providers: stat legacy codefree credentials %q: %w", legacy, err)
	}
	if err := os.MkdirAll(filepath.Dir(primary), 0o755); err != nil {
		return codeFreeStoredCredentials{}, fmt.Errorf("providers: create caelis codefree credential dir: %w", err)
	}
	if err := os.WriteFile(primary, raw, 0o600); err != nil {
		return codeFreeStoredCredentials{}, fmt.Errorf("providers: import codefree credentials into %q: %w", primary, err)
	}
	if err := os.Chtimes(primary, info.ModTime(), info.ModTime()); err != nil {
		return codeFreeStoredCredentials{}, fmt.Errorf("providers: preserve imported codefree credential mtime for %q: %w", primary, err)
	}
	return readCodeFreeStoredCredentialsAtPath(primary)
}

func canUseCodeFreeStoredCredentials(cached codeFreeCachedCredentials) bool {
	return strings.TrimSpace(cached.UserID) != "" && strings.TrimSpace(cached.APIKey) != ""
}

func finalizeCodeFreeCredentials(stored codeFreeStoredCredentials) (codeFreeCredentials, error) {
	userID := strings.TrimSpace(stored.Cached.UserID)
	if userID == "" {
		return codeFreeCredentials{}, fmt.Errorf("providers: codefree credentials missing id_token/userId")
	}
	apiKey, err := decryptCodeFreeAPIKey(stored.Cached.APIKey)
	if err != nil {
		return codeFreeCredentials{}, err
	}
	return codeFreeCredentials{
		UserID:           userID,
		APIKey:           apiKey,
		AccessToken:      strings.TrimSpace(stored.Cached.AccessToken),
		RefreshToken:     strings.TrimSpace(stored.Cached.RefreshToken),
		BaseURL:          codeFreeFirstNonEmpty(strings.TrimSpace(stored.Cached.BaseURL), codeFreeDefaultBaseURL),
		TokenType:        strings.TrimSpace(stored.Cached.TokenType),
		ExpiresAt:        codeFreeExpiresAt(stored.Cached, stored.ModTime),
		RefreshExpiresAt: codeFreeRefreshExpiresAt(stored.Cached, stored.ModTime),
		CredentialPath:   stored.Path,
	}, nil
}

func codeFreeFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func codeFreeExpiresAt(cached codeFreeCachedCredentials, modTime time.Time) time.Time {
	if cached.ExpiresAtUnixMilli > 0 {
		return time.UnixMilli(cached.ExpiresAtUnixMilli)
	}
	if cached.ObtainedAtUnixMilli > 0 && cached.ExpiresIn > 0 {
		return time.UnixMilli(cached.ObtainedAtUnixMilli).Add(time.Duration(cached.ExpiresIn) * time.Second)
	}
	if !modTime.IsZero() && cached.ExpiresIn > 0 {
		return modTime.Add(time.Duration(cached.ExpiresIn) * time.Second)
	}
	return time.Time{}
}

func codeFreeRefreshExpiresAt(cached codeFreeCachedCredentials, modTime time.Time) time.Time {
	if cached.RefreshExpiresAtUnixMilli > 0 {
		return time.UnixMilli(cached.RefreshExpiresAtUnixMilli)
	}
	if cached.ObtainedAtUnixMilli > 0 && cached.RefreshTokenExpiresIn > 0 {
		return time.UnixMilli(cached.ObtainedAtUnixMilli).Add(time.Duration(cached.RefreshTokenExpiresIn) * time.Second)
	}
	if !modTime.IsZero() && cached.RefreshTokenExpiresIn > 0 {
		return modTime.Add(time.Duration(cached.RefreshTokenExpiresIn) * time.Second)
	}
	return time.Time{}
}

func needsCodeFreeRefresh(cached codeFreeCachedCredentials, modTime time.Time) bool {
	if strings.TrimSpace(cached.RefreshToken) == "" {
		return false
	}
	if strings.TrimSpace(cached.UserID) == "" || strings.TrimSpace(cached.APIKey) == "" {
		return true
	}
	expiresAt := codeFreeExpiresAt(cached, modTime)
	return !expiresAt.IsZero() && !time.Now().Before(expiresAt)
}
