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

	"github.com/OnslaughtSnail/caelis/internal/testenv"
	"github.com/OnslaughtSnail/caelis/ports/model"
)

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
	setHomeForCodeFreeTest(t, home)
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

func TestReadCodeFreeStoredCredentialsIgnoresLegacyCodeFreeCreds(t *testing.T) {
	home := t.TempDir()
	setHomeForCodeFreeTest(t, home)
	t.Setenv(codeFreeCredsPathEnv, "")

	primary, err := resolveCodeFreeDefaultCredentialPath()
	if err != nil {
		t.Fatalf("resolveCodeFreeDefaultCredentialPath() error = %v", err)
	}
	legacy := filepath.Join(home, ".codefree-cli", codeFreeDefaultCredentialFile)
	writeCodeFreeCredsAtPathForTest(t, legacy, "272182", "legacy-api-key")

	if _, err := readCodeFreeStoredCredentials(); err == nil {
		t.Fatal("readCodeFreeStoredCredentials() error = nil, want missing current credentials")
	}
	if _, err := os.Stat(primary); err == nil {
		t.Fatalf("unexpected imported credentials at %q", primary)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("expected legacy credentials to remain at %q: %v", legacy, err)
	}
}

func setHomeForCodeFreeTest(t *testing.T, home string) {
	t.Helper()
	testenv.SetHome(t, home)
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
