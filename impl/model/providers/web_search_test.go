package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/model"
)

func TestGeminiSearchWebUsesGroundingToolAndReturnsSources(t *testing.T) {
	var payload map[string]any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
		  "modelVersion":"gemini-2.5-flash",
		  "candidates":[{
		    "content":{"role":"model","parts":[{"text":"grounded answer"}]},
		    "groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://example.com/a","title":"A","domain":"example.com"}}]}
		  }],
		  "usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":5,"totalTokenCount":9}
		}`)
	}))
	defer server.Close()

	llm := newGemini(Config{
		Provider:   "gemini",
		Model:      "gemini-2.5-flash",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token").(*geminiLLM)
	resp, err := llm.SearchWeb(context.Background(), model.WebSearchRequest{Query: "latest", MaxResults: 3})
	if err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}
	if !payloadHasToolKey(payloadTools(payload), "googleSearch", "google_search") {
		t.Fatalf("payload tools = %#v, want Gemini Google Search tool", payload["tools"])
	}
	if !payloadToolConfigBool(payload, "includeServerSideToolInvocations") {
		t.Fatalf("payload toolConfig = %#v, want server-side invocation opt-in", payload["toolConfig"])
	}
	if resp.Answer != "grounded answer" {
		t.Fatalf("answer = %q, want grounded answer", resp.Answer)
	}
	if len(resp.Results) != 1 || resp.Results[0].URL != "https://example.com/a" {
		t.Fatalf("results = %#v, want grounding source", resp.Results)
	}
}

func TestMimoSearchWebUsesWebSearchToolAndReturnsAnnotations(t *testing.T) {
	var payload map[string]any
	var apiKey string
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		apiKey = r.Header.Get("api-key")
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
		  "model":"mimo-v2-flash",
		  "choices":[{
		    "message":{
		      "role":"assistant",
		      "content":"search answer",
		      "annotations":[{"url":"https://example.com/a","title":"A","summary":"summary","site_name":"Example","publish_time":"2026-06-23"}]
		    },
		    "finish_reason":"stop"
		  }],
		  "usage":{"prompt_tokens":4,"completion_tokens":5,"total_tokens":9}
		}`)
	}))
	defer server.Close()

	llm := newMimo(Config{
		Provider:   "xiaomi",
		Model:      "mimo-v2-flash",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token").(*mimoLLM)
	resp, err := llm.SearchWeb(context.Background(), model.WebSearchRequest{Query: "latest", MaxResults: 2})
	if err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}
	if apiKey != "token" {
		t.Fatalf("api-key = %q, want token", apiKey)
	}
	tools, _ := payload["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools len = %d, want 1: %#v", len(tools), payload["tools"])
	}
	if got := payload["model"]; got != "mimo-v2-flash" {
		t.Fatalf("model = %#v, want selected MiMo session model", got)
	}
	webSearch, _ := tools[0].(map[string]any)
	if got := webSearch["type"]; got != "web_search" {
		t.Fatalf("tool type = %#v, want web_search", got)
	}
	if _, ok := webSearch["webSearchEnabled"]; ok {
		t.Fatalf("web_search tool payload includes undocumented webSearchEnabled flag: %#v", webSearch)
	}
	if resp.Answer != "search answer" {
		t.Fatalf("answer = %q, want search answer", resp.Answer)
	}
	if len(resp.Results) != 1 || resp.Results[0].Snippet != "summary" {
		t.Fatalf("results = %#v, want annotation result", resp.Results)
	}
}

func TestMimoSearchWebUsesSelectedCustomModel(t *testing.T) {
	var payload map[string]any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"future-mimo","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer server.Close()

	llm := newMimo(Config{
		Provider:   "xiaomi",
		Model:      "future-mimo",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token").(*mimoLLM)
	if _, err := llm.SearchWeb(context.Background(), model.WebSearchRequest{Query: "latest"}); err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}
	if got := payload["model"]; got != "future-mimo" {
		t.Fatalf("model = %#v, want selected custom MiMo model", got)
	}
}

func TestMimoTokenPlanDoesNotExposeWebSearcher(t *testing.T) {
	for _, baseURL := range []string{
		"https://token-plan-cn.xiaomimimo.com/v1",
		"https://token-plan-sgp.xiaomimimo.com/v1",
	} {
		t.Run(baseURL, func(t *testing.T) {
			llm := newMimo(Config{
				Provider: "xiaomi",
				Model:    "mimo-v2.5-pro",
				BaseURL:  baseURL,
			}, "token")
			if _, ok := llm.(model.WebSearcher); ok {
				t.Fatal("newMimo() exposes WebSearcher for token-plan endpoint")
			}
			reasoner, ok := llm.(model.WebSearchAvailability)
			if !ok {
				t.Fatal("newMimo() missing web search unavailable reason for token-plan endpoint")
			}
			if reason := reasoner.WebSearchUnavailableReason(); !strings.Contains(reason, "Token Plan") || !strings.Contains(reason, "api.xiaomimimo.com") {
				t.Fatalf("WebSearchUnavailableReason() = %q, want Token Plan/native API guidance", reason)
			}
			providerTools, ok := llm.(interface{ UsesProviderExecutedTools(*model.Request) bool })
			if !ok {
				t.Fatal("newMimo() missing provider tool usage contract")
			}
			req := &model.Request{
				Messages: []model.Message{model.NewTextMessage(model.RoleUser, "weather")},
				Tools: []model.ToolSpec{
					model.NewProviderExecutedToolSpec("xiaomi", mimoProviderWebSearchWireType, map[string]json.RawMessage{
						"type":         json.RawMessage(`"web_search"`),
						"force_search": json.RawMessage(`true`),
					}),
				},
			}
			if providerTools.UsesProviderExecutedTools(req) {
				t.Fatal("UsesProviderExecutedTools() = true, want token-plan endpoint to suppress MiMo web_search")
			}
		})
	}
}

func TestMimoNativeAPIExposesWebSearcher(t *testing.T) {
	llm := newMimo(Config{
		Provider: "xiaomi",
		Model:    "mimo-v2.5-pro",
		BaseURL:  "https://api.xiaomimimo.com/v1",
	}, "token")
	if _, ok := llm.(model.WebSearcher); !ok {
		t.Fatal("newMimo() does not expose WebSearcher for native API endpoint")
	}
}

func TestMimoSearchWebMapsDisabledPluginError(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":{"message":"webSearchEnabled is false"}}`)
	}))
	defer server.Close()

	llm := newMimo(Config{
		Provider:   "xiaomi",
		Model:      "mimo-v2.5-pro",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token").(*mimoLLM)
	_, err := llm.SearchWeb(context.Background(), model.WebSearchRequest{Query: "latest"})
	if err == nil {
		t.Fatal("SearchWeb() error = nil, want plugin unavailable error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "plugin unavailable") {
		t.Fatalf("SearchWeb() error = %q, want plugin unavailable guidance", msg)
	}
	if strings.Contains(msg, "webSearchEnabled") {
		t.Fatalf("SearchWeb() error = %q, should not expose raw provider flag", msg)
	}
}
