package providers

import (
	"context"
	"fmt"
	"net/http"
	"testing"
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
