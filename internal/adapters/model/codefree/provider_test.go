package codefree

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/model"
)

func TestProviderStreamUsesStoredCredentialsAndParsesResponse(t *testing.T) {
	credsPath := writeCredentials(t, "272182", "live-api-key")
	t.Setenv(credsPathEnv, credsPath)
	t.Setenv(clientVersionEnv, "0.3.6-test")

	var captured chatCompletionRequest
	var headers http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != chatCompletionsPath {
			t.Fatalf("path = %q, want %s", r.URL.Path, chatCompletionsPath)
		}
		headers = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp",
			"model":"GLM-4.7",
			"created":1700000000,
			"choices":[{
				"message":{"role":"assistant","content":"pong"},
				"finish_reason":"stop"
			}],
			"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}
		}`))
	}))
	defer server.Close()

	provider, err := New(Config{
		BaseURL:         server.URL,
		Model:           "GLM-4.7",
		MaxOutputTokens: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := provider.Stream(context.Background(), model.Request{
		Messages: []model.Message{{
			Role:  model.RoleUser,
			Parts: []model.Part{model.NewTextPart("Reply with exactly pong.")},
		}},
		Tools: []model.ToolSpec{model.NewFunctionToolSpec("run_command", "run shell", map[string]any{
			"type": "object",
		})},
		Output: &model.OutputSpec{Mode: model.OutputJSON},
		Stream: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if event.Type != model.StreamTurnDone || event.Response == nil {
		t.Fatalf("event = %#v, want final response", event)
	}
	if got := event.Response.Message.TextContent(); got != "pong" {
		t.Fatalf("assistant text = %q, want pong", got)
	}
	if event.Response.Usage == nil || event.Response.Usage.TotalTokens != 12 {
		t.Fatalf("usage = %#v, want parsed usage", event.Response.Usage)
	}

	if got := headers.Get("Authorization"); got != authorizationValue {
		t.Fatalf("authorization = %q, want %q", got, authorizationValue)
	}
	if got := headers.Get("Userid"); got != "272182" {
		t.Fatalf("userid = %q, want 272182", got)
	}
	if got := headers.Get("Apikey"); got != "live-api-key" {
		t.Fatalf("apikey = %q, want decrypted api key", got)
	}
	if got := headers.Get("Clientversion"); got != "0.3.6-test" {
		t.Fatalf("clientversion = %q", got)
	}
	if headers.Get("Sessionid") == "" {
		t.Fatal("Sessionid header is empty")
	}

	if captured.Model != "GLM-4.7" || captured.Stream {
		t.Fatalf("captured model/stream = %q/%v, want GLM-4.7/false", captured.Model, captured.Stream)
	}
	if captured.MaxTokens != 1024 || captured.Temperature == nil || *captured.Temperature != 0 ||
		captured.TopP == nil || *captured.TopP != 1 {
		t.Fatalf("captured generation controls = %#v", captured)
	}
	if captured.ResponseFormat == nil || captured.ResponseFormat.Type != "json_object" {
		t.Fatalf("response_format = %#v, want json_object", captured.ResponseFormat)
	}
	if len(captured.Tools) != 1 || captured.Tools[0].Function.Name != "run_command" {
		t.Fatalf("tools = %#v", captured.Tools)
	}
}

func TestProviderStreamSendsToolResultMessages(t *testing.T) {
	credsPath := writeCredentials(t, "272182", "live-api-key")
	t.Setenv(credsPathEnv, credsPath)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var captured chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		if len(captured.Messages) != 3 {
			t.Fatalf("messages = %d, want user, assistant tool call, tool result", len(captured.Messages))
		}
		if captured.Messages[1].Role != "assistant" || len(captured.Messages[1].ToolCalls) != 1 {
			t.Fatalf("assistant tool-call message = %#v", captured.Messages[1])
		}
		if captured.Messages[2].Role != "tool" || captured.Messages[2].ToolCallID != "call-1" ||
			captured.Messages[2].Content != "hello" {
			t.Fatalf("tool result message = %#v", captured.Messages[2])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"GLM-4.7",
			"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	provider, err := New(Config{BaseURL: server.URL, Model: "GLM-4.7"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := provider.Stream(context.Background(), model.Request{Messages: []model.Message{
		{Role: model.RoleUser, Parts: []model.Part{model.NewTextPart("run")}},
		{Role: model.RoleAssistant, Parts: []model.Part{{
			Kind: model.PartToolUse,
			ToolUse: &model.ToolCall{
				ID:    "call-1",
				Name:  "run_command",
				Input: json.RawMessage(`{"command":"printf hello"}`),
			},
		}}},
		{Role: model.RoleTool, Parts: []model.Part{{
			Kind: model.PartToolResult,
			ToolResult: &model.ToolResultPart{
				ToolCallID: "call-1",
				Name:       "run_command",
				Content:    []model.Part{model.NewTextPart("hello")},
			},
		}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	event, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if got := event.Response.Message.TextContent(); got != "done" {
		t.Fatalf("assistant text = %q, want done", got)
	}
}

func TestProviderModelsUsesVersionEndpoint(t *testing.T) {
	credsPath := writeCredentials(t, "272182", "live-api-key")
	t.Setenv(credsPathEnv, credsPath)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != versionCheckPath {
			t.Fatalf("path = %q, want %s", r.URL.Path, versionCheckPath)
		}
		if got := r.Header.Get("Apikey"); got != "live-api-key" {
			t.Fatalf("apikey = %q, want decrypted api key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"modelName":"GLM-4.7","modelType":"chat","maxOutputTokens":8192}]}`))
	}))
	defer server.Close()

	provider, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	models, err := provider.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "GLM-4.7" ||
		models[0].ContextWindowTokens != 128000 ||
		models[0].MaxOutputTokens != 8192 ||
		!models[0].SupportsToolCalls {
		t.Fatalf("models = %#v", models)
	}
}

func TestProviderMapsRetCodeBackpressureError(t *testing.T) {
	credsPath := writeCredentials(t, "272182", "live-api-key")
	t.Setenv(credsPathEnv, credsPath)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != chatCompletionsPath {
			t.Fatalf("path = %q, want %s", r.URL.Path, chatCompletionsPath)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"retCode":51,"message":"server overloaded"}`))
	}))
	defer server.Close()

	provider, err := New(Config{BaseURL: server.URL, Model: "GLM-4.7"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Stream(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Parts: []model.Part{model.NewTextPart("hello")}}},
	})
	if err == nil {
		t.Fatal("Stream error = nil, want provider error")
	}
	providerErr, ok := model.ProviderErrorFrom(err)
	if !ok {
		t.Fatalf("error = %T %[1]v, want ProviderError", err)
	}
	if providerErr.Provider != "codefree" ||
		providerErr.Code != "51" ||
		providerErr.Message != "server overloaded" ||
		!providerErr.Backpressure() ||
		!providerErr.Retryable() {
		t.Fatalf("provider error = %#v, want codefree backpressure", providerErr)
	}
}

func writeCredentials(t *testing.T, userID string, apiKey string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "oauth_creds.json")
	raw, err := json.Marshal(map[string]string{
		"id_token": userID,
		"apikey":   encryptAPIKeyForTest(t, apiKey),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func encryptAPIKeyForTest(t *testing.T, apiKey string) string {
	t.Helper()
	block, err := aes.NewCipher([]byte(apiKeyDecryptKey))
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(apiKey)
	padding := block.BlockSize() - len(plain)%block.BlockSize()
	for i := 0; i < padding; i++ {
		plain = append(plain, byte(padding))
	}
	ciphertext := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, []byte(apiKeyDecryptIV)).CryptBlocks(ciphertext, plain)
	return base64.StdEncoding.EncodeToString(ciphertext)
}
