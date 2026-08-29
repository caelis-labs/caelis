package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestXAIResponsesSearchWebUsesHostedToolAndPreservesProviderResults(t *testing.T) {
	t.Parallel()

	type wireIdentity struct {
		conversationID string
		requestID      string
		sessionID      string
	}
	var body map[string]any
	var identities []wireIdentity
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %q, want /responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer xai-access" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q, want application/json", got)
		}
		if got := r.Header.Get("x-grok-model-override"); got != "grok-4.5" {
			t.Errorf("x-grok-model-override = %q, want grok-4.5", got)
		}
		if got := r.Header.Get("originator"); got != "" {
			t.Errorf("originator = %q, want empty", got)
		}
		identity := wireIdentity{
			conversationID: r.Header.Get("x-grok-conv-id"),
			requestID:      r.Header.Get("x-grok-req-id"),
			sessionID:      r.Header.Get("x-grok-session-id"),
		}
		if identity.conversationID == "" || identity.conversationID != identity.sessionID || identity.sessionID == "parent-session" {
			t.Errorf("Grok search identity = %#v, want one isolated temporary Session", identity)
		}
		if identity.requestID == "" || identity.requestID == identity.conversationID {
			t.Errorf("x-grok-req-id = %q, want independent request identity", identity.requestID)
		}
		identities = append(identities, identity)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "resp_search",
			"status": "completed",
			"model":  "grok-4.5",
			"citations": []string{
				"https://example.com/a",
				"https://example.org/b",
				"https://example.net/c",
				"https://example.com/a",
			},
			"output": []any{
				map[string]any{
					"type":   "web_search_call",
					"id":     "ws_1",
					"status": "completed",
				},
				map[string]any{
					"type": "message",
					"id":   "msg_1",
					"content": []any{map[string]any{
						"type": "output_text",
						"text": "最新：甲、乙、甲",
						"annotations": []any{
							map[string]any{"type": "url_citation", "url": "https://example.com/a", "title": "A1", "start_index": 3, "end_index": 4},
							map[string]any{"type": "url_citation", "url": "https://example.org/b", "title": "B", "start_index": 5, "end_index": 6},
							map[string]any{"type": "url_citation", "url": "https://example.com/a", "title": "A2", "start_index": 7, "end_index": 8},
						},
					}},
				},
			},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15},
		})
	}))
	defer server.Close()

	client := server.Client()
	client.Transport = xAIResponsesTestAuthTransport{base: client.Transport}
	factory := NewFactory()
	if err := factory.Register(Config{
		Alias: "xai/grok-4.5", Provider: "xai", API: APIXAIResponses, Model: "grok-4.5",
		BaseURL: server.URL, HTTPClient: client, MaxOutputTok: 32768,
		Auth: AuthConfig{Type: AuthOAuthToken},
	}); err != nil {
		t.Fatal(err)
	}
	llm, err := factory.NewByAlias("xai/grok-4.5")
	if err != nil {
		t.Fatal(err)
	}
	searcher, ok := llm.(model.WebSearcher)
	if !ok {
		t.Fatalf("Factory xAI model = %T, want model.WebSearcher", llm)
	}
	ctx := model.WithProviderRequestMetadata(context.Background(), model.ProviderRequestMetadata{SessionAffinity: "parent-session"})
	searchRequest := model.WebSearchRequest{Query: "最新 xAI 文档", MaxResults: 1}
	got, err := searcher.SearchWeb(ctx, searchRequest)
	if err != nil {
		t.Fatalf("first SearchWeb() error = %v", err)
	}
	if _, err := searcher.SearchWeb(ctx, searchRequest); err != nil {
		t.Fatalf("second SearchWeb() error = %v", err)
	}
	if len(identities) != 2 || identities[0].sessionID == identities[1].sessionID || identities[0].requestID == identities[1].requestID {
		t.Fatalf("Grok search identities = %#v, want fresh temporary Session and request IDs", identities)
	}

	if body["model"] != "grok-4.5" || body["input"] != "最新 xAI 文档" {
		t.Fatalf("request model/input = %#v", body)
	}
	if _, ok := body["prompt_cache_key"]; ok {
		t.Fatalf("temporary search request contains prompt_cache_key: %#v", body)
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %#v, want one hosted web_search", body["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if !reflect.DeepEqual(tool, map[string]any{"type": "web_search"}) {
		t.Fatalf("hosted tool = %#v", tool)
	}
	if _, ok := body["stream"]; ok {
		t.Fatalf("search request unexpectedly streams: %#v", body)
	}
	if _, ok := body["max_results"]; ok {
		t.Fatalf("search request unexpectedly limits provider results: %#v", body)
	}
	if body["store"] != false || body["max_output_tokens"] != float64(xAIWebSearchMaxOutputTokens) ||
		body["temperature"] != 0.1 || body["top_p"] != 0.95 {
		t.Fatalf("search request controls = %#v", body)
	}
	wantResults := []model.WebSearchResult{
		{RefID: "citation-0", Title: "A1", URL: "https://example.com/a", Source: "example.com"},
		{RefID: "citation-1", Title: "B", URL: "https://example.org/b", Source: "example.org"},
		{RefID: "citation-2", URL: "https://example.net/c", Source: "example.net"},
		{RefID: "citation-3", Title: "A2", URL: "https://example.com/a", Source: "example.com"},
	}
	if got.Query != "最新 xAI 文档" || got.Provider != "xai" || got.Model != "grok-4.5" ||
		got.Answer != "最新：甲、乙、甲" || !reflect.DeepEqual(got.Results, wantResults) {
		t.Fatalf("SearchWeb() = %#v, want complete provider results %#v", got, wantResults)
	}
	if len(got.Citations) != 3 || got.Citations[0].StartIndex != len("最新：") ||
		got.Citations[0].Sources[0].RefID != "citation-0" ||
		got.Citations[2].Sources[0].RefID != "citation-3" ||
		got.Citations[2].Sources[0].Title != "A2" {
		t.Fatalf("citations = %#v", got.Citations)
	}
	if got.Usage.PromptTokens != 10 || got.Usage.CompletionTokens != 5 || got.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %#v", got.Usage)
	}
}

func TestXAIResponsesSearchWebFallsBackToAnnotationsAcrossMessages(t *testing.T) {
	t.Parallel()

	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_search",
			"status":"completed",
			"model":"grok-4.5",
			"output":[
				{"type":"message","id":"msg_1","content":[
					{"type":"output_text","text":"甲","annotations":[
						{"type":"url_citation","url":"https://example.com/a","title":"A","start_index":0,"end_index":1}
					]}
				]},
				{"type":"message","id":"msg_2","content":[
					{"type":"output_text","text":"乙","annotations":[
						{"type":"url_citation","url":"https://example.org/b","title":"B","start_index":0,"end_index":1}
					]}
				]}
			]
		}`))
	}))
	defer server.Close()

	llm := newXAIResponses(Config{
		Provider: "xai", Model: "grok-4.5", BaseURL: server.URL, HTTPClient: server.Client(),
	})
	got, err := llm.SearchWeb(context.Background(), model.WebSearchRequest{Query: "latest"})
	if err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}
	wantResults := []model.WebSearchResult{
		{RefID: "citation-0", Title: "A", URL: "https://example.com/a", Source: "example.com"},
		{RefID: "citation-1", Title: "B", URL: "https://example.org/b", Source: "example.org"},
	}
	if got.Answer != "甲\n乙" || !reflect.DeepEqual(got.Results, wantResults) {
		t.Fatalf("SearchWeb() = %#v, want annotation fallback %#v", got, wantResults)
	}
	if len(got.Citations) != 2 ||
		got.Citations[0].StartIndex != 0 || got.Citations[0].EndIndex != len("甲") ||
		got.Citations[0].Sources[0].RefID != "citation-0" ||
		got.Citations[1].StartIndex != len("甲\n") || got.Citations[1].EndIndex != len("甲\n乙") ||
		got.Citations[1].Sources[0].RefID != "citation-1" {
		t.Fatalf("citations = %#v", got.Citations)
	}
}

func TestXAIResponsesSearchWebAllowsCompletedEmptyResult(t *testing.T) {
	t.Parallel()

	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_empty","status":"completed","model":"grok-4.5","output":[]}`))
	}))
	defer server.Close()

	llm := newXAIResponses(Config{
		Provider: "xai", Model: "grok-4.5", BaseURL: server.URL, HTTPClient: server.Client(),
	})
	got, err := llm.SearchWeb(context.Background(), model.WebSearchRequest{Query: "no matches"})
	if err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}
	if got.Query != "no matches" || got.Provider != "xai" || got.Model != "grok-4.5" ||
		got.Answer != "" || len(got.Results) != 0 || len(got.Citations) != 0 {
		t.Fatalf("SearchWeb() = %#v, want successful empty result", got)
	}
}

func TestXAIResponsesSearchWebTreatsOAuthDenialsAsTerminal(t *testing.T) {
	t.Parallel()

	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"web search is not entitled"}}`))
	}))
	defer server.Close()

	llm := newXAIResponses(Config{
		Provider: "xai", Model: "grok-4.5", BaseURL: server.URL, HTTPClient: server.Client(),
	})
	_, err := llm.SearchWeb(context.Background(), model.WebSearchRequest{Query: "latest"})
	if err == nil {
		t.Fatal("SearchWeb() error = nil, want terminal permission denial")
	}
	var terminal *xAIResponsesTerminalError
	if !errors.As(err, &terminal) || errorcode.CodeOf(err) != errorcode.PermissionDenied || terminal.Retryable() {
		t.Fatalf("SearchWeb() error = %T %v, want non-retryable permission denial", err, err)
	}
}

func TestXAIResponsesSearchWebTreatsBodyAuthFailureAsTerminal(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_denied",
			"status":"failed",
			"error":{"code":"forbidden","message":"web search is not entitled"}
		}`))
	}))
	defer server.Close()

	inner := newXAIResponses(Config{
		Provider: "xai", Model: "grok-4.5", BaseURL: server.URL, HTTPClient: server.Client(),
	})
	llm := model.WithRetry(inner, model.RetryConfig{
		MaxRetries: 2, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond,
	})
	searcher, ok := llm.(model.WebSearcher)
	if !ok {
		t.Fatalf("WithRetry(xAI) = %T, want model.WebSearcher", llm)
	}
	_, err := searcher.SearchWeb(context.Background(), model.WebSearchRequest{Query: "latest"})
	if err == nil {
		t.Fatal("SearchWeb() error = nil, want terminal permission denial")
	}
	var terminal *xAIResponsesTerminalError
	if !errors.As(err, &terminal) || errorcode.CodeOf(err) != errorcode.PermissionDenied || terminal.Retryable() {
		t.Fatalf("SearchWeb() error = %T %v, want non-retryable permission denial", err, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1 for terminal body error", got)
	}
}
