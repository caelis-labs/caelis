package providers

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
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
	path := filepath.Join(dir, codeFreeDefaultCredentialFile)
	writeCodeFreeCredsAtPathForTest(t, path, userID, apiKey, "")
	return path
}

func writeCodeFreeCredsAtPathForTest(t *testing.T, path string, userID string, apiKey string, baseURL string) {
	t.Helper()
	payload := map[string]string{
		"encryptedApiKey": encryptCodeFreeAPIKeyForTest(t, apiKey),
		"userId":          userID,
		"sessionId":       "login-session-" + userID,
	}
	if baseURL := strings.TrimSpace(baseURL); baseURL != "" {
		payload["baseUrlSnapshot"] = baseURL
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
	state := base64.StdEncoding.EncodeToString([]byte("http://127.0.0.1:12345/oauth2callback?from=codefree-o&randomCode=1234"))
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

	var seenHeaders http.Header
	var seenPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	ctx := model.WithProviderRequestMetadata(context.Background(), model.ProviderRequestMetadata{
		SessionAffinity: "caelis-session-1",
	})
	for resp, err := range llm.Generate(ctx, &model.Request{
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
	if got := seenHeaders.Get("Authorization"); got != "" {
		t.Fatalf("authorization = %q, want empty", got)
	}
	if got := seenHeaders.Get("Accept"); got != "application/json" {
		t.Fatalf("accept = %q, want application/json", got)
	}
	if got := seenHeaders.Get("userId"); got != "272182" {
		t.Fatalf("userid = %q, want %q", got, "272182")
	}
	if got := seenHeaders.Get("apiKey"); got != "76475baf-3659-488a-932d-0971ae103591" {
		t.Fatalf("apikey = %q", got)
	}
	if got := seenHeaders.Get("modelName"); got != "GLM-4.7" {
		t.Fatalf("modelname = %q, want GLM-4.7", got)
	}
	if got := seenHeaders.Get("clientType"); got != codeFreeDefaultClientType {
		t.Fatalf("clienttype = %q, want %q", got, codeFreeDefaultClientType)
	}
	if got := seenHeaders.Get("subService"); got != codeFreeDefaultSubservice {
		t.Fatalf("subservice = %q, want %q", got, codeFreeDefaultSubservice)
	}
	if got := seenHeaders.Get("clientVersion"); got != "1.3.1" {
		t.Fatalf("clientversion = %q, want 1.3.1", got)
	}
	if got := seenHeaders.Get("sessionId"); got != "caelis-session-1" {
		t.Fatalf("sessionid = %q, want caelis-session-1", got)
	}
	if got := seenPayload["temperature"]; got != float64(0) {
		t.Fatalf("temperature = %#v, want 0", got)
	}
	if got := seenPayload["modelName"]; got != "GLM-4.7" {
		t.Fatalf("modelName payload = %#v, want GLM-4.7", got)
	}
	if got := seenPayload["top_p"]; got != float64(1) {
		t.Fatalf("top_p = %#v, want 1", got)
	}
	if _, ok := seenPayload["stream_options"]; ok {
		t.Fatalf("non-stream payload unexpectedly included stream_options: %#v", seenPayload["stream_options"])
	}
}

func TestResolveCodeFreeDefaultCredentialPath_UsesCaelisStore(t *testing.T) {
	home := t.TempDir()
	setHomeForCodeFreeTest(t, home)

	got, err := resolveCodeFreeDefaultCredentialPath()
	if err != nil {
		t.Fatalf("resolveCodeFreeDefaultCredentialPath() error = %v", err)
	}
	want := filepath.Join(home, ".caelis", filepath.FromSlash(codeFreeCredentialDir), codeFreeDefaultCredentialFile)
	if got != want {
		t.Fatalf("credential path = %q, want %q", got, want)
	}
}

func TestLoadCodeFreeCredentialsImportsCodeFreeOLocalCredentials(t *testing.T) {
	home := t.TempDir()
	setHomeForCodeFreeTest(t, home)
	t.Setenv(codeFreeCredsPathEnv, "")

	primary, err := resolveCodeFreeDefaultCredentialPath()
	if err != nil {
		t.Fatalf("resolveCodeFreeDefaultCredentialPath() error = %v", err)
	}
	source := filepath.Join(home, ".codefree-o", ".local", "share", codeFreeDefaultCredentialFile)
	writeCodeFreeCredsAtPathForTest(t, source, "272182", "local-api-key", codeFreeDefaultBaseURL)

	creds, err := loadCodeFreeCredentials(context.Background(), codeFreeDefaultBaseURL)
	if err != nil {
		t.Fatalf("loadCodeFreeCredentials() error = %v", err)
	}
	if creds.APIKey != "local-api-key" {
		t.Fatalf("apikey = %q, want local-api-key", creds.APIKey)
	}
	if _, err := os.Stat(primary); err != nil {
		t.Fatalf("expected imported credentials at %q: %v", primary, err)
	}
	stored, err := readCodeFreeStoredCredentialsAtPath(primary)
	if err != nil {
		t.Fatalf("read imported credentials: %v", err)
	}
	if got := stored.Cached.UserID; got != "272182" {
		t.Fatalf("imported userId = %q, want 272182", got)
	}
}

func TestLoadCodeFreeCredentialsImportsEmptySnapshotForRequestedBase(t *testing.T) {
	home := t.TempDir()
	setHomeForCodeFreeTest(t, home)
	t.Setenv(codeFreeCredsPathEnv, "")

	baseURL := "https://dev.srdcloud.cn"
	primary, err := resolveCodeFreeDefaultCredentialPath()
	if err != nil {
		t.Fatalf("resolveCodeFreeDefaultCredentialPath() error = %v", err)
	}
	source := filepath.Join(home, ".codefree-o", ".local", "share", codeFreeDefaultCredentialFile)
	writeCodeFreeCredsAtPathForTest(t, source, "272182", "local-api-key", "")

	creds, err := loadCodeFreeCredentials(context.Background(), baseURL)
	if err != nil {
		t.Fatalf("loadCodeFreeCredentials() error = %v", err)
	}
	if creds.BaseURL != baseURL {
		t.Fatalf("base url = %q, want %q", creds.BaseURL, baseURL)
	}
	stored, err := readCodeFreeStoredCredentialsAtPath(primary)
	if err != nil {
		t.Fatalf("read imported credentials: %v", err)
	}
	if got := stored.Cached.BaseURLSnapshot; got != baseURL {
		t.Fatalf("imported baseUrlSnapshot = %q, want %q", got, baseURL)
	}
	if err := os.Remove(source); err != nil {
		t.Fatalf("remove source credentials: %v", err)
	}
	if _, err := loadCodeFreeCredentials(context.Background(), baseURL); err != nil {
		t.Fatalf("load imported credentials after source removal: %v", err)
	}
}

func setHomeForCodeFreeTest(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	if runtime.GOOS != "windows" {
		return
	}
	t.Setenv("USERPROFILE", home)
	volume := filepath.VolumeName(home)
	if volume == "" {
		return
	}
	t.Setenv("HOMEDRIVE", volume)
	t.Setenv("HOMEPATH", strings.TrimPrefix(home, volume))
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
	if got := seenHeaders.Get("sessionId"); got != "login-session-272182" {
		t.Fatalf("sessionid = %q, want stored login session", got)
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

func TestCodeFreeResponseLooksLikeSSE_DoesNotWaitForLargePeek(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()

	go func() {
		_, _ = writer.Write([]byte("d"))
	}()
	done := make(chan bool, 1)
	go func() {
		resp := &http.Response{Header: http.Header{"Content-Type": []string{"text/event-stream"}}}
		done <- codeFreeResponseLooksLikeSSE(resp, bufio.NewReader(reader))
	}()

	select {
	case got := <-done:
		if !got {
			t.Fatal("codeFreeResponseLooksLikeSSE() = false, want true")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("codeFreeResponseLooksLikeSSE() blocked waiting for more bytes")
	}
}

func TestCodeFreeResponseLooksLikeSSE_RecognizesBufferedSSEAfterWhitespace(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(" \n\tdata: {\"choices\":[]}\n\n"))
	resp := &http.Response{Header: http.Header{"Content-Type": []string{"application/octet-stream"}}}
	if !codeFreeResponseLooksLikeSSE(resp, reader) {
		t.Fatal("codeFreeResponseLooksLikeSSE() = false, want true")
	}
}

func TestCodeFreeResponseLooksLikeSSE_PrefersJSONPrefixOverContentType(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(`{"id":"resp","choices":[]}`))
	resp := &http.Response{Header: http.Header{"Content-Type": []string{"text/event-stream"}}}
	if codeFreeResponseLooksLikeSSE(resp, reader) {
		t.Fatal("codeFreeResponseLooksLikeSSE() = true, want false for JSON body")
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

func TestCodeFreeStreamRetriesHeaderTimeoutBeforeEmission(t *testing.T) {
	credsPath := writeCodeFreeCredsForTest(t, "272182", "76475baf-3659-488a-932d-0971ae103591")
	t.Setenv(codeFreeCredsPathEnv, credsPath)

	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return nil, fmt.Errorf(`Post "https://www.srdcloud.cn/api/acbackend/codechat/v1/completions": net/http: timeout awaiting response headers: %w`, context.DeadlineExceeded)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"id\":\"chunk-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"GLM-5.1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\",\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
					"data: {\"id\":\"chunk-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"GLM-5.1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"stop\"}]}\n\n" +
					"data: [DONE]\n\n")),
			Request: req,
		}, nil
	})}
	llm := newCodeFreeRetryTestModelWithConfig(t, Config{
		Alias:      "codefree/glm-5.1",
		Provider:   "codefree",
		API:        APICodeFree,
		Model:      "GLM-5.1",
		BaseURL:    "https://www.srdcloud.cn",
		HTTPClient: client,
		Timeout:    2 * time.Second,
	}, 2)

	var (
		gotErr error
		final  *model.Response
		resets []*model.AttemptReset
	)
	for event, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
		Stream:   true,
	}) {
		if err != nil {
			gotErr = err
			continue
		}
		if event == nil {
			continue
		}
		if event.AttemptReset != nil {
			resets = append(resets, event.AttemptReset)
		}
		if event.Response != nil && event.TurnComplete {
			final = event.Response
		}
	}
	if gotErr != nil {
		t.Fatalf("stream generate error: %v", gotErr)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if len(resets) != 1 || resets[0].Attempt != 1 || resets[0].MaxRetries != 2 || !resets[0].Retrying {
		t.Fatalf("attempt reset events = %#v, want structured retry reset metadata", resets)
	}
	if final == nil || final.Message.TextContent() != "ok" {
		t.Fatalf("final response = %#v, want ok after retry", final)
	}
}

func TestCodeFreeStreamRetriesIdleTimeoutBetweenSSEEvents(t *testing.T) {
	credsPath := writeCodeFreeCredsForTest(t, "272182", "76475baf-3659-488a-932d-0971ae103591")
	t.Setenv(codeFreeCredsPathEnv, credsPath)

	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			reader, writer := io.Pipe()
			go func() {
				_, _ = fmt.Fprint(writer, "data: {\"id\":\"chunk-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"GLM-5.1\",\"choices\":[]}\n\n")
			}()
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       reader,
				Request:    req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"id\":\"chunk-2\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"GLM-5.1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\",\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
					"data: {\"id\":\"chunk-2\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"GLM-5.1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"stop\"}]}\n\n" +
					"data: [DONE]\n\n")),
			Request: req,
		}, nil
	})}
	llm := newCodeFreeRetryTestModelWithConfig(t, Config{
		Alias:                   "codefree/glm-5.1",
		Provider:                "codefree",
		API:                     APICodeFree,
		Model:                   "GLM-5.1",
		BaseURL:                 "https://www.srdcloud.cn",
		HTTPClient:              client,
		Timeout:                 2 * time.Second,
		StreamFirstEventTimeout: 20 * time.Millisecond,
	}, 2)

	var (
		gotErr error
		final  *model.Response
		resets []*model.AttemptReset
	)
	for event, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
		Stream:   true,
	}) {
		if err != nil {
			gotErr = err
			continue
		}
		if event == nil {
			continue
		}
		if event.AttemptReset != nil {
			resets = append(resets, event.AttemptReset)
		}
		if event.Response != nil && event.TurnComplete {
			final = event.Response
		}
	}
	if gotErr != nil {
		t.Fatalf("stream generate error: %v", gotErr)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if len(resets) != 1 || resets[0].Attempt != 1 || resets[0].MaxRetries != 2 || !resets[0].Retrying {
		t.Fatalf("attempt reset events = %#v, want structured retry reset metadata", resets)
	}
	if final == nil || final.Message.TextContent() != "ok" {
		t.Fatalf("final response = %#v, want ok after retry", final)
	}
}

func newCodeFreeRetryTestModel(t *testing.T, server *providerTestServer, retryMax int) model.LLM {
	t.Helper()
	return newCodeFreeRetryTestModelWithConfig(t, Config{
		Alias:      "codefree/glm-5.1",
		Provider:   "codefree",
		API:        APICodeFree,
		Model:      "GLM-5.1",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, retryMax)
}

func newCodeFreeRetryTestModelWithConfig(t *testing.T, cfg Config, retryMax int) model.LLM {
	t.Helper()
	factory := NewFactory()
	cfg.Retry = model.RetryConfig{
		MaxRetries:          retryMax,
		BaseDelay:           time.Nanosecond,
		MaxDelay:            time.Nanosecond,
		RateLimitMaxRetries: retryMax,
		RateLimitBaseDelay:  time.Nanosecond,
		RateLimitMaxDelay:   time.Nanosecond,
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

func TestCodeFreeLogin_PersistsCodeFreeOCredentials(t *testing.T) {
	oldOpenBrowser := codeFreeOpenBrowser
	defer func() { codeFreeOpenBrowser = oldOpenBrowser }()

	credsPath := filepath.Join(t.TempDir(), codeFreeDefaultCredentialFile)
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
			w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
			_, _ = fmt.Fprint(w, "access_token=oauth-access&token_type=bearer&expires_in=3600&uid=272182&ori_session_id=session-short-123&ori_token=other-short-token")
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
		if got := parsed.Path; got != codeFreeOAuthAuthorizePath {
			t.Fatalf("authorize path = %q, want %q", got, codeFreeOAuthAuthorizePath)
		}
		query := parsed.Query()
		if got := query.Get("redirect_uri"); got != server.URL+codeFreeOAuthRedirectPath {
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
		if callbackURL.Path != "/oauth2callback" {
			t.Fatalf("callback path = %q, want /oauth2callback", callbackURL.Path)
		}
		if got := callbackURL.Query().Get("from"); got != "codefree-o" {
			t.Fatalf("callback from = %q, want codefree-o", got)
		}
		if got := callbackURL.Query().Get("randomCode"); got != "1234" {
			t.Fatalf("callback randomCode = %q, want 1234", got)
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
	if got := stored.Cached.SessionID; got != "session-short-123" {
		t.Fatalf("stored sessionId = %q, want session-short-123", got)
	}
	if got := stored.Cached.UserID; got != "272182" {
		t.Fatalf("stored userId = %q, want 272182", got)
	}
	if got := stored.Cached.BaseURLSnapshot; got != server.URL {
		t.Fatalf("stored baseUrlSnapshot = %q, want %q", got, server.URL)
	}
}

func TestResolveCodeFreeOAuthConfig_DefaultsAuthCodeExchangeToNoneWithoutClientSecret(t *testing.T) {
	cfg, err := resolveCodeFreeOAuthConfig("https://www.srdcloud.cn", nil, filepath.Join(t.TempDir(), codeFreeDefaultCredentialFile), "", "", "")
	if err != nil {
		t.Fatalf("resolveCodeFreeOAuthConfig() error = %v", err)
	}
	if cfg.ClientAuthMethod != CodeFreeClientAuthNone {
		t.Fatalf("client auth method = %q, want %q", cfg.ClientAuthMethod, CodeFreeClientAuthNone)
	}
}

func TestResolveCodeFreeOAuthConfig_UsesCodeFreeOClientIDsByBaseURL(t *testing.T) {
	tests := []struct {
		baseURL string
		want    string
	}{
		{baseURL: "https://dev.srdcloud.cn", want: codeFreeDevOAuthClientID},
		{baseURL: "https://test.srdcloud.cn", want: codeFreeTestOAuthClientID},
		{baseURL: "https://www.srdcloud.cn", want: codeFreeDefaultOAuthClientID},
		{baseURL: "", want: codeFreeDefaultOAuthClientID},
	}
	for _, tt := range tests {
		cfg, err := resolveCodeFreeOAuthConfig(tt.baseURL, nil, filepath.Join(t.TempDir(), codeFreeDefaultCredentialFile), "", "", "")
		if err != nil {
			t.Fatalf("resolveCodeFreeOAuthConfig(%q) error = %v", tt.baseURL, err)
		}
		if cfg.ClientID != tt.want {
			t.Fatalf("resolveCodeFreeOAuthConfig(%q).ClientID = %q, want %q", tt.baseURL, cfg.ClientID, tt.want)
		}
	}
}

func TestCodeFreeLogin_AcceptsLocalCallbackWithoutState(t *testing.T) {
	oldOpenBrowser := codeFreeOpenBrowser
	defer func() { codeFreeOpenBrowser = oldOpenBrowser }()

	credsPath := filepath.Join(t.TempDir(), codeFreeDefaultCredentialFile)
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case codeFreeOAuthTokenPath:
			w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
			_, _ = fmt.Fprint(w, "uid=272182&ori_session_id=session-short-123")
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
		if strings.TrimSpace(parsed.Query().Get("state")) == "" {
			t.Fatal("expected oauth state in auth url")
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
	if result.UserID != "272182" {
		t.Fatalf("user id = %q, want 272182", result.UserID)
	}
}

func TestCodeFreeLogin_AcceptsFormEncodedTokenResponse(t *testing.T) {
	oldOpenBrowser := codeFreeOpenBrowser
	defer func() { codeFreeOpenBrowser = oldOpenBrowser }()

	credsPath := filepath.Join(t.TempDir(), codeFreeDefaultCredentialFile)
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case codeFreeOAuthTokenPath:
			w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
			_, _ = fmt.Fprint(w, "access_token=login-access&token_type=bearer&expires_in=3600&uid=272182&ori_session_id=session-short-123")
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

	codeFreeOpenBrowser = func(string) error { return nil }

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
}

func TestDecodeCodeFreeTokenResponse_AcceptsSnakeCaseFormAliases(t *testing.T) {
	payload, err := decodeCodeFreeTokenResponse([]byte("access_token=login-access&token_type=bearer&expires_in=3600&user_id=272182&session_id=session-short-123"))
	if err != nil {
		t.Fatalf("decodeCodeFreeTokenResponse() error = %v", err)
	}
	if payload.UserID != "272182" {
		t.Fatalf("user id = %q, want 272182", payload.UserID)
	}
	if payload.SessionID != "session-short-123" {
		t.Fatalf("session id = %q, want session-short-123", payload.SessionID)
	}
}

func TestCodeFreeEnsureAuth_SkipsLoginWhenCodeFreeOCredsAlreadyExist(t *testing.T) {
	oldOpenBrowser := codeFreeOpenBrowser
	defer func() { codeFreeOpenBrowser = oldOpenBrowser }()

	credsPath := writeCodeFreeCredsForTest(t, "272182", "cached-api-key")
	var opened bool
	codeFreeOpenBrowser = func(string) error {
		opened = true
		return nil
	}

	_, err := CodeFreeEnsureAuth(context.Background(), CodeFreeEnsureAuthOptions{
		CredentialPath: credsPath,
		OpenBrowser:    true,
	})
	if err != nil {
		t.Fatalf("CodeFreeEnsureAuth() error = %v", err)
	}
	if opened {
		t.Fatal("expected existing codefree-o credentials to skip browser login")
	}
}

func TestCodeFreeEnsureAuth_ImportsCodeFreeOLocalCredentialsBeforeLogin(t *testing.T) {
	oldOpenBrowser := codeFreeOpenBrowser
	defer func() { codeFreeOpenBrowser = oldOpenBrowser }()

	home := t.TempDir()
	setHomeForCodeFreeTest(t, home)
	credsPath, err := resolveCodeFreeDefaultCredentialPath()
	if err != nil {
		t.Fatalf("resolve default credential path: %v", err)
	}
	source := filepath.Join(home, ".codefree-o", ".local", "share", codeFreeDefaultCredentialFile)
	writeCodeFreeCredsAtPathForTest(t, source, "272182", "imported-api-key", codeFreeDefaultBaseURL)
	var opened bool
	codeFreeOpenBrowser = func(string) error {
		opened = true
		return nil
	}

	result, err := CodeFreeEnsureAuth(context.Background(), CodeFreeEnsureAuthOptions{
		OpenBrowser: true,
	})
	if err != nil {
		t.Fatalf("CodeFreeEnsureAuth() error = %v", err)
	}
	if opened {
		t.Fatal("expected imported codefree-o credentials to skip browser login")
	}
	if result.UserID != "272182" {
		t.Fatalf("user id = %q, want 272182", result.UserID)
	}
	stored, err := readCodeFreeStoredCredentialsAtPath(credsPath)
	if err != nil {
		t.Fatalf("read imported credentials: %v", err)
	}
	if got := stored.Cached.UserID; got != "272182" {
		t.Fatalf("imported userId = %q, want 272182", got)
	}
}

func TestCodeFreeEnsureAuth_DoesNotImportWhenCredentialPathIsExplicit(t *testing.T) {
	home := t.TempDir()
	setHomeForCodeFreeTest(t, home)
	source := filepath.Join(home, ".codefree-o", ".local", "share", codeFreeDefaultCredentialFile)
	writeCodeFreeCredsAtPathForTest(t, source, "272182", "imported-api-key", codeFreeDefaultBaseURL)

	credsPath := filepath.Join(t.TempDir(), codeFreeDefaultCredentialFile)
	_, err := loadCodeFreeStoredCredentialsLocked(codeFreeDefaultBaseURL, credsPath)
	if err == nil {
		t.Fatal("loadCodeFreeStoredCredentialsLocked() error = nil, want missing explicit credentials")
	}
	if _, statErr := os.Stat(credsPath); statErr == nil {
		t.Fatalf("explicit credential path was unexpectedly created at %q", credsPath)
	}
}
