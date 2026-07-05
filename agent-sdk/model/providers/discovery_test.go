package providers

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/codefreecaps"
)

func TestDiscoverGeminiModels_UsesAPIKeyHeader(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-goog-api-key"); got != "token" {
			t.Fatalf("expected x-goog-api-key header, got %q", got)
		}
		if got := r.URL.Query().Get("key"); got != "" {
			t.Fatalf("did not expect key query param, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"models":[{"name":"models/gemini-2.5-flash","inputTokenLimit":1048576,"outputTokenLimit":65536,"supportedGenerationMethods":["generateContent"]}]}`)
	}))
	defer server.Close()

	got, err := discoverGeminiModels(context.Background(), server.Client(), Config{
		Provider:   "gemini",
		API:        APIGemini,
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Auth: AuthConfig{
			Type:  AuthAPIKey,
			Token: "token",
		},
	}, "token")
	if err != nil {
		t.Fatalf("discoverGeminiModels failed: %v", err)
	}
	if len(got) != 1 || got[0].Name != "gemini-2.5-flash" {
		t.Fatalf("unexpected models: %+v", got)
	}
}

func TestDiscoverOpenRouterModels_ParsesOpenRouterShapeAndConfiguredHeaders(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("expected bearer auth header, got %q", got)
		}
		if got := r.Header.Get("HTTP-Referer"); got != "https://example.com/caelis" {
			t.Fatalf("expected HTTP-Referer header, got %q", got)
		}
		if got := r.Header.Get("X-Title"); got != "caelis" {
			t.Fatalf("expected X-Title header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":[{"id":"openai/gpt-4o-mini","context_length":128000,"top_provider":{"max_completion_tokens":16384},"supported_parameters":["tools","reasoning"],"architecture":{"input_modalities":["text","image"],"output_modalities":["text"]}}]}`)
	}))
	defer server.Close()

	got, err := discoverOpenAIModels(context.Background(), server.Client(), Config{
		Provider:   "openrouter",
		API:        APIOpenRouter,
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Headers: map[string]string{
			"HTTP-Referer": "https://example.com/caelis",
			"X-Title":      "caelis",
		},
		Auth: AuthConfig{
			Type:  AuthAPIKey,
			Token: "token",
		},
	}, "token")
	if err != nil {
		t.Fatalf("discoverOpenAIModels failed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("unexpected models: %+v", got)
	}
	if got[0].Name != "openai/gpt-4o-mini" {
		t.Fatalf("unexpected model name %+v", got[0])
	}
	if got[0].ContextWindowTokens != 128000 {
		t.Fatalf("expected context length 128000, got %+v", got[0])
	}
	if got[0].MaxOutputTokens != 16384 {
		t.Fatalf("expected max output 16384, got %+v", got[0])
	}
	if len(got[0].Capabilities) == 0 {
		t.Fatalf("expected capabilities parsed, got %+v", got[0])
	}
}

func TestCodeFreeModelLimitsMatchCodeFreeODirectory(t *testing.T) {
	tests := []struct {
		model     string
		context   int
		maxOutput int
		image     bool
		known     bool
	}{
		{model: "DeepSeek-V4-Flash-ctyun-oc", context: 112000, maxOutput: 16000, image: false, known: true},
		{model: "deepseek-v4-flash-ctyun-oc", context: 112000, maxOutput: 16000, image: false, known: true},
		{model: "GLM-4.7", context: 80000, maxOutput: 8000, image: false, known: true},
		{model: "GLM-5-ctyun-oc", context: 112000, maxOutput: 16000, image: false, known: true},
		{model: "GLM-5.1", context: 112000, maxOutput: 16000, image: false, known: true},
		{model: "GLM-5.1-ctyun-oc", context: 112000, maxOutput: 16000, image: false, known: true},
		{model: "Qwen3.5-122B-A10B", context: 112000, maxOutput: 16000, image: true, known: true},
		{model: "custom-codefree-model", context: 128000, maxOutput: 8000, image: false, known: false},
	}
	for _, tt := range tests {
		got, ok := codefreecaps.Lookup(tt.model)
		if ok != tt.known {
			t.Fatalf("codefreecaps.Lookup(%q) ok = %v, want %v", tt.model, ok, tt.known)
		}
		contextWindow := codefreecaps.UnknownContextWindowTokens
		maxOutput := codefreecaps.UnknownMaxOutputTokens
		image := false
		if ok {
			contextWindow = got.ContextWindowTokens
			maxOutput = got.MaxOutputTokens
			image = got.SupportsImages
		}
		if contextWindow != tt.context || maxOutput != tt.maxOutput || image != tt.image {
			t.Fatalf("codefreecaps.Lookup(%q) = limits %d/%d image %v, want %d/%d image %v",
				tt.model, contextWindow, maxOutput, image, tt.context, tt.maxOutput, tt.image)
		}
	}
}

func TestDiscoverCodeFreeModelsParsesLimits(t *testing.T) {
	credsPath := writeCodeFreeCredsForTest(t, "272182", "api-key")
	t.Setenv(codeFreeCredsPathEnv, credsPath)

	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("sessionId"); got != "login-session-272182" {
			t.Fatalf("sessionId = %q, want stored login session", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"optResult":0,"data":[{"modelName":"DeepSeek-V4-Flash-ctyun-oc","modelType":"chat","maxTokens":112000,"maxOutputTokens":16000},{"modelName":"GLM-5.1-ctyun-oc","modelType":"chat","maxTokens":112000,"maxOutputTokens":16000},{"modelName":"GLM-5-ctyun-oc","modelType":"chat","maxTokens":112000,"maxOutputTokens":16000},{"modelName":"GLM-4.7","modelType":"chat","maxTokens":80000,"maxOutputTokens":8000},{"modelName":"GLM-5.1","modelType":"chat","maxTokens":112000,"maxOutputTokens":16000},{"modelName":"Qwen3.5-122B-A10B","modelType":"multimodal","maxTokens":112000,"maxOutputTokens":16000}]}`)
	}))
	defer server.Close()

	got, err := discoverCodeFreeModels(context.Background(), server.Client(), Config{
		Provider:   "codefree",
		API:        APICodeFree,
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Model:      "GLM-4.7",
	})
	if err != nil {
		t.Fatalf("discoverCodeFreeModels failed: %v", err)
	}
	byName := map[string]RemoteModel{}
	for _, item := range got {
		byName[item.Name] = item
	}
	for _, tt := range []struct {
		model     string
		context   int
		maxOutput int
		image     bool
	}{
		{model: "DeepSeek-V4-Flash-ctyun-oc", context: 112000, maxOutput: 16000, image: false},
		{model: "GLM-4.7", context: 80000, maxOutput: 8000, image: false},
		{model: "GLM-5-ctyun-oc", context: 112000, maxOutput: 16000, image: false},
		{model: "GLM-5.1", context: 112000, maxOutput: 16000, image: false},
		{model: "GLM-5.1-ctyun-oc", context: 112000, maxOutput: 16000, image: false},
		{model: "Qwen3.5-122B-A10B", context: 112000, maxOutput: 16000, image: true},
	} {
		item, ok := byName[tt.model]
		if !ok {
			t.Fatalf("discoverCodeFreeModels() missing %q in %+v", tt.model, got)
		}
		if item.ContextWindowTokens != tt.context || item.MaxOutputTokens != tt.maxOutput {
			t.Fatalf("discoverCodeFreeModels(%q) limits = %d/%d, want %d/%d",
				tt.model, item.ContextWindowTokens, item.MaxOutputTokens, tt.context, tt.maxOutput)
		}
		if hasImage := containsRemoteCapability(item.Capabilities, "image"); hasImage != tt.image {
			t.Fatalf("discoverCodeFreeModels(%q) image capability = %v in %#v, want %v", tt.model, hasImage, item.Capabilities, tt.image)
		}
	}
}

func containsRemoteCapability(capabilities []string, want string) bool {
	for _, capability := range capabilities {
		if capability == want {
			return true
		}
	}
	return false
}
