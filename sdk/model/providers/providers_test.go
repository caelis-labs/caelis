package providers

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/sdk/model"
	"google.golang.org/genai"
)

func jsonArgs(v map[string]any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func TestListModelsRequiresRegistration(t *testing.T) {
	factory := NewFactory()
	if got := factory.ListModels(); len(got) != 0 {
		t.Fatalf("expected empty model list, got %v", got)
	}
	if _, err := factory.NewByAlias("deepseek/deepseek-v4-flash"); err == nil {
		t.Fatalf("expected unknown alias error without registration")
	}

	cfg := Config{
		Alias:               "deepseek/deepseek-v4-flash",
		Provider:            "deepseek",
		API:                 APIDeepSeek,
		Model:               "deepseek-v4-flash",
		BaseURL:             "https://api.deepseek.com/v1",
		ContextWindowTokens: 64000,
		Auth: AuthConfig{
			Type:  AuthAPIKey,
			Token: "secret",
		},
	}
	if err := factory.Register(cfg); err != nil {
		t.Fatalf("register provider config: %v", err)
	}
	list := factory.ListModels()
	if len(list) != 1 || list[0] != cfg.Alias {
		t.Fatalf("unexpected list models: %v", list)
	}
}

func TestFactoryRequiresTokenFromConfig(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-token-should-be-ignored")

	factory := NewFactory()
	cfg := Config{
		Alias:    "openai/gpt-4o-mini",
		Provider: "openai",
		API:      APIOpenAI,
		Model:    "gpt-4o-mini",
		BaseURL:  "https://api.openai.com/v1",
		Auth: AuthConfig{
			Type:     AuthAPIKey,
			TokenEnv: "OPENAI_API_KEY",
		},
	}
	if err := factory.Register(cfg); err != nil {
		t.Fatalf("register provider config: %v", err)
	}
	_, err := factory.NewByAlias(cfg.Alias)
	if err == nil {
		t.Fatalf("expected missing token error")
	}
	if !strings.Contains(err.Error(), "auth token is empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func encryptCodeFreeAPIKeyForTest(t *testing.T, apiKey string) string {
	t.Helper()
	block, err := aes.NewCipher([]byte(codeFreeAPIKeyDecryptKey))
	if err != nil {
		t.Fatalf("init aes cipher: %v", err)
	}
	blockSize := block.BlockSize()
	pad := blockSize - (len(apiKey) % blockSize)
	plain := append([]byte(apiKey), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, []byte(codeFreeAPIKeyDecryptIV)).CryptBlocks(out, plain)
	return base64.StdEncoding.EncodeToString(out)
}

func writeCodeFreeCredsForTest(t *testing.T, userID string, apiKey string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "oauth_creds.json")
	writeCodeFreeCredsAtPathForTest(t, path, userID, apiKey)
	return path
}

func writeCodeFreeCredsAtPathForTest(t *testing.T, path string, userID string, apiKey string) {
	t.Helper()
	payload := map[string]string{
		"id_token": userID,
		"apikey":   encryptCodeFreeAPIKeyForTest(t, apiKey),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal creds: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir creds dir: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
}

func writeCodeFreeRefreshableCredsForTest(t *testing.T, baseURL string, userID string, apiKey string, refreshToken string, expiresAt time.Time) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "oauth_creds.json")
	now := time.Now()
	payload := map[string]any{
		"access_token":               "stale-access-token",
		"refresh_token":              refreshToken,
		"id_token":                   userID,
		"apikey":                     encryptCodeFreeAPIKeyForTest(t, apiKey),
		"baseUrl":                    strings.TrimSpace(baseURL),
		"token_type":                 "bearer",
		"expires_in":                 int64(time.Until(expiresAt).Seconds()),
		"refresh_token_expires_in":   int64((24 * time.Hour).Seconds()),
		"obtained_at_unix_ms":        now.Add(-time.Hour).UnixMilli(),
		"expires_at_unix_ms":         expiresAt.UnixMilli(),
		"refresh_expires_at_unix_ms": now.Add(24 * time.Hour).UnixMilli(),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal refreshable creds: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write refreshable creds: %v", err)
	}
	return path
}

type codeFreeLoginFlowStub struct {
	state         string
	codeVerifier  string
	codeChallenge string
	callback      codeFreeOAuthCallback
}

func (f *codeFreeLoginFlowStub) State() string         { return f.state }
func (f *codeFreeLoginFlowStub) CodeChallenge() string { return f.codeChallenge }
func (f *codeFreeLoginFlowStub) CodeVerifier() string  { return f.codeVerifier }
func (f *codeFreeLoginFlowStub) Close() error          { return nil }

func (f *codeFreeLoginFlowStub) Wait(context.Context) (codeFreeOAuthCallback, error) {
	return f.callback, nil
}

func withCodeFreeLoginFlowForTest(t *testing.T, callback func(state string) codeFreeOAuthCallback) {
	t.Helper()
	state := base64.StdEncoding.EncodeToString([]byte("http://127.0.0.1/callback"))
	old := newCodeFreeLoginFlowSession
	newCodeFreeLoginFlowSession = func(string, int) (codeFreeLoginFlowSession, error) {
		return &codeFreeLoginFlowStub{
			state:         state,
			codeVerifier:  "verifier",
			codeChallenge: "challenge",
			callback:      callback(state),
		}, nil
	}
	t.Cleanup(func() { newCodeFreeLoginFlowSession = old })
}

func withCodeFreeControlHTTPClientForTest(t *testing.T, client *http.Client) {
	t.Helper()
	old := newCodeFreeControlHTTPClientFunc
	newCodeFreeControlHTTPClientFunc = func() *http.Client { return client }
	t.Cleanup(func() { newCodeFreeControlHTTPClientFunc = old })
}

func TestCodeFreeNonStream_UsesLocalOAuthCredsAndEndpoint(t *testing.T) {
	credsPath := writeCodeFreeCredsForTest(t, "272182", "76475baf-3659-488a-932d-0971ae103591")
	t.Setenv(codeFreeCredsPathEnv, credsPath)
	t.Setenv(codeFreeClientVersionEnv, "0.3.6")

	var seenHeaders http.Header
	var seenPayload map[string]any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != codeFreeChatCompletionsPath {
			http.NotFound(w, r)
			return
		}
		seenHeaders = r.Header.Clone()
		rawBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(rawBody, &seenPayload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp","object":"chat.completion","created":1,"model":"GLM-4.7","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
	}))
	defer server.Close()

	llm := newCodeFree(Config{
		Provider:   "codefree",
		Model:      "GLM-4.7",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	})

	var final *model.Response
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "Reply with exactly pong.")},
		Stream:   false,
	}) {
		if err != nil {
			t.Fatalf("generate error: %v", err)
		}
		if resp != nil && resp.Response != nil {
			final = resp.Response
		}
	}
	if final == nil {
		t.Fatal("expected final response")
	}
	if got := final.Message.TextContent(); got != "pong" {
		t.Fatalf("final text = %q, want %q", got, "pong")
	}
	if got := seenHeaders.Get("Authorization"); got != codeFreeAuthorizationValue {
		t.Fatalf("authorization = %q, want %q", got, codeFreeAuthorizationValue)
	}
	if got := seenHeaders.Get("Accept"); got != "application/json" {
		t.Fatalf("accept = %q, want application/json", got)
	}
	if got := seenHeaders.Get("Userid"); got != "272182" {
		t.Fatalf("userid = %q, want %q", got, "272182")
	}
	if got := seenHeaders.Get("Apikey"); got != "76475baf-3659-488a-932d-0971ae103591" {
		t.Fatalf("apikey = %q", got)
	}
	if got := seenHeaders.Get("Modelname"); got != "GLM-4.7" {
		t.Fatalf("modelname = %q, want GLM-4.7", got)
	}
	if got := seenHeaders.Get("Clientversion"); got != "0.3.6" {
		t.Fatalf("clientversion = %q, want 0.3.6", got)
	}
	if strings.TrimSpace(seenHeaders.Get("Sessionid")) == "" {
		t.Fatal("expected sessionid header")
	}
	if got := seenPayload["temperature"]; got != float64(0) {
		t.Fatalf("temperature = %#v, want 0", got)
	}
	if got := seenPayload["top_p"]; got != float64(1) {
		t.Fatalf("top_p = %#v, want 1", got)
	}
	if _, ok := seenPayload["stream_options"]; ok {
		t.Fatalf("non-stream payload unexpectedly included stream_options: %#v", seenPayload["stream_options"])
	}
}

func TestResolveCodeFreeCredentialPath_DefaultsToCaelisStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(codeFreeCredsPathEnv, "")

	got, err := resolveCodeFreeCredentialPath()
	if err != nil {
		t.Fatalf("resolveCodeFreeCredentialPath() error = %v", err)
	}
	want := filepath.Join(home, ".caelis", filepath.FromSlash(codeFreeCredentialDir), codeFreeDefaultCredentialFile)
	if got != want {
		t.Fatalf("credential path = %q, want %q", got, want)
	}
}

func TestReadCodeFreeStoredCredentials_ImportsLegacyCodeFreeCredsIntoCaelisStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(codeFreeCredsPathEnv, "")

	primary, legacy, err := resolveCodeFreeDefaultCredentialPaths()
	if err != nil {
		t.Fatalf("resolveCodeFreeDefaultCredentialPaths() error = %v", err)
	}
	writeCodeFreeCredsAtPathForTest(t, legacy, "272182", "legacy-api-key")

	stored, err := readCodeFreeStoredCredentials()
	if err != nil {
		t.Fatalf("readCodeFreeStoredCredentials() error = %v", err)
	}
	if stored.Path != primary {
		t.Fatalf("stored path = %q, want %q", stored.Path, primary)
	}
	creds, err := finalizeCodeFreeCredentials(stored)
	if err != nil {
		t.Fatalf("finalizeCodeFreeCredentials() error = %v", err)
	}
	if creds.APIKey != "legacy-api-key" {
		t.Fatalf("apikey = %q, want legacy-api-key", creds.APIKey)
	}
	if _, err := os.Stat(primary); err != nil {
		t.Fatalf("expected imported credentials at %q: %v", primary, err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("expected legacy credentials to remain at %q: %v", legacy, err)
	}
}

func TestCodeFreeStream_ParsesSSE(t *testing.T) {
	credsPath := writeCodeFreeCredsForTest(t, "272182", "76475baf-3659-488a-932d-0971ae103591")
	t.Setenv(codeFreeCredsPathEnv, credsPath)

	var (
		seenHeaders http.Header
		seenPayload map[string]any
	)
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != codeFreeChatCompletionsPath {
			http.NotFound(w, r)
			return
		}
		seenHeaders = r.Header.Clone()
		rawBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(rawBody, &seenPayload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chunk-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"GLM-4.7\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pong\",\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chunk-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"GLM-4.7\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chunk-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"GLM-4.7\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	llm := newCodeFree(Config{
		Provider:   "codefree",
		Model:      "GLM-4.7",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	})

	var (
		gotErr error
		final  *model.Response
	)
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "Reply with exactly pong.")},
		Stream:   true,
	}) {
		if err != nil {
			gotErr = err
			continue
		}
		if resp != nil && resp.Response != nil && resp.TurnComplete {
			final = resp.Response
		}
	}
	if gotErr != nil {
		t.Fatalf("stream generate error: %v", gotErr)
	}
	if final == nil {
		t.Fatal("expected final streamed response")
	}
	if got := final.Message.TextContent(); got != "pong" {
		t.Fatalf("final streamed text = %q, want %q", got, "pong")
	}
	if final.Usage.PromptTokens != 10 || final.Usage.CompletionTokens != 2 || final.Usage.TotalTokens != 12 {
		t.Fatalf("unexpected usage: %+v", final.Usage)
	}
	if final.FinishReason != model.FinishReasonStop {
		t.Fatalf("finish reason = %q, want %q", final.FinishReason, model.FinishReasonStop)
	}
	if got := seenHeaders.Get("Accept"); got != codeFreeStreamAcceptValue {
		t.Fatalf("accept = %q, want %q", got, codeFreeStreamAcceptValue)
	}
	if got := seenPayload["temperature"]; got != float64(0) {
		t.Fatalf("temperature = %#v, want 0", got)
	}
	if got := seenPayload["top_p"]; got != float64(1) {
		t.Fatalf("top_p = %#v, want 1", got)
	}
	streamOptions, _ := seenPayload["stream_options"].(map[string]any)
	if streamOptions["include_usage"] != true {
		t.Fatalf("stream_options = %#v, want include_usage=true", seenPayload["stream_options"])
	}
}

func TestCodeFreeStream_FallsBackToJSONWhenBackendDoesNotUseSSE(t *testing.T) {
	credsPath := writeCodeFreeCredsForTest(t, "272182", "76475baf-3659-488a-932d-0971ae103591")
	t.Setenv(codeFreeCredsPathEnv, credsPath)

	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != codeFreeChatCompletionsPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp","object":"chat.completion","created":1,"model":"GLM-5.1","choices":[{"index":0,"message":{"role":"assistant","content":"我可以读取文件、运行命令、修改代码并验证结果。"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":14,"total_tokens":34}}`)
	}))
	defer server.Close()

	llm := newCodeFree(Config{
		Provider:   "codefree",
		Model:      "GLM-5.1",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	})

	var (
		gotErr error
		final  *model.Response
	)
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{
			model.NewTextMessage(model.RoleUser, "演示一下你的工具调用能力"),
		},
		Stream: true,
	}) {
		if err != nil {
			gotErr = err
			continue
		}
		if resp != nil && resp.Response != nil && resp.TurnComplete {
			final = resp.Response
		}
	}
	if gotErr != nil {
		t.Fatalf("stream generate error = %v", gotErr)
	}
	if final == nil {
		t.Fatal("expected final response")
	}
	if got := strings.TrimSpace(final.Message.TextContent()); got == "" {
		t.Fatal("expected non-empty assistant text")
	}
	if got := final.Message.TextContent(); got != "我可以读取文件、运行命令、修改代码并验证结果。" {
		t.Fatalf("final text = %q", got)
	}
	if final.Usage.PromptTokens != 20 || final.Usage.CompletionTokens != 14 || final.Usage.TotalTokens != 34 {
		t.Fatalf("unexpected usage: %+v", final.Usage)
	}
	if final.FinishReason != model.FinishReasonStop {
		t.Fatalf("finish reason = %q, want %q", final.FinishReason, model.FinishReasonStop)
	}
}

func TestCodeFreeStream_RetriesRetCode51ControlPacket(t *testing.T) {
	credsPath := writeCodeFreeCredsForTest(t, "272182", "76475baf-3659-488a-932d-0971ae103591")
	t.Setenv(codeFreeCredsPathEnv, credsPath)

	requests := 0
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != codeFreeChatCompletionsPath {
			http.NotFound(w, r)
			return
		}
		requests++
		if requests == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, `{"retCode":51}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chunk-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"GLM-5.1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\",\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chunk-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"GLM-5.1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	llm := newCodeFreeRetryTestModel(t, server, 2)

	var (
		gotErr error
		final  *model.Response
	)
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
		Stream:   true,
	}) {
		if err != nil {
			gotErr = err
			continue
		}
		if resp != nil && resp.Response != nil && resp.TurnComplete {
			final = resp.Response
		}
	}
	if gotErr != nil {
		t.Fatalf("stream generate error: %v", gotErr)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if final == nil {
		t.Fatal("expected final response after retry")
	}
	if got := final.Message.TextContent(); got != "ok" {
		t.Fatalf("final text = %q, want ok", got)
	}
}

func TestCodeFreeRetCode51ExhaustionIsBackpressureError(t *testing.T) {
	credsPath := writeCodeFreeCredsForTest(t, "272182", "76475baf-3659-488a-932d-0971ae103591")
	t.Setenv(codeFreeCredsPathEnv, credsPath)

	const retryMax = 2
	requests := 0
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != codeFreeChatCompletionsPath {
			http.NotFound(w, r)
			return
		}
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, `{"retCode":51}`)
	}))
	defer server.Close()

	llm := newCodeFreeRetryTestModel(t, server, retryMax)

	var gotErr error
	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
		Stream:   true,
	}) {
		if err != nil {
			gotErr = err
		}
	}
	if gotErr == nil {
		t.Fatal("expected retCode 51 error")
	}
	if requests != retryMax+1 {
		t.Fatalf("requests = %d, want %d", requests, retryMax+1)
	}
	text := gotErr.Error()
	for _, want := range []string{"model: llm request hit provider backpressure", "model: codefree server overloaded", "retCode=51", `body={"retCode":51}`} {
		if !strings.Contains(text, want) {
			t.Fatalf("error = %q, want %q", text, want)
		}
	}
	if strings.Contains(text, "empty choices") {
		t.Fatalf("error = %q, did not want empty choices", text)
	}
}

func newCodeFreeRetryTestModel(t *testing.T, server *providerTestServer, retryMax int) model.LLM {
	t.Helper()
	factory := NewFactory()
	cfg := Config{
		Alias:      "codefree/glm-5.1",
		Provider:   "codefree",
		API:        APICodeFree,
		Model:      "GLM-5.1",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
		Retry: model.RetryConfig{
			MaxRetries:          retryMax,
			BaseDelay:           time.Nanosecond,
			MaxDelay:            time.Nanosecond,
			RateLimitMaxRetries: retryMax,
			RateLimitBaseDelay:  time.Nanosecond,
			RateLimitMaxDelay:   time.Nanosecond,
		},
	}
	if err := factory.Register(cfg); err != nil {
		t.Fatalf("register codefree retry provider: %v", err)
	}
	llm, err := factory.NewByAlias(cfg.Alias)
	if err != nil {
		t.Fatalf("NewByAlias() error = %v", err)
	}
	return llm
}

func TestCodeFreeEmptyChoicesIncludesRedactedResponseBody(t *testing.T) {
	credsPath := writeCodeFreeCredsForTest(t, "272182", "76475baf-3659-488a-932d-0971ae103591")
	t.Setenv(codeFreeCredsPathEnv, credsPath)

	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != codeFreeChatCompletionsPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[],"error":{"message":"provider returned no candidate"},"apikey":"secret-api-key","userid":"272182","user_id":272182}`)
	}))
	defer server.Close()

	llm := newCodeFree(Config{
		Provider:   "codefree",
		Model:      "GLM-5.1",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	})

	var gotErr error
	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
		Stream:   false,
	}) {
		if err != nil {
			gotErr = err
		}
	}
	if gotErr == nil {
		t.Fatal("expected empty choices error")
	}
	text := gotErr.Error()
	if !strings.Contains(text, "model: empty choices") || !strings.Contains(text, "provider returned no candidate") {
		t.Fatalf("error = %q, want empty choices with response summary", text)
	}
	if strings.Contains(text, "secret-api-key") || strings.Contains(text, "272182") {
		t.Fatalf("error leaked sensitive response fields: %q", text)
	}
	if !strings.Contains(text, "[redacted len=") {
		t.Fatalf("error = %q, want redacted sensitive fields", text)
	}
}

func TestCodeFreeLogin_PersistsRefreshableOAuthCredentials(t *testing.T) {
	oldOpenBrowser := codeFreeOpenBrowser
	defer func() { codeFreeOpenBrowser = oldOpenBrowser }()

	credsPath := filepath.Join(t.TempDir(), "oauth_creds.json")
	var tokenRequests int
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case codeFreeOAuthTokenPath:
			tokenRequests++
			if got := r.Method; got != http.MethodGet {
				t.Fatalf("method = %q, want GET", got)
			}
			values := r.URL.Query()
			if got := values.Get("grant_type"); got != "authorization_code" {
				t.Fatalf("grant_type = %q, want authorization_code", got)
			}
			if strings.TrimSpace(values.Get("code_verifier")) == "" {
				t.Fatal("expected code_verifier in token exchange")
			}
			if got := values.Get("client_id"); got != codeFreeDefaultOAuthClientID {
				t.Fatalf("client_id = %q, want %q", got, codeFreeDefaultOAuthClientID)
			}
			if got := values.Get("client_secret"); got != "" {
				t.Fatalf("client_secret = %q, want empty by default", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"access_token":"login-access","refresh_token":"login-refresh","token_type":"bearer","expires_in":3600,"refresh_token_expires_in":7200,"id_token":"272182"}`)
		case codeFreeUserAPIKeyPath:
			if got := r.Header.Get("sessionId"); got != "login-access" {
				t.Fatalf("sessionId = %q, want login-access", got)
			}
			if got := r.Header.Get("userId"); got != "272182" {
				t.Fatalf("userId = %q, want 272182", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"encryptedApiKey":%q,"optResult":0}`, encryptCodeFreeAPIKeyForTest(t, "live-api-key"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	withCodeFreeLoginFlowForTest(t, func(state string) codeFreeOAuthCallback {
		return codeFreeOAuthCallback{Code: "auth-code", State: state}
	})

	codeFreeOpenBrowser = func(authURL string) error {
		parsed, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		if got := parsed.Path; got != codeFreeOAuthAuthorizePath {
			t.Fatalf("authorize path = %q, want %q", got, codeFreeOAuthAuthorizePath)
		}
		query := parsed.Query()
		redirectURL := query.Get("redirect_uri")
		if redirectURL == "" {
			t.Fatal("expected redirect_uri in auth url")
		}
		if got := redirectURL; got != server.URL+codeFreeOAuthRedirectPath {
			t.Fatalf("redirect_uri = %q, want %q", got, server.URL+codeFreeOAuthRedirectPath)
		}
		state := query.Get("state")
		if strings.TrimSpace(state) == "" {
			t.Fatal("expected oauth state in auth url")
		}
		localURLBytes, err := base64.StdEncoding.DecodeString(state)
		if err != nil {
			return err
		}
		callbackURL, err := url.Parse(string(localURLBytes))
		if err != nil {
			return err
		}
		if callbackURL.Scheme != "http" || callbackURL.Host == "" || callbackURL.Path == "" {
			t.Fatalf("decoded local callback url = %q, want http://host/path", callbackURL.String())
		}
		return nil
	}

	result, err := CodeFreeLogin(context.Background(), CodeFreeLoginOptions{
		BaseURL:        server.URL,
		HTTPClient:     server.Client(),
		CredentialPath: credsPath,
		OpenBrowser:    true,
	})
	if err != nil {
		t.Fatalf("CodeFreeLogin() error = %v", err)
	}
	if !result.HasRefreshToken {
		t.Fatal("expected login result to include refresh token")
	}
	if result.UserID != "272182" {
		t.Fatalf("user id = %q, want 272182", result.UserID)
	}
	if tokenRequests != 1 {
		t.Fatalf("token request count = %d, want 1", tokenRequests)
	}

	stored, err := readCodeFreeStoredCredentialsAtPath(credsPath)
	if err != nil {
		t.Fatalf("readCodeFreeStoredCredentialsAtPath() error = %v", err)
	}
	if got := stored.Cached.RefreshToken; got != "login-refresh" {
		t.Fatalf("stored refresh_token = %q, want login-refresh", got)
	}
	if got := stored.Cached.AccessToken; got != "login-access" {
		t.Fatalf("stored access_token = %q, want login-access", got)
	}
}

func TestResolveCodeFreeOAuthConfig_DefaultsAuthCodeExchangeToNoneWithoutClientSecret(t *testing.T) {
	cfg, err := resolveCodeFreeOAuthConfig("https://www.srdcloud.cn", nil, filepath.Join(t.TempDir(), "oauth_creds.json"), "", "", "")
	if err != nil {
		t.Fatalf("resolveCodeFreeOAuthConfig() error = %v", err)
	}
	if cfg.ClientAuthMethod != CodeFreeClientAuthNone {
		t.Fatalf("client auth method = %q, want %q", cfg.ClientAuthMethod, CodeFreeClientAuthNone)
	}
}

func TestCodeFreeLogin_AcceptsLocalCallbackWithoutState(t *testing.T) {
	oldOpenBrowser := codeFreeOpenBrowser
	defer func() { codeFreeOpenBrowser = oldOpenBrowser }()

	credsPath := filepath.Join(t.TempDir(), "oauth_creds.json")
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case codeFreeOAuthTokenPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"access_token":"login-access","refresh_token":"login-refresh","token_type":"bearer","expires_in":3600,"refresh_token_expires_in":7200,"id_token":"272182"}`)
		case codeFreeUserAPIKeyPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"encryptedApiKey":%q,"optResult":0}`, encryptCodeFreeAPIKeyForTest(t, "live-api-key"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	withCodeFreeLoginFlowForTest(t, func(string) codeFreeOAuthCallback {
		return codeFreeOAuthCallback{Code: "auth-code"}
	})

	codeFreeOpenBrowser = func(authURL string) error {
		parsed, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		state := parsed.Query().Get("state")
		if strings.TrimSpace(state) == "" {
			t.Fatal("expected oauth state in auth url")
		}
		localURLBytes, err := base64.StdEncoding.DecodeString(state)
		if err != nil {
			return err
		}
		callbackURL, err := url.Parse(string(localURLBytes))
		if err != nil {
			return err
		}
		_ = callbackURL
		return nil
	}

	result, err := CodeFreeLogin(context.Background(), CodeFreeLoginOptions{
		BaseURL:        server.URL,
		HTTPClient:     server.Client(),
		CredentialPath: credsPath,
		OpenBrowser:    true,
	})
	if err != nil {
		t.Fatalf("CodeFreeLogin() error = %v", err)
	}
	if !result.HasRefreshToken {
		t.Fatal("expected login result to include refresh token")
	}
	if result.UserID != "272182" {
		t.Fatalf("user id = %q, want 272182", result.UserID)
	}
}

func TestCodeFreeLogin_AcceptsFormEncodedTokenResponse(t *testing.T) {
	oldOpenBrowser := codeFreeOpenBrowser
	defer func() { codeFreeOpenBrowser = oldOpenBrowser }()

	credsPath := filepath.Join(t.TempDir(), "oauth_creds.json")
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case codeFreeOAuthTokenPath:
			w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
			_, _ = fmt.Fprint(w, "access_token=login-access&refresh_token=login-refresh&token_type=bearer&expires_in=3600&refresh_token_expires_in=7200&id_token=272182")
		case codeFreeUserAPIKeyPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"encryptedApiKey":%q,"optResult":0}`, encryptCodeFreeAPIKeyForTest(t, "live-api-key"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	withCodeFreeLoginFlowForTest(t, func(state string) codeFreeOAuthCallback {
		return codeFreeOAuthCallback{Code: "auth-code", State: state}
	})

	codeFreeOpenBrowser = func(authURL string) error {
		parsed, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		state := parsed.Query().Get("state")
		localURLBytes, err := base64.StdEncoding.DecodeString(state)
		if err != nil {
			return err
		}
		callbackURL, err := url.Parse(string(localURLBytes))
		if err != nil {
			return err
		}
		_ = callbackURL
		return nil
	}

	result, err := CodeFreeLogin(context.Background(), CodeFreeLoginOptions{
		BaseURL:        server.URL,
		HTTPClient:     server.Client(),
		CredentialPath: credsPath,
		OpenBrowser:    true,
	})
	if err != nil {
		t.Fatalf("CodeFreeLogin() error = %v", err)
	}
	if result.UserID != "272182" {
		t.Fatalf("user id = %q, want 272182", result.UserID)
	}
	if !result.HasRefreshToken {
		t.Fatal("expected login result to include refresh token")
	}
}

func TestCodeFreeLogin_UsesOriSessionIDForAPIKeyAndStoredAccessToken(t *testing.T) {
	oldOpenBrowser := codeFreeOpenBrowser
	defer func() { codeFreeOpenBrowser = oldOpenBrowser }()

	credsPath := filepath.Join(t.TempDir(), "oauth_creds.json")
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case codeFreeOAuthTokenPath:
			w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
			_, _ = fmt.Fprint(w, "access_token=oauth-access-long-token&token_type=bearer&expires_in=86400&id_token=&refresh_token=&uid=272182&ori_session_id=session-short-123&ori_token=other-short-token")
		case codeFreeUserAPIKeyPath:
			if got := r.Header.Get("sessionId"); got != "session-short-123" {
				t.Fatalf("sessionId = %q, want session-short-123", got)
			}
			if got := r.Header.Get("userId"); got != "272182" {
				t.Fatalf("userId = %q, want 272182", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"encryptedApiKey":%q,"optResult":0}`, encryptCodeFreeAPIKeyForTest(t, "live-api-key"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	withCodeFreeLoginFlowForTest(t, func(state string) codeFreeOAuthCallback {
		return codeFreeOAuthCallback{Code: "auth-code", State: state}
	})

	codeFreeOpenBrowser = func(authURL string) error {
		parsed, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		state := parsed.Query().Get("state")
		localURLBytes, err := base64.StdEncoding.DecodeString(state)
		if err != nil {
			return err
		}
		callbackURL, err := url.Parse(string(localURLBytes))
		if err != nil {
			return err
		}
		_ = callbackURL
		return nil
	}

	result, err := CodeFreeLogin(context.Background(), CodeFreeLoginOptions{
		BaseURL:        server.URL,
		HTTPClient:     server.Client(),
		CredentialPath: credsPath,
		OpenBrowser:    true,
	})
	if err != nil {
		t.Fatalf("CodeFreeLogin() error = %v", err)
	}
	if result.UserID != "272182" {
		t.Fatalf("user id = %q, want 272182", result.UserID)
	}
	if result.HasRefreshToken {
		t.Fatal("expected login result without refresh token")
	}

	stored, err := readCodeFreeStoredCredentialsAtPath(credsPath)
	if err != nil {
		t.Fatalf("readCodeFreeStoredCredentialsAtPath() error = %v", err)
	}
	if got := stored.Cached.AccessToken; got != "session-short-123" {
		t.Fatalf("stored access_token = %q, want session-short-123", got)
	}
	if got := stored.Cached.UserID; got != "272182" {
		t.Fatalf("stored user_id = %q, want 272182", got)
	}
}

func TestLoadCodeFreeCredentials_RefreshesExpiredToken(t *testing.T) {
	var tokenRequests int
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case codeFreeOAuthTokenPath:
			tokenRequests++
			body, _ := io.ReadAll(r.Body)
			values, _ := url.ParseQuery(string(body))
			if got := values.Get("grant_type"); got != "refresh_token" {
				t.Fatalf("grant_type = %q, want refresh_token", got)
			}
			if got := values.Get("refresh_token"); got != "refresh-1" {
				t.Fatalf("refresh_token = %q, want refresh-1", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"access_token":"fresh-access","refresh_token":"refresh-2","token_type":"bearer","expires_in":3600,"refresh_token_expires_in":7200,"id_token":"272182"}`)
		case codeFreeUserAPIKeyPath:
			if got := r.Header.Get("sessionId"); got != "fresh-access" {
				t.Fatalf("sessionId = %q, want fresh-access", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"encryptedApiKey":%q,"optResult":0}`, encryptCodeFreeAPIKeyForTest(t, "fresh-api-key"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	withCodeFreeControlHTTPClientForTest(t, server.Client())

	credsPath := writeCodeFreeRefreshableCredsForTest(t, server.URL, "272182", "stale-api-key", "refresh-1", time.Now().Add(-time.Minute))
	t.Setenv(codeFreeCredsPathEnv, credsPath)

	creds, err := loadCodeFreeCredentials(context.Background())
	if err != nil {
		t.Fatalf("loadCodeFreeCredentials() error = %v", err)
	}
	if tokenRequests != 1 {
		t.Fatalf("token request count = %d, want 1", tokenRequests)
	}
	if creds.APIKey != "fresh-api-key" {
		t.Fatalf("apikey = %q, want fresh-api-key", creds.APIKey)
	}
	if creds.AccessToken != "fresh-access" {
		t.Fatalf("access token = %q, want fresh-access", creds.AccessToken)
	}

	stored, err := readCodeFreeStoredCredentialsAtPath(credsPath)
	if err != nil {
		t.Fatalf("readCodeFreeStoredCredentialsAtPath() error = %v", err)
	}
	if got := stored.Cached.RefreshToken; got != "refresh-2" {
		t.Fatalf("stored refresh_token = %q, want refresh-2", got)
	}
}

func TestCodeFreeEnsureAuth_SkipsLoginWhenRefreshableCredsAlreadyExist(t *testing.T) {
	oldOpenBrowser := codeFreeOpenBrowser
	defer func() { codeFreeOpenBrowser = oldOpenBrowser }()

	credsPath := writeCodeFreeRefreshableCredsForTest(t, "https://www.srdcloud.cn", "272182", "cached-api-key", "refresh-1", time.Now().Add(time.Hour))
	var opened bool
	codeFreeOpenBrowser = func(string) error {
		opened = true
		return nil
	}

	result, err := CodeFreeEnsureAuth(context.Background(), CodeFreeEnsureAuthOptions{
		CredentialPath: credsPath,
		OpenBrowser:    true,
	})
	if err != nil {
		t.Fatalf("CodeFreeEnsureAuth() error = %v", err)
	}
	if opened {
		t.Fatal("expected existing refreshable credentials to skip browser login")
	}
	if !result.HasRefreshToken {
		t.Fatal("expected ensure auth result to report refresh token")
	}
}

func TestCodeFreeEnsureAuth_SkipsLoginWhenUsableCredsLackRefreshToken(t *testing.T) {
	oldOpenBrowser := codeFreeOpenBrowser
	defer func() { codeFreeOpenBrowser = oldOpenBrowser }()

	credsPath := writeCodeFreeCredsForTest(t, "272182", "cached-api-key")
	var opened bool
	codeFreeOpenBrowser = func(string) error {
		opened = true
		return nil
	}

	result, err := CodeFreeEnsureAuth(context.Background(), CodeFreeEnsureAuthOptions{
		CredentialPath: credsPath,
		OpenBrowser:    true,
	})
	if err != nil {
		t.Fatalf("CodeFreeEnsureAuth() error = %v", err)
	}
	if opened {
		t.Fatal("expected usable credentials without refresh token to skip browser login")
	}
	if result.HasRefreshToken {
		t.Fatal("expected ensure auth result without refresh token")
	}
}

func TestOpenAICompatStream_PropagatesSSEErrorsWithoutTurnComplete(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"model\":\"test-model\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {invalid-json}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	llm := newOpenAICompat(Config{
		Provider:   "openai-compatible",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	var (
		gotErr       error
		turnComplete bool
	)
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   true,
	}) {
		if err != nil {
			gotErr = err
			continue
		}
		if resp != nil && resp.Response != nil && resp.TurnComplete {
			turnComplete = true
		}
	}
	if gotErr == nil {
		t.Fatalf("expected stream error, got nil")
	}
	if turnComplete {
		t.Fatalf("did not expect turn_complete on stream error")
	}
}

func TestOpenAICompatStream_DoesNotApplyRequestTimeout(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: {\"model\":\"test-model\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(150 * time.Millisecond)
		_, _ = fmt.Fprint(w, "data: {\"model\":\"test-model\",\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	llm := newOpenAICompat(Config{
		Provider:   "openai-compatible",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    50 * time.Millisecond,
	}, "token")

	var (
		gotErr    error
		finalText string
	)
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   true,
	}) {
		if err != nil {
			gotErr = err
			continue
		}
		if resp != nil && resp.Response != nil && resp.TurnComplete {
			finalText = resp.Response.Message.TextContent()
		}
	}
	if gotErr != nil {
		t.Fatalf("expected no stream error, got %v", gotErr)
	}
	if finalText != "hello world" {
		t.Fatalf("unexpected final text %q", finalText)
	}
}

func TestOpenAICompatStream_IncludesUsageRequestOptionAndPropagatesUsage(t *testing.T) {
	var includeUsage bool
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		streamOptions, _ := payload["stream_options"].(map[string]any)
		includeUsage, _ = streamOptions["include_usage"].(bool)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"model\":\"test-model\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"model\":\"test-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":7,\"total_tokens\":18,\"prompt_tokens_details\":{\"cached_tokens\":9}}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	llm := newOpenAICompat(Config{
		Provider:   "openai-compatible",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	var (
		gotErr error
		usage  model.Usage
	)
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   true,
	}) {
		if err != nil {
			gotErr = err
			continue
		}
		if resp != nil && resp.Response != nil && resp.TurnComplete {
			usage = resp.Usage
		}
	}
	if gotErr != nil {
		t.Fatalf("expected no stream error, got %v", gotErr)
	}
	if !includeUsage {
		t.Fatal("expected stream_options.include_usage=true in request payload")
	}
	if usage.PromptTokens != 11 || usage.CachedInputTokens != 9 || usage.CompletionTokens != 7 || usage.TotalTokens != 18 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestOpenAICompatNonStream_IncludesStructuredOutputRequest(t *testing.T) {
	var payload map[string]any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"test-model","choices":[{"message":{"role":"assistant","content":"{\"outcome\":\"allow\"}"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	llm := newOpenAICompat(Config{
		Provider:     "openai-compatible",
		Model:        "test-model",
		BaseURL:      server.URL,
		HTTPClient:   server.Client(),
		MaxOutputTok: 2048,
		Timeout:      2 * time.Second,
	}, "token")
	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "review")},
		Output: &model.OutputSpec{
			Mode: model.OutputModeSchema,
			JSONSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"outcome": map[string]any{"type": "string"},
				},
			},
			MaxOutputTokens: 64,
		},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
	}
	if got := payload["max_tokens"]; got != float64(64) {
		t.Fatalf("max_tokens = %v, want 64", got)
	}
	responseFormat, _ := payload["response_format"].(map[string]any)
	if got := responseFormat["type"]; got != "json_schema" {
		t.Fatalf("response_format.type = %v, want json_schema", got)
	}
	jsonSchema, _ := responseFormat["json_schema"].(map[string]any)
	if _, ok := jsonSchema["strict"]; ok {
		t.Fatalf("json_schema.strict is present for optional schema: %#v", jsonSchema["strict"])
	}
	schema, _ := jsonSchema["schema"].(map[string]any)
	if got := schema["type"]; got != "object" {
		t.Fatalf("json_schema.schema.type = %v, want object", got)
	}
}

func TestOpenAICompatNonStream_UsesStrictStructuredOutputOnlyForClosedRequiredSchema(t *testing.T) {
	var payload map[string]any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"test-model","choices":[{"message":{"role":"assistant","content":"{\"outcome\":\"allow\"}"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	llm := newOpenAICompat(Config{
		Provider:   "openai-compatible",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")
	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "review")},
		Output: &model.OutputSpec{
			Mode: model.OutputModeSchema,
			JSONSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"outcome": map[string]any{"type": "string"},
				},
				"required": []any{"outcome"},
			},
		},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
	}
	responseFormat, _ := payload["response_format"].(map[string]any)
	jsonSchema, _ := responseFormat["json_schema"].(map[string]any)
	if got := jsonSchema["strict"]; got != true {
		t.Fatalf("json_schema.strict = %v, want true", got)
	}
}

func TestOpenAICompatNonStream_PropagatesFinishReason(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"test-model","choices":[{"message":{"role":"assistant","content":"truncated"},"finish_reason":"length"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)
	}))
	defer server.Close()

	llm := newOpenAICompat(Config{
		Provider:   "openai-compatible",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	var final *model.Response
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   false,
	}) {
		if err != nil {
			t.Fatalf("expected no generate error, got %v", err)
		}
		if resp != nil && resp.Response != nil {
			final = resp.Response
		}
	}
	if final == nil {
		t.Fatal("expected final response")
	}
	if !final.TurnComplete {
		t.Fatal("expected turn complete on terminal non-stream response")
	}
	if final.FinishReason != model.FinishReasonLength {
		t.Fatalf("expected finish reason length, got %q", final.FinishReason)
	}
}

func TestOpenAICompatStream_PropagatesTerminalFinishReason(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"model\":\"test-model\",\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"model\":\"test-model\",\"choices\":[{\"delta\":{\"content\":\" world\"},\"finish_reason\":\"length\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	llm := newOpenAICompat(Config{
		Provider:   "openai-compatible",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	var final *model.Response
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   true,
	}) {
		if err != nil {
			t.Fatalf("expected no stream error, got %v", err)
		}
		if resp != nil && resp.Response != nil && resp.TurnComplete {
			final = resp.Response
		}
	}
	if final == nil {
		t.Fatal("expected final response")
	}
	if final.Message.TextContent() != "hello world" {
		t.Fatalf("unexpected final text %q", final.Message.TextContent())
	}
	if final.FinishReason != model.FinishReasonLength {
		t.Fatalf("expected finish reason length, got %q", final.FinishReason)
	}
}

func TestOpenAICompatRequest_IncludesMaxTokens(t *testing.T) {
	var gotMax float64
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		got, _ := payload["max_tokens"].(float64)
		gotMax = got
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"test-model","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	llm := newOpenAICompat(Config{
		Provider:     "openai-compatible",
		Model:        "test-model",
		BaseURL:      server.URL,
		HTTPClient:   server.Client(),
		Timeout:      2 * time.Second,
		MaxOutputTok: 2048,
	}, "token")

	var gotErr error
	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   false,
	}) {
		if err != nil {
			gotErr = err
		}
	}
	if gotErr != nil {
		t.Fatalf("expected no generate error, got %v", gotErr)
	}
	if gotMax != 2048 {
		t.Fatalf("expected max_tokens=2048, got %v", gotMax)
	}
}

func TestOpenRouterRequest_AppliesConfiguredHeaders(t *testing.T) {
	var gotReferer string
	var gotTitle string
	var gotModel string
	var gotModels []any
	var gotRoute string
	var gotTransforms []any
	var gotProvider map[string]any
	var gotPlugins []any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		gotReferer = r.Header.Get("HTTP-Referer")
		gotTitle = r.Header.Get("X-Title")
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		gotModel, _ = payload["model"].(string)
		gotModels, _ = payload["models"].([]any)
		gotRoute, _ = payload["route"].(string)
		gotTransforms, _ = payload["transforms"].([]any)
		gotProvider, _ = payload["provider"].(map[string]any)
		gotPlugins, _ = payload["plugins"].([]any)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"test-model","choices":[{"message":{"role":"assistant","content":"ok","reasoning":"thinking..."}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	llm := newOpenRouter(Config{
		Provider:   "openrouter",
		API:        APIOpenRouter,
		Model:      "openrouter/healer-alpha",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Headers: map[string]string{
			"HTTP-Referer": "https://example.com/app",
			"X-Title":      "caelis",
		},
		OpenRouter: OpenRouterConfig{
			Models:     []string{"openrouter/openai/gpt-4o-mini", "openrouter/anthropic/claude-sonnet-4"},
			Route:      "fallback",
			Transforms: []string{"middle-out"},
			Provider: map[string]any{
				"allow_fallbacks": true,
			},
			Plugins: []map[string]any{
				{"id": "web"},
			},
		},
		Timeout: 2 * time.Second,
	}, "token")

	var finalReasoning string
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   false,
	}) {
		if err != nil {
			t.Fatalf("expected no generate error, got %v", err)
		}
		if resp != nil && resp.Response != nil && resp.TurnComplete {
			finalReasoning = resp.Response.Message.ReasoningText()
		}
	}
	if gotReferer != "https://example.com/app" || gotTitle != "caelis" {
		t.Fatalf("expected configured headers, got referer=%q title=%q", gotReferer, gotTitle)
	}
	if gotModel != "openrouter/healer-alpha" {
		t.Fatalf("expected native openrouter model id preserved, got %q", gotModel)
	}
	if len(gotModels) != 2 {
		t.Fatalf("expected native openrouter models list, got %#v", gotModels)
	}
	if gotModels[0] != "openai/gpt-4o-mini" || gotModels[1] != "anthropic/claude-sonnet-4" {
		t.Fatalf("expected routed model ids normalized for request payload, got %#v", gotModels)
	}
	if gotRoute != "fallback" {
		t.Fatalf("expected native openrouter route, got %q", gotRoute)
	}
	if len(gotTransforms) != 1 || gotTransforms[0] != "middle-out" {
		t.Fatalf("expected native openrouter transforms, got %#v", gotTransforms)
	}
	if value, _ := gotProvider["allow_fallbacks"].(bool); !value {
		t.Fatalf("expected native openrouter provider preferences, got %#v", gotProvider)
	}
	if len(gotPlugins) != 1 {
		t.Fatalf("expected native openrouter plugins, got %#v", gotPlugins)
	}
	if finalReasoning != "thinking..." {
		t.Fatalf("expected native openrouter reasoning field, got %q", finalReasoning)
	}
}

func TestOpenRouterRequest_DoesNotForceStrictForOptionalStructuredOutput(t *testing.T) {
	var payload map[string]any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"openrouter/test","choices":[{"message":{"role":"assistant","content":"{\"outcome\":\"allow\"}"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	llm := newOpenRouter(Config{
		Provider:   "openrouter",
		API:        APIOpenRouter,
		Model:      "openrouter/test",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")
	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "review")},
		Output: &model.OutputSpec{
			Mode: model.OutputModeSchema,
			JSONSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"outcome":   map[string]any{"type": "string"},
					"rationale": map[string]any{"type": "string"},
				},
				"required": []any{"outcome"},
			},
		},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
	}
	responseFormat, _ := payload["response_format"].(map[string]any)
	jsonSchema, _ := responseFormat["json_schema"].(map[string]any)
	if _, ok := jsonSchema["strict"]; ok {
		t.Fatalf("json_schema.strict is present for optional OpenRouter schema: %#v", jsonSchema["strict"])
	}
}

func TestOpenRouterStream_PropagatesTerminalFinishReason(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"model\":\"test-model\",\"choices\":[{\"delta\":{\"content\":\"step 1\"},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"model\":\"test-model\",\"choices\":[{\"delta\":{\"content\":\" done\"},\"finish_reason\":\"tool_calls\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	llm := newOpenRouter(Config{
		Provider:   "openrouter",
		API:        APIOpenRouter,
		Model:      "openrouter/test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	var final *model.Response
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   true,
	}) {
		if err != nil {
			t.Fatalf("expected no stream error, got %v", err)
		}
		if resp != nil && resp.Response != nil && resp.TurnComplete {
			final = resp.Response
		}
	}
	if final == nil {
		t.Fatal("expected final response")
	}
	if final.FinishReason != model.FinishReasonToolCalls {
		t.Fatalf("expected tool_calls finish reason, got %q", final.FinishReason)
	}
}

func TestOpenAICompatNonStream_AppliesRequestTimeout(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"test-model","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer server.Close()

	llm := newOpenAICompat(Config{
		Provider:   "openai-compatible",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    50 * time.Millisecond,
	}, "token")

	var gotErr error
	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   false,
	}) {
		if err != nil {
			gotErr = err
		}
	}
	if gotErr == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if !strings.Contains(strings.ToLower(gotErr.Error()), "context deadline exceeded") {
		t.Fatalf("expected context deadline exceeded, got %v", gotErr)
	}
}

func TestOpenAICompatNonStream_DefaultDoesNotApplyRequestTimeout(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"test-model","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	llm := newOpenAICompat(Config{
		Provider:   "openai-compatible",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	}, "token")

	var (
		gotErr    error
		finalText string
	)
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   false,
	}) {
		if err != nil {
			gotErr = err
			continue
		}
		if resp != nil && resp.Response != nil && resp.TurnComplete {
			finalText = resp.Response.Message.TextContent()
		}
	}
	if gotErr != nil {
		t.Fatalf("expected no timeout error, got %v", gotErr)
	}
	if finalText != "ok" {
		t.Fatalf("unexpected final text %q", finalText)
	}
}

func TestGeminiStream_DoesNotApplyRequestTimeout(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1beta/models/test-model:streamGenerateContent") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello\"}]}}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(150 * time.Millisecond)
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"!\"}]}}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":2,\"totalTokenCount\":3}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	llm := newGemini(Config{
		Provider:   "gemini",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    50 * time.Millisecond,
	}, "token")

	var (
		gotErr    error
		finalText string
	)
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   true,
	}) {
		if err != nil {
			gotErr = err
			continue
		}
		if resp != nil && resp.Response != nil && resp.TurnComplete {
			finalText = resp.Response.Message.TextContent()
		}
	}
	if gotErr != nil {
		t.Fatalf("expected no stream error, got %v", gotErr)
	}
	if finalText != "hello!" {
		t.Fatalf("unexpected final text %q", finalText)
	}
}

func TestGeminiStream_EmitsReasoningChunks(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1beta/models/test-model:streamGenerateContent") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"think-1\",\"thought\":true},{\"text\":\"hello\"}]}}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"think-2\",\"thought\":true},{\"text\":\"!\"}]}}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	llm := newGemini(Config{
		Provider:   "gemini",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	var (
		reasoningChunks []string
		finalReasoning  string
		finalText       string
	)
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   true,
		Reasoning: model.ReasoningConfig{
			Effort: "high",
		},
	}) {
		if err != nil {
			t.Fatalf("expected no stream error, got %v", err)
		}
		if resp == nil {
			continue
		}
		if resp.PartDelta != nil && resp.PartDelta.Kind == model.PartKindReasoning && strings.TrimSpace(resp.PartDelta.TextDelta) != "" {
			reasoningChunks = append(reasoningChunks, strings.TrimSpace(resp.PartDelta.TextDelta))
		}
		if resp.Response != nil && resp.TurnComplete {
			finalReasoning = strings.TrimSpace(resp.Response.Message.ReasoningText())
			finalText = strings.TrimSpace(resp.Response.Message.TextContent())
		}
	}
	if strings.Join(reasoningChunks, "|") != "think-1|think-2" {
		t.Fatalf("unexpected reasoning chunks: %v", reasoningChunks)
	}
	if finalReasoning != "think-1think-2" {
		t.Fatalf("unexpected final reasoning %q", finalReasoning)
	}
	if finalText != "hello!" {
		t.Fatalf("unexpected final text %q", finalText)
	}
}

func TestGeminiRequest_IncludesMaxOutputTokens(t *testing.T) {
	var gotMax float64
	var gotThinkingLevel string
	var gotIncludeThoughts bool
	var gotThinkingBudget any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1beta/models/test-model:generateContent") {
			http.NotFound(w, r)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		if cfg, ok := payload["generationConfig"].(map[string]any); ok {
			gotMax, _ = cfg["maxOutputTokens"].(float64)
			if thinking, ok := cfg["thinkingConfig"].(map[string]any); ok {
				gotThinkingLevel, _ = thinking["thinkingLevel"].(string)
				gotIncludeThoughts, _ = thinking["includeThoughts"].(bool)
				gotThinkingBudget = thinking["thinkingBudget"]
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
	}))
	defer server.Close()

	llm := newGemini(Config{
		Provider:     "gemini",
		Model:        "test-model",
		BaseURL:      server.URL,
		HTTPClient:   server.Client(),
		Timeout:      2 * time.Second,
		MaxOutputTok: 3072,
	}, "token")

	var gotErr error
	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   false,
		Reasoning: model.ReasoningConfig{
			Effort: "high",
		},
	}) {
		if err != nil {
			gotErr = err
		}
	}
	if gotErr != nil {
		t.Fatalf("expected no generate error, got %v", gotErr)
	}
	if gotMax != 3072 {
		t.Fatalf("expected generationConfig.maxOutputTokens=3072, got %v", gotMax)
	}
	if gotThinkingLevel != "HIGH" {
		t.Fatalf("expected generationConfig.thinkingConfig.thinkingLevel=HIGH, got %q", gotThinkingLevel)
	}
	if !gotIncludeThoughts {
		t.Fatalf("expected generationConfig.thinkingConfig.includeThoughts=true")
	}
	if gotThinkingBudget != nil {
		t.Fatalf("expected thinkingBudget omitted, got %v", gotThinkingBudget)
	}
}

func TestGeminiRequest_Pre3UsesThinkingBudget(t *testing.T) {
	var gotThinkingLevel string
	var gotThinkingBudget float64
	var gotIncludeThoughts bool
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1beta/models/gemini-2.5-flash:generateContent") {
			http.NotFound(w, r)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		if cfg, ok := payload["generationConfig"].(map[string]any); ok {
			if thinking, ok := cfg["thinkingConfig"].(map[string]any); ok {
				gotThinkingLevel, _ = thinking["thinkingLevel"].(string)
				gotThinkingBudget, _ = thinking["thinkingBudget"].(float64)
				gotIncludeThoughts, _ = thinking["includeThoughts"].(bool)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`)
	}))
	defer server.Close()

	llm := newGemini(Config{
		Provider:   "gemini",
		Model:      "gemini-2.5-flash",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages:  []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:    false,
		Reasoning: model.ReasoningConfig{Effort: "high"},
	}) {
		if err != nil {
			t.Fatalf("expected no generate error, got %v", err)
		}
	}

	if gotThinkingLevel != "" {
		t.Fatalf("expected thinkingLevel omitted for pre-3 model, got %q", gotThinkingLevel)
	}
	if gotThinkingBudget != 8192 {
		t.Fatalf("expected thinkingBudget=8192 for high effort, got %v", gotThinkingBudget)
	}
	if !gotIncludeThoughts {
		t.Fatalf("expected includeThoughts=true for enabled reasoning")
	}
}

func TestGeminiRequest_Pre3DisableReasoningUsesZeroBudget(t *testing.T) {
	var gotThinkingBudget float64
	var gotIncludeThoughts bool
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		if cfg, ok := payload["generationConfig"].(map[string]any); ok {
			if thinking, ok := cfg["thinkingConfig"].(map[string]any); ok {
				gotThinkingBudget, _ = thinking["thinkingBudget"].(float64)
				gotIncludeThoughts, _ = thinking["includeThoughts"].(bool)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`)
	}))
	defer server.Close()

	llm := newGemini(Config{
		Provider:   "gemini",
		Model:      "gemini-2.5-pro",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   false,
		Reasoning: model.ReasoningConfig{
			Effort: "none",
		},
	}) {
		if err != nil {
			t.Fatalf("expected no generate error, got %v", err)
		}
	}

	if gotThinkingBudget != 0 {
		t.Fatalf("expected thinkingBudget=0 when reasoning disabled, got %v", gotThinkingBudget)
	}
	if gotIncludeThoughts {
		t.Fatalf("expected includeThoughts=false when reasoning disabled")
	}
}

func TestGeminiRequest_BaseURLWithVersionPath(t *testing.T) {
	var gotPath string
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`)
	}))
	defer server.Close()

	llm := newGemini(Config{
		Provider:   "gemini",
		Model:      "test-model",
		BaseURL:    server.URL + "/v1beta",
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   false,
	}) {
		if err != nil {
			t.Fatalf("expected no generate error, got %v", err)
		}
	}

	if gotPath != "/v1beta/models/test-model:generateContent" {
		t.Fatalf("unexpected request path %q", gotPath)
	}
}

func TestGeminiRequest_XHighEffortFallsBackToHighLevel(t *testing.T) {
	var gotThinkingLevel string
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		if cfg, ok := payload["generationConfig"].(map[string]any); ok {
			if thinking, ok := cfg["thinkingConfig"].(map[string]any); ok {
				gotThinkingLevel, _ = thinking["thinkingLevel"].(string)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`)
	}))
	defer server.Close()

	llm := newGemini(Config{
		Provider:   "gemini",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages:  []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:    false,
		Reasoning: model.ReasoningConfig{Effort: "xhigh"},
	}) {
		if err != nil {
			t.Fatalf("expected no generate error, got %v", err)
		}
	}

	if gotThinkingLevel != "HIGH" {
		t.Fatalf("expected xhigh fallback to HIGH, got %q", gotThinkingLevel)
	}
}

func TestFromToOpenAIMessage(t *testing.T) {
	llm := newOpenAICompat(Config{
		Provider: "openai-compatible",
		Model:    "gpt-4o-mini",
		BaseURL:  "https://api.openai.com/v1",
		Timeout:  time.Second,
	}, "token")
	in := model.MessageFromAssistantParts("", "thinking...", []model.ToolCall{{
		ID:   "c1",
		Name: "echo",
		Args: jsonArgs(map[string]any{"text": "hello"}),
	}})
	raw := llm.fromKernelMessage(in)
	if raw.ReasoningContent != nil {
		t.Fatalf("did not expect reasoning_content in generic openai-compatible request")
	}
	back, err := toKernelMessage(openAICompatMsg{
		Role:       raw.Role,
		Content:    raw.Content,
		ToolCallID: raw.ToolCallID,
		ToolCalls:  raw.ToolCalls,
		ReasoningContent: func() string {
			if raw.ReasoningContent == nil {
				return ""
			}
			return *raw.ReasoningContent
		}(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(back.ToolCalls()) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(back.ToolCalls()))
	}
	if back.ToolCalls()[0].Name != "echo" {
		t.Fatalf("unexpected tool name %q", back.ToolCalls()[0].Name)
	}
	if back.ReasoningText() != "" {
		t.Fatalf("expected no reasoning in generic openai-compatible roundtrip, got %q", back.ReasoningText())
	}
}

func TestToKernelMessage_OpenAICompatKeepsRawToolArgsOnDecodeFailure(t *testing.T) {
	msg, err := toKernelMessage(openAICompatMsg{
		Role: "assistant",
		ToolCalls: []openAICompatToolCall{
			{
				ID:   "c1",
				Type: "function",
				Function: openAICompatCallFunction{
					Name:      "WRITE",
					Arguments: `{"path":`,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("expected no hard parse error, got %v", err)
	}
	if len(msg.ToolCalls()) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msg.ToolCalls()))
	}
	if got := strings.TrimSpace(msg.ToolCalls()[0].Args); got == "" {
		t.Fatalf("expected raw args kept, got %#v", msg.ToolCalls()[0])
	}
}

func TestDeepSeekThinkingPayload(t *testing.T) {
	llm := newDeepSeek(Config{
		Provider: "deepseek",
		Model:    "deepseek-v4-pro",
		BaseURL:  "https://api.deepseek.com/v1",
		Timeout:  time.Second,
	}, "token").(*openAICompatLLM)
	req := &model.Request{
		Messages: []model.Message{
			model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
				ID:   "c1",
				Name: "echo",
				Args: jsonArgs(map[string]any{"text": "hi"}),
			}}, ""),
		},
		Reasoning: model.ReasoningConfig{Effort: "high"},
	}
	payload := openAICompatRequest{
		Model:    "deepseek-v4-pro",
		Messages: llm.fromKernelMessages(nil, req.Messages),
	}
	llm.options.ApplyReasoning(&payload, req.Reasoning)
	if payload.Thinking == nil || payload.Thinking.Type != "enabled" {
		t.Fatalf("expected deepseek thinking config, got %#v", payload.Thinking)
	}
	if payload.ReasoningEffort != "high" {
		t.Fatalf("expected deepseek reasoning_effort=high, got %q", payload.ReasoningEffort)
	}
	if payload.Reasoning != nil {
		t.Fatalf("did not expect OpenAI reasoning block for deepseek payload")
	}
	if len(payload.Messages) != 1 || payload.Messages[0].ReasoningContent == nil {
		t.Fatalf("expected reasoning_content field for deepseek tool-call message")
	}
	if got := *payload.Messages[0].ReasoningContent; got != "" {
		t.Fatalf("expected empty reasoning_content for tool-call loop, got %q", got)
	}
	// When thinking is enabled the payload MaxTokens must be at least 32K so
	// the reasoning chain is not prematurely truncated.
	if payload.MaxTokens < thinkingModeMinTokens {
		t.Fatalf("expected MaxTokens >= %d when thinking enabled, got %d",
			thinkingModeMinTokens, payload.MaxTokens)
	}
}

func TestOpenAICompatProviderSpecificStructuredOutputStrategy(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"outcome": map[string]any{"type": "string"},
		},
		"required": []any{"outcome"},
	}
	tests := []struct {
		name string
		llm  *openAICompatLLM
		want string
	}{
		{
			name: "deepseek",
			llm: newDeepSeek(Config{
				Provider: "deepseek",
				Model:    "deepseek-v4-pro",
			}, "token").(*openAICompatLLM),
			want: "json_object",
		},
		{
			name: "mimo",
			llm: newMimo(Config{
				Provider: "xiaomi",
				Model:    "mimo-v2-pro",
			}, "token").(*openAICompatLLM),
			want: "json_object",
		},
		{
			name: "volcengine",
			llm: newVolcengine(Config{
				Provider: "volcengine",
				Model:    "doubao-seed",
			}, "token").(*openAICompatLLM),
			want: "json_object",
		},
		{
			name: "openai-compatible",
			llm: newOpenAICompat(Config{
				Provider: "openai-compatible",
				Model:    "gpt-compatible",
			}, "token"),
			want: "json_schema",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := openAICompatRequest{}
			applyOpenAICompatOutput(&payload, &model.OutputSpec{
				Mode:       model.OutputModeSchema,
				JSONSchema: schema,
			}, tc.llm.options.StructuredOutput)
			if payload.ResponseFormat == nil {
				t.Fatal("ResponseFormat = nil")
			}
			if payload.ResponseFormat.Type != tc.want {
				t.Fatalf("ResponseFormat.Type = %q, want %q", payload.ResponseFormat.Type, tc.want)
			}
			if tc.want == "json_object" && payload.ResponseFormat.JSONSchema != nil {
				t.Fatalf("JSONSchema = %#v, want nil for json_object strategy", payload.ResponseFormat.JSONSchema)
			}
		})
	}
}

func TestDeepSeekThinkingPayload_IncludesEmptyReasoningForPlainAssistantHistory(t *testing.T) {
	llm := newDeepSeek(Config{
		Provider: "deepseek",
		Model:    "deepseek-v4-pro",
		BaseURL:  "https://api.deepseek.com/v1",
		Timeout:  time.Second,
	}, "token").(*openAICompatLLM)
	req := &model.Request{
		Messages: []model.Message{
			model.NewTextMessage(model.RoleAssistant, "plain assistant from controller handoff"),
			model.NewTextMessage(model.RoleUser, "continue"),
		},
		Reasoning: model.ReasoningConfig{Effort: "high"},
	}
	payload := openAICompatRequest{
		Model:    "deepseek-v4-pro",
		Messages: llm.fromKernelMessages(nil, req.Messages),
	}
	llm.options.ApplyReasoning(&payload, req.Reasoning)
	if len(payload.Messages) != 2 {
		t.Fatalf("len(Messages) = %d, want 2", len(payload.Messages))
	}
	if payload.Messages[0].Role != string(model.RoleAssistant) || payload.Messages[0].ReasoningContent == nil {
		t.Fatalf("assistant message = %#v, want explicit empty reasoning_content", payload.Messages[0])
	}
	if got := *payload.Messages[0].ReasoningContent; got != "" {
		t.Fatalf("assistant reasoning_content = %q, want empty string", got)
	}
	if payload.Messages[1].ReasoningContent != nil {
		t.Fatalf("user message reasoning_content = %#v, want nil", payload.Messages[1].ReasoningContent)
	}
}

func TestDeepSeekThinkingPayload_SmallMaxTokensBumped(t *testing.T) {
	llm := newDeepSeek(Config{
		Provider:     "deepseek",
		Model:        "deepseek-v4-pro",
		BaseURL:      "https://api.deepseek.com/v1",
		Timeout:      time.Second,
		MaxOutputTok: 8192, // smaller than thinking min – must be bumped
	}, "token").(*openAICompatLLM)
	req := &model.Request{
		Messages:  []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Reasoning: model.ReasoningConfig{Effort: "medium"},
	}
	payload := openAICompatRequest{
		Model:     "deepseek-v4-pro",
		Messages:  llm.fromKernelMessages(nil, req.Messages),
		MaxTokens: llm.maxOutputTok, // 8192 from config
	}
	llm.options.ApplyReasoning(&payload, req.Reasoning)
	if payload.Thinking == nil || payload.Thinking.Type != "enabled" {
		t.Fatalf("expected thinking enabled")
	}
	if payload.ReasoningEffort != "high" {
		t.Fatalf("expected medium to map to reasoning_effort=high, got %q", payload.ReasoningEffort)
	}
	if payload.MaxTokens < thinkingModeMinTokens {
		t.Fatalf("expected MaxTokens bumped to >= %d, got %d",
			thinkingModeMinTokens, payload.MaxTokens)
	}
}

func TestDeepSeekThinkingPayload_DefaultUsesHighEffort(t *testing.T) {
	llm := newDeepSeek(Config{
		Provider:     "deepseek",
		Model:        "deepseek-v4-pro",
		BaseURL:      "https://api.deepseek.com/v1",
		Timeout:      time.Second,
		MaxOutputTok: 400000,
	}, "token").(*openAICompatLLM)
	req := &model.Request{
		Messages:  []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Reasoning: model.ReasoningConfig{},
	}
	payload := openAICompatRequest{
		Model:     "deepseek-v4-pro",
		Messages:  llm.fromKernelMessages(nil, req.Messages),
		MaxTokens: llm.maxOutputTok,
	}
	llm.options.ApplyReasoning(&payload, req.Reasoning)
	if payload.Thinking == nil || payload.Thinking.Type != "enabled" {
		t.Fatalf("expected thinking enabled")
	}
	if payload.ReasoningEffort != "high" {
		t.Fatalf("expected default reasoning_effort=high, got %q", payload.ReasoningEffort)
	}
	if payload.MaxTokens != deepSeekMaxTokens {
		t.Fatalf("expected MaxTokens capped to %d for default thinking, got %d", deepSeekMaxTokens, payload.MaxTokens)
	}
}

func TestDeepSeekThinkingPayload_MaxEffort(t *testing.T) {
	llm := newDeepSeek(Config{
		Provider: "deepseek",
		Model:    "deepseek-v4-pro",
		BaseURL:  "https://api.deepseek.com/v1",
		Timeout:  time.Second,
	}, "token").(*openAICompatLLM)
	payload := openAICompatRequest{
		Model:    "deepseek-v4-pro",
		Messages: llm.fromKernelMessages(nil, []model.Message{model.NewTextMessage(model.RoleUser, "hi")}),
	}
	llm.options.ApplyReasoning(&payload, model.ReasoningConfig{Effort: "xhigh"})
	if payload.Thinking == nil || payload.Thinking.Type != "enabled" {
		t.Fatalf("expected thinking enabled")
	}
	if payload.ReasoningEffort != "max" {
		t.Fatalf("expected xhigh to map to reasoning_effort=max, got %q", payload.ReasoningEffort)
	}
}

func TestDeepSeekThinkingPayload_DisabledCapsToChatRange(t *testing.T) {
	llm := newDeepSeek(Config{
		Provider:     "deepseek",
		Model:        "deepseek-v4-pro",
		BaseURL:      "https://api.deepseek.com/v1",
		Timeout:      time.Second,
		MaxOutputTok: 400000,
	}, "token").(*openAICompatLLM)
	req := &model.Request{
		Messages: []model.Message{
			model.NewTextMessage(model.RoleAssistant, "previous assistant"),
			model.NewTextMessage(model.RoleUser, "hi"),
		},
		Reasoning: model.ReasoningConfig{Effort: "none"},
	}
	payload := openAICompatRequest{
		Model:     "deepseek-v4-pro",
		Messages:  llm.fromKernelMessages(nil, req.Messages),
		MaxTokens: llm.maxOutputTok,
	}
	llm.options.ApplyReasoning(&payload, req.Reasoning)
	if payload.Thinking == nil || payload.Thinking.Type != "disabled" {
		t.Fatalf("expected thinking disabled")
	}
	if payload.MaxTokens != deepSeekMaxTokens {
		t.Fatalf("expected MaxTokens capped to %d when thinking is disabled, got %d", deepSeekMaxTokens, payload.MaxTokens)
	}
	for i, msg := range payload.Messages {
		if msg.ReasoningContent != nil {
			t.Fatalf("message %d reasoning_content = %#v, want nil when thinking disabled", i, msg.ReasoningContent)
		}
	}
}

func TestDeepSeekV4FlashSupportsReasoningAndCapsTokens(t *testing.T) {
	llm := newDeepSeek(Config{
		Provider:     "deepseek",
		Model:        "deepseek-v4-flash",
		BaseURL:      "https://api.deepseek.com/v1",
		Timeout:      time.Second,
		MaxOutputTok: 400000,
	}, "token").(*openAICompatLLM)
	payload := openAICompatRequest{
		Model:     "deepseek-v4-flash",
		Messages:  llm.fromKernelMessages(nil, []model.Message{model.NewTextMessage(model.RoleUser, "hi")}),
		MaxTokens: llm.maxOutputTok,
	}
	llm.options.ApplyReasoning(&payload, model.ReasoningConfig{Effort: "high"})
	if payload.Thinking == nil || payload.Thinking.Type != "enabled" {
		t.Fatalf("expected thinking payload for deepseek-v4-flash, got %#v", payload.Thinking)
	}
	if payload.ReasoningEffort != "high" {
		t.Fatalf("expected deepseek-v4-flash reasoning_effort=high, got %q", payload.ReasoningEffort)
	}
	if payload.MaxTokens != deepSeekMaxTokens {
		t.Fatalf("expected MaxTokens capped to %d for deepseek-v4-flash, got %d", deepSeekMaxTokens, payload.MaxTokens)
	}
}

func TestDeepSeekUsagePropagatesPromptCacheHitTokens(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"deepseek-v4-pro","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":42,"prompt_cache_hit_tokens":31,"prompt_cache_miss_tokens":11,"completion_tokens":8,"total_tokens":50}}`)
	}))
	defer server.Close()

	llm := newDeepSeek(Config{
		Provider:   "deepseek",
		Model:      "deepseek-v4-pro",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    time.Second,
	}, "token")

	var final *model.Response
	for event, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if event != nil && event.Response != nil {
			final = event.Response
		}
	}
	if final == nil {
		t.Fatal("expected final response")
	}
	if final.Usage.PromptTokens != 42 || final.Usage.CachedInputTokens != 31 || final.Usage.CompletionTokens != 8 || final.Usage.TotalTokens != 50 {
		t.Fatalf("usage = %+v, want DeepSeek cache-hit usage propagated", final.Usage)
	}
}

func TestCodeFreeDoesNotApplyReasoningPayload(t *testing.T) {
	llm := newCodeFree(Config{
		Provider: "codefree",
		Model:    "GLM-5.1",
		BaseURL:  "https://www.srdcloud.cn",
		Timeout:  time.Second,
	}).(*codeFreeLLM)
	if llm.options.ApplyReasoning != nil {
		t.Fatal("CodeFree ApplyReasoning is configured, want nil")
	}
}

func TestMimoProviderUsesThinkingPayload(t *testing.T) {
	llm := newMimo(Config{
		Provider: "xiaomi",
		Model:    "mimo",
		BaseURL:  "https://api.xiaomimimo.com/v1",
		Timeout:  time.Second,
	}, "token").(*openAICompatLLM)
	payload := openAICompatRequest{
		Model: "mimo",
		Messages: llm.fromKernelMessages(nil, []model.Message{
			model.NewTextMessage(model.RoleUser, "hello"),
		}),
	}
	llm.options.ApplyReasoning(&payload, model.ReasoningConfig{Effort: "high"})
	if payload.Thinking == nil || payload.Thinking.Type != "enabled" {
		t.Fatalf("expected mimo thinking payload, got %#v", payload.Thinking)
	}
	if payload.Reasoning != nil || payload.ReasoningEffort != "" {
		t.Fatalf("did not expect openai reasoning fields for mimo payload")
	}
}

func TestMimoUsagePropagatesPromptTokenDetailsCachedTokens(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"mimo-v2-flash","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":64,"completion_tokens":9,"total_tokens":73,"prompt_tokens_details":{"cached_tokens":48,"audio_tokens":0}}}`)
	}))
	defer server.Close()

	llm := newMimo(Config{
		Provider:   "xiaomi",
		Model:      "mimo-v2-flash",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    time.Second,
	}, "token")

	var final *model.Response
	for event, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if event != nil && event.Response != nil {
			final = event.Response
		}
	}
	if final == nil {
		t.Fatal("expected final response")
	}
	if final.Usage.PromptTokens != 64 || final.Usage.CachedInputTokens != 48 || final.Usage.CompletionTokens != 9 || final.Usage.TotalTokens != 73 {
		t.Fatalf("usage = %+v, want MiMo cached usage propagated", final.Usage)
	}
}

func TestVolcengineCodingPlanReasoningDisabledSendsThinkingDisabled(t *testing.T) {
	llm := newVolcengineCodingPlan(Config{
		Provider: "volcengine",
		Model:    "doubao-seed-2.0-pro",
		BaseURL:  "https://ark.cn-beijing.volces.com/api/coding/v3",
		Timeout:  time.Second,
	}, "token").(*openAICompatLLM)
	payload := openAICompatRequest{
		Model: "doubao-seed-2.0-pro",
		Messages: llm.fromKernelMessages(nil, []model.Message{
			model.NewTextMessage(model.RoleUser, "hello"),
		}),
	}
	llm.options.ApplyReasoning(&payload, model.ReasoningConfig{Effort: "none"})
	if payload.Thinking == nil || payload.Thinking.Type != "disabled" {
		t.Fatalf("expected volcengine coding plan payload to disable thinking explicitly, got %#v", payload.Thinking)
	}
	if payload.Reasoning != nil || payload.ReasoningEffort != "" {
		t.Fatalf("did not expect openai reasoning fields for volcengine coding plan payload")
	}
}

func TestOpenAICompatEffortReasoningUsesOpenAIReasoningPayload(t *testing.T) {
	llm := newOpenAICompat(Config{
		Provider:      "openai-compatible",
		Model:         "gpt-5",
		BaseURL:       "https://example.com/v1",
		Timeout:       time.Second,
		ReasoningMode: "effort",
	}, "token")
	payload := openAICompatRequest{
		Model: "gpt-5",
		Messages: llm.fromKernelMessages(nil, []model.Message{
			model.NewTextMessage(model.RoleUser, "hello"),
		}),
	}
	llm.options.ApplyReasoning(&payload, model.ReasoningConfig{Effort: "high"})
	if payload.Reasoning == nil || payload.Reasoning.Effort != "high" {
		t.Fatalf("expected effort openai-compatible payload to carry reasoning effort, got %#v", payload.Reasoning)
	}
	if payload.ReasoningEffort != "high" {
		t.Fatalf("expected compatibility reasoning_effort=high, got %q", payload.ReasoningEffort)
	}
	if payload.Thinking != nil {
		t.Fatalf("did not expect thinking payload for effort openai-compatible request")
	}
}

func TestOpenAICompatMessageTransform_SkipsInvalidToolResponses(t *testing.T) {
	llm := newOpenAICompat(Config{
		Provider: "openai-compatible",
		Model:    "test-model",
		BaseURL:  "https://example.com/v1",
		Timeout:  time.Second,
	}, "token")
	messages := llm.fromKernelMessages(nil, []model.Message{
		model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
			ID:   "call_1",
			Name: "echo",
			Args: jsonArgs(map[string]any{"text": "x"}),
		}}, ""),
		model.MessageFromToolResponse(&model.ToolResponse{
			ID:     "",
			Name:   "echo",
			Result: map[string]any{"echo": "missing-id"},
		}),
		model.MessageFromToolResponse(&model.ToolResponse{
			ID:     "call_2",
			Name:   "echo",
			Result: map[string]any{"echo": "unmatched-id"},
		}),
		model.MessageFromToolResponse(&model.ToolResponse{
			ID:     "call_1",
			Name:   "echo",
			Result: map[string]any{"echo": "ok"},
		}),
		{
			Role: model.RoleTool,
		},
	})
	if len(messages) != 2 {
		t.Fatalf("expected 2 transformed messages, got %d", len(messages))
	}
	if messages[1].Role != string(model.RoleTool) {
		t.Fatalf("expected tool role at index 1, got %q", messages[1].Role)
	}
	if messages[1].ToolCallID != "call_1" {
		t.Fatalf("expected tool_call_id=call_1, got %q", messages[1].ToolCallID)
	}
}

func TestAnthropicMessageTransform(t *testing.T) {
	system := toAnthropicSystem([]model.Part{model.NewTextPart("sys")})
	msgs := toAnthropicMessages([]model.Message{
		model.NewTextMessage(model.RoleSystem, "sys"),
		model.NewTextMessage(model.RoleUser, "u"),
		model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
			ID:   "call1",
			Name: "echo",
			Args: jsonArgs(map[string]any{"text": "x"}),
		}}, ""),
	})
	if len(system) != 1 || system[0].Text != "sys" {
		t.Fatalf("unexpected system blocks: %+v", system)
	}
	if len(msgs) < 2 {
		t.Fatalf("expected >= 2 messages, got %d", len(msgs))
	}
}

func TestAnthropicSDKNonStream_NormalizesBaseURLAndMapsParts(t *testing.T) {
	var sawCustomTool bool
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "sk-anthropic" {
			t.Fatalf("expected x-api-key header, got %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got == "" {
			t.Fatal("expected anthropic-version header")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		system, _ := payload["system"].([]any)
		if len(system) != 1 {
			t.Fatalf("expected one system block, got %+v", payload["system"])
		}
		sys0, _ := system[0].(map[string]any)
		if sys0["text"] != "system instruction" {
			t.Fatalf("unexpected system block %+v", sys0)
		}
		messages, _ := payload["messages"].([]any)
		if len(messages) != 3 {
			t.Fatalf("expected 3 messages, got %+v", payload["messages"])
		}
		assistant, _ := messages[1].(map[string]any)
		assistantContent, _ := assistant["content"].([]any)
		if len(assistantContent) != 3 {
			t.Fatalf("expected assistant thinking/text/tool_use blocks, got %+v", assistantContent)
		}
		thinking, _ := assistantContent[0].(map[string]any)
		if thinking["type"] != "thinking" || thinking["signature"] != "sig-prev" || thinking["thinking"] != "prior reasoning" {
			t.Fatalf("unexpected thinking block %+v", thinking)
		}
		toolUse, _ := assistantContent[2].(map[string]any)
		if toolUse["type"] != "tool_use" || toolUse["id"] != "call-prev" || toolUse["name"] != "echo" {
			t.Fatalf("unexpected tool_use block %+v", toolUse)
		}
		toolMessage, _ := messages[2].(map[string]any)
		toolContent, _ := toolMessage["content"].([]any)
		if len(toolContent) != 1 {
			t.Fatalf("expected single tool_result block, got %+v", toolMessage)
		}
		toolResult, _ := toolContent[0].(map[string]any)
		if toolResult["type"] != "tool_result" || toolResult["tool_use_id"] != "call-prev" {
			t.Fatalf("unexpected tool_result block %+v", toolResult)
		}
		tools, _ := payload["tools"].([]any)
		if len(tools) != 1 {
			t.Fatalf("expected one declared tool, got %+v", payload["tools"])
		}
		toolDecl, _ := tools[0].(map[string]any)
		if toolDecl["name"] == "lookup" {
			sawCustomTool = true
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"test-model","stop_reason":"tool_use","stop_sequence":"","content":[{"type":"thinking","thinking":"I'll call the tool.","signature":"sig-final"},{"type":"text","text":"Let me check."},{"type":"tool_use","id":"call_2","name":"lookup","input":{"q":"weather"}}],"usage":{"input_tokens":11,"output_tokens":7}}`)
	}))
	defer server.Close()

	llm := newAnthropic(Config{
		Provider:   "anthropic",
		API:        APIAnthropic,
		Model:      "test-model",
		BaseURL:    server.URL + "/v1",
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
		Auth: AuthConfig{
			Type:  AuthAPIKey,
			Token: "sk-anthropic",
		},
	}, "sk-anthropic")

	priorReasoning := model.NewReasoningPart("prior reasoning", model.ReasoningVisibilityVisible)
	priorReasoning.Reasoning.Replay = &model.ReplayMeta{Provider: "anthropic", Kind: anthropicReplayKindThinkingSignature, Token: "sig-prev"}

	var final *model.Response
	for event, err := range llm.Generate(context.Background(), &model.Request{
		Instructions: []model.Part{model.NewTextPart("system instruction")},
		Messages: []model.Message{
			model.NewTextMessage(model.RoleUser, "hello"),
			model.NewMessage(
				model.RoleAssistant,
				priorReasoning,
				model.NewTextPart("Working."),
				model.NewToolUsePart("call-prev", "echo", json.RawMessage(`{"text":"x"}`)),
			),
			model.MessageFromToolResponse(&model.ToolResponse{
				ID:     "call-prev",
				Name:   "echo",
				Result: map[string]any{"echo": "x"},
			}),
		},
		Tools: []model.ToolSpec{
			model.NewFunctionToolSpec("lookup", "Look up weather.", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"q": map[string]any{"type": "string"},
				},
				"required": []string{"q"},
			}),
		},
	}) {
		if err != nil {
			t.Fatalf("generate failed: %v", err)
		}
		if event != nil && event.Response != nil && event.TurnComplete {
			final = event.Response
		}
	}
	if !sawCustomTool {
		t.Fatal("expected tool declaration in anthropic request")
	}
	if final == nil {
		t.Fatal("expected final response")
	}
	if final.FinishReason != model.FinishReasonToolCalls {
		t.Fatalf("expected tool_calls finish reason, got %q", final.FinishReason)
	}
	if got := final.Message.TextContent(); got != "Let me check." {
		t.Fatalf("unexpected final text %q", got)
	}
	if got := final.Message.ReasoningText(); got != "I'll call the tool." {
		t.Fatalf("unexpected reasoning text %q", got)
	}
	reasoningParts := final.Message.ReasoningParts()
	if len(reasoningParts) != 1 || reasoningParts[0].Replay == nil || reasoningParts[0].Replay.Token != "sig-final" {
		t.Fatalf("expected thinking signature replay token, got %+v", reasoningParts)
	}
	toolCalls := final.Message.ToolCalls()
	if len(toolCalls) != 1 || toolCalls[0].Name != "lookup" || toolCalls[0].Args != `{"q":"weather"}` {
		t.Fatalf("unexpected tool calls %+v", toolCalls)
	}
}

func TestAnthropicSDKStream_MapsThinkingDeltasAndSignature(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("x-minimax-api-key"); got != "compat-token" {
			t.Fatalf("expected custom auth header, got %q", got)
		}
		if got := r.Header.Get("x-extra-header"); got != "1" {
			t.Fatalf("expected configured header, got %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test-model\",\"content\":[],\"stop_reason\":\"\",\"stop_sequence\":\"\",\"usage\":{\"input_tokens\":11,\"output_tokens\":0}}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"I should think first. \"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig-stream\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello world\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
		_, _ = fmt.Fprint(w, "event: message_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":\"\"},\"usage\":{\"input_tokens\":11,\"output_tokens\":7,\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":4,\"server_tool_use\":{\"web_fetch_requests\":0,\"web_search_requests\":0}}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	llm := newAnthropic(Config{
		Provider:   "anthropic-compatible",
		API:        APIAnthropicCompatible,
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
		Auth: AuthConfig{
			Type:      AuthAPIKey,
			Token:     "compat-token",
			HeaderKey: "x-minimax-api-key",
		},
		Headers: map[string]string{"x-extra-header": "1"},
	}, "compat-token")

	var (
		reasoningDelta string
		textDelta      string
		final          *model.Response
	)
	for event, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
		Stream:   true,
	}) {
		if err != nil {
			t.Fatalf("generate failed: %v", err)
		}
		if event == nil {
			continue
		}
		if event.PartDelta != nil {
			switch event.PartDelta.Kind {
			case model.PartKindReasoning:
				reasoningDelta += event.PartDelta.TextDelta
			case model.PartKindText:
				textDelta += event.PartDelta.TextDelta
			}
		}
		if event.Response != nil && event.TurnComplete {
			final = event.Response
		}
	}
	if reasoningDelta != "I should think first. " {
		t.Fatalf("unexpected reasoning delta %q", reasoningDelta)
	}
	if textDelta != "Hello world" {
		t.Fatalf("unexpected text delta %q", textDelta)
	}
	if final == nil {
		t.Fatal("expected final streamed response")
	}
	if final.FinishReason != model.FinishReasonStop {
		t.Fatalf("expected stop finish reason, got %q", final.FinishReason)
	}
	if got := final.Message.TextContent(); got != "Hello world" {
		t.Fatalf("unexpected final text %q", got)
	}
	if final.Usage.PromptTokens != 11 || final.Usage.CachedInputTokens != 4 || final.Usage.CompletionTokens != 7 || final.Usage.TotalTokens != 18 {
		t.Fatalf("unexpected usage: %+v", final.Usage)
	}
	reasoningParts := final.Message.ReasoningParts()
	if len(reasoningParts) != 1 || reasoningParts[0].Replay == nil || reasoningParts[0].Replay.Token != "sig-stream" {
		t.Fatalf("expected streamed signature replay token, got %+v", reasoningParts)
	}
}

func TestMiniMaxStream_EmitsStartBlockTextWithoutSmoothingAtProviderLayer(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" && r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"MiniMax-M2.5\",\"content\":[],\"stop_reason\":\"\",\"stop_sequence\":\"\",\"usage\":{\"input_tokens\":11,\"output_tokens\":0}}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"MiniMax streaming \"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"should feel much smoother in the terminal output.\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = fmt.Fprint(w, "event: message_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":\"\"},\"usage\":{\"input_tokens\":11,\"output_tokens\":12}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	llm := newAnthropic(Config{
		Provider:   "minimax",
		API:        APIAnthropicCompatible,
		Model:      "MiniMax-M2.5",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
		Auth: AuthConfig{
			Type:      AuthAPIKey,
			Token:     "compat-token",
			HeaderKey: "x-minimax-api-key",
		},
	}, "compat-token")

	var (
		textChunks []string
		final      *model.Response
	)
	for event, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
		Stream:   true,
	}) {
		if err != nil {
			t.Fatalf("generate failed: %v", err)
		}
		if event == nil {
			continue
		}
		if event.PartDelta != nil && event.PartDelta.Kind == model.PartKindText && event.PartDelta.TextDelta != "" {
			textChunks = append(textChunks, event.PartDelta.TextDelta)
		}
		if event.Response != nil && event.TurnComplete {
			final = event.Response
		}
	}

	if len(textChunks) != 2 {
		t.Fatalf("expected start block text plus one delta, got %v", textChunks)
	}
	if got := strings.Join(textChunks, ""); got != "MiniMax streaming should feel much smoother in the terminal output." {
		t.Fatalf("unexpected streamed text %q", got)
	}
	if final == nil {
		t.Fatal("expected final streamed response")
	}
	if got := final.Message.TextContent(); got != "MiniMax streaming should feel much smoother in the terminal output." {
		t.Fatalf("unexpected final text %q", got)
	}
}

func TestMiniMaxUsesAnthropicCompatibleConstructorDefaults(t *testing.T) {
	llm := newMiniMax(Config{
		Provider: "minimax",
		API:      APIMiniMax,
		Model:    "MiniMax-M2",
		Auth: AuthConfig{
			Type:  AuthBearerToken,
			Token: "compat-token",
		},
	}, "compat-token")
	typed, ok := llm.(*anthropicSDKLLM)
	if !ok {
		t.Fatalf("newAnthropic() = %T, want *anthropicSDKLLM", llm)
	}
	if typed.baseURL != miniMaxDefaultBaseURL {
		t.Fatalf("baseURL = %q, want %q", typed.baseURL, miniMaxDefaultBaseURL)
	}
	if typed.maxOutputTok != 4096 {
		t.Fatalf("maxOutputTok = %d, want 4096", typed.maxOutputTok)
	}
}

func TestGeminiMessageTransform(t *testing.T) {
	system, msgs, err := toGeminiContents(nil, []model.Message{
		model.NewTextMessage(model.RoleSystem, "sys"),
		model.NewTextMessage(model.RoleUser, "u"),
		model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
			ID:               "call1",
			Name:             "echo",
			Args:             jsonArgs(map[string]any{"text": "x"}),
			ThoughtSignature: "sig-1",
		}}, ""),
	})
	if err != nil {
		t.Fatalf("toGeminiContents: %v", err)
	}
	if system != "sys" {
		t.Fatalf("unexpected system text: %q", system)
	}
	if len(msgs) < 2 {
		t.Fatalf("expected >= 2 messages, got %d", len(msgs))
	}
	parts := msgs[len(msgs)-1].Parts
	if len(parts) == 0 || parts[0].FunctionCall == nil {
		t.Fatalf("expected function call part in last gemini message")
	}
	if string(parts[0].ThoughtSignature) != "sig-1" {
		t.Fatalf("expected thought signature propagated, got %q", string(parts[0].ThoughtSignature))
	}
}

func TestGeminiMessageTransform_SkipsToolCallWithoutThoughtSignature(t *testing.T) {
	_, msgs, err := toGeminiContents(nil, []model.Message{
		model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
			ID:   "call1",
			Name: "BASH",
			Args: jsonArgs(map[string]any{"command": "ls"}),
		}}, "tool planned"),
	})
	if err != nil {
		t.Fatalf("toGeminiContents: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if len(msgs[0].Parts) != 1 {
		t.Fatalf("expected only assistant text part, got %d", len(msgs[0].Parts))
	}
	if msgs[0].Parts[0].FunctionCall != nil {
		t.Fatalf("expected tool call without thought signature to be skipped")
	}
}

func TestGeminiResponseToMessage_PreservesThoughtSignature(t *testing.T) {
	msg, _, err := geminiResponseToMessage(&genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Parts: []*genai.Part{
						{
							ThoughtSignature: []byte("sig-call-1"),
							FunctionCall: &genai.FunctionCall{
								Name: "BASH",
								Args: map[string]any{"command": "ls"},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls()) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msg.ToolCalls()))
	}
	if msg.ToolCalls()[0].ThoughtSignature == "sig-call-1" {
		t.Fatalf("expected thought signature to be encoded for lossless persistence, got raw %q", msg.ToolCalls()[0].ThoughtSignature)
	}
	if got := decodeGeminiThoughtSignature(msg.ToolCalls()[0].ThoughtSignature); string(got) != "sig-call-1" {
		t.Fatalf("expected decoded thought signature kept, got %q", string(got))
	}
}

func TestGeminiResponseToMessage_ExtractsReasoningText(t *testing.T) {
	msg, _, err := geminiResponseToMessage(&genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Parts: []*genai.Part{
						{Text: "thought-1", Thought: true},
						{Text: "answer"},
						{Text: "thought-2", Thought: true},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.TextContent() != "answer" {
		t.Fatalf("unexpected answer text %q", msg.TextContent())
	}
	if msg.ReasoningText() != "thought-1\nthought-2" {
		t.Fatalf("unexpected reasoning text %q", msg.ReasoningText())
	}
}

func TestGeminiResponseToMessage_DoesNotClassifyAnswerTextAsReasoningByThoughtSignature(t *testing.T) {
	msg, _, err := geminiResponseToMessage(&genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Parts: []*genai.Part{
						{Text: "thought-signature", ThoughtSignature: []byte("sig-thought")},
						{Text: "answer"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.TextContent() != "thought-signature\nanswer" {
		t.Fatalf("unexpected answer text %q", msg.TextContent())
	}
	if msg.ReasoningText() != "" {
		t.Fatalf("unexpected reasoning text %q", msg.ReasoningText())
	}
}

func TestGeminiResponseDecode_PartLevelThoughtSignature(t *testing.T) {
	raw := []byte(`{
		"candidates":[
			{
				"content":{
					"parts":[
						{
							"functionCall":{"name":"BASH","args":{"command":"ls"}},
							"thoughtSignature":"c2lnLXBhcnQtMQ=="
						}
					]
				}
			}
		]
	}`)
	var out genai.GenerateContentResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	msg, _, err := geminiResponseToMessage(&out)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls()) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msg.ToolCalls()))
	}
	if got := decodeGeminiThoughtSignature(msg.ToolCalls()[0].ThoughtSignature); string(got) != "sig-part-1" {
		t.Fatalf("expected part-level thought signature, got %q", string(got))
	}
}

func TestDedupToolCalls_MergesLateThoughtSignature(t *testing.T) {
	calls := dedupToolCalls([]model.ToolCall{
		{
			ID:   "BASH",
			Name: "BASH",
			Args: jsonArgs(map[string]any{"command": "ls"}),
		},
		{
			ID:               "BASH",
			Name:             "BASH",
			Args:             jsonArgs(map[string]any{"command": "ls -la"}),
			ThoughtSignature: "sig-late-1",
		},
	})
	if len(calls) != 1 {
		t.Fatalf("expected 1 merged call, got %d", len(calls))
	}
	if calls[0].ThoughtSignature != "sig-late-1" {
		t.Fatalf("expected merged thought signature, got %q", calls[0].ThoughtSignature)
	}
	if strings.TrimSpace(calls[0].Args) != `{"command":"ls -la"}` {
		t.Fatalf("expected latest args merged, got %v", calls[0].Args)
	}
}

func TestGeminiThoughtSignature_BinaryRoundTrip(t *testing.T) {
	raw := []byte{0x00, 0x01, 0x02, 0xff, 0x20, 0x09}
	encoded := encodeGeminiThoughtSignature(raw)
	if encoded == "" || encoded == string(raw) {
		t.Fatalf("expected non-empty encoded signature, got %q", encoded)
	}
	decoded := decodeGeminiThoughtSignature(encoded)
	if !bytes.Equal(decoded, raw) {
		t.Fatalf("expected decoded signature to match raw bytes")
	}
	legacy := decodeGeminiThoughtSignature("sig-legacy-1")
	if string(legacy) != "sig-legacy-1" {
		t.Fatalf("expected legacy signature compatibility, got %q", string(legacy))
	}
}
