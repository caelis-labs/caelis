package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/model"
)

func TestDiscoverOpenAIModelsParsesOpenAICompatibleShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		if got := r.Header.Get("X-Test"); got != "yes" {
			t.Fatalf("X-Test = %q, want configured header", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":[
			{"id":"gpt-small","context_window":"128000","max_output_tokens":4096,"supported_parameters":["tools","reasoning_effort"],"architecture":{"input_modalities":["text","image"],"output_modalities":["text"]}},
			{"id":"gpt-small","context_length":256000,"top_provider":{"max_completion_tokens":8192},"capabilities":{"audio":true}},
			{"id":"   "}
		]}`)
	}))
	defer server.Close()

	models, err := DiscoverOpenAIModels(context.Background(), OpenAIConfig{
		BaseURL:    server.URL + "/v1/",
		Token:      "token",
		HTTPClient: server.Client(),
		Headers:    map[string]string{"X-Test": "yes"},
	})
	if err != nil {
		t.Fatalf("DiscoverOpenAIModels() error = %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("models = %#v, want one normalized model", models)
	}
	got := models[0]
	if got.Name != "gpt-small" {
		t.Fatalf("name = %q, want gpt-small", got.Name)
	}
	if got.ContextWindowTokens != 128000 {
		t.Fatalf("context = %d, want first positive context preserved", got.ContextWindowTokens)
	}
	if got.MaxOutputTokens != 4096 {
		t.Fatalf("max output = %d, want first positive max output preserved", got.MaxOutputTokens)
	}
	wantCaps := []string{"audio", "image", "reasoning", "reasoning_effort", "text", "tools"}
	if !reflect.DeepEqual(got.Capabilities, wantCaps) {
		t.Fatalf("capabilities = %#v, want %#v", got.Capabilities, wantCaps)
	}
}

func TestDiscoverOpenAIModelsReturnsContextOverflowError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":{"message":"maximum context length exceeded"}}`)
	}))
	defer server.Close()

	_, err := DiscoverOpenAIModels(context.Background(), OpenAIConfig{
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	if !model.IsContextOverflow(err) {
		t.Fatalf("DiscoverOpenAIModels() error = %v, want context overflow", err)
	}
}

func TestDiscoverOpenAIModelsAppliesRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	start := time.Now()
	_, err := DiscoverOpenAIModels(context.Background(), OpenAIConfig{
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    20 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("DiscoverOpenAIModels() error = nil, want timeout")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("timeout elapsed = %s, want request timeout applied", elapsed)
	}
}

func TestRemoteModelsToModelInfoMapsCatalogCapabilities(t *testing.T) {
	infos := RemoteModelsToModelInfo("openai", []RemoteModel{
		{Name: "gpt-a", ContextWindowTokens: 128000, Capabilities: []string{"tools", "image", "audio"}},
	})
	if len(infos) != 1 {
		t.Fatalf("infos = %#v, want one model info", infos)
	}
	got := infos[0]
	if got.ModelID != "gpt-a" || got.DisplayName != "gpt-a" || got.Provider != "openai" || got.MaxTokens != 128000 {
		t.Fatalf("info = %+v, want catalog fields mapped", got)
	}
	if !got.SupportsTools || !got.SupportsImage || !got.SupportsAudio {
		t.Fatalf("capabilities = tools:%v image:%v audio:%v, want all true", got.SupportsTools, got.SupportsImage, got.SupportsAudio)
	}
}

func TestDiscoverMiniMaxModelsUsesAnthropicCompatibleEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("x-minimax-api-key"); got != "token" {
			t.Fatalf("x-minimax-api-key = %q, want token", got)
		}
		if got := r.Header.Get("anthropic-version"); got == "" {
			t.Fatal("anthropic-version header is empty")
		}
		if got := r.Header.Get("x-extra"); got != "1" {
			t.Fatalf("x-extra = %q, want configured header", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":[{"id":"MiniMax-M2","context_window":1000000,"max_output_tokens":8192,"capabilities":["tools"]}]}`)
	}))
	defer server.Close()

	models, err := DiscoverMiniMaxModels(context.Background(), MiniMaxConfig{
		BaseURL:    server.URL + "/v1",
		Token:      "token",
		HTTPClient: server.Client(),
		Headers:    map[string]string{"x-extra": "1"},
	})
	if err != nil {
		t.Fatalf("DiscoverMiniMaxModels() error = %v", err)
	}
	if len(models) != 1 || models[0].Name != "MiniMax-M2" || models[0].ContextWindowTokens != 1000000 || models[0].MaxOutputTokens != 8192 {
		t.Fatalf("models = %#v, want MiniMax-M2 metadata", models)
	}
}
