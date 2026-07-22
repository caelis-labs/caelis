package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestOpenAICodexSearchWebUsesStandaloneSearchEndpoint(t *testing.T) {
	t.Parallel()

	var body openAICodexSearchRequest
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/alpha/search" {
			t.Errorf("path = %q, want /alpha/search", r.URL.Path)
		}
		if got := r.Header.Get("originator"); got != "caelis" {
			t.Errorf("originator = %q, want caelis", got)
		}
		if got := r.Header.Get("session-id"); got != "search-session" {
			t.Errorf("session-id = %q, want search-session", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": "Current answer with cited sources.  \ue200cite\ue202turn0search0\ue202turn0search1\ue201",
			"results": []any{
				map[string]any{
					"type": "text_result", "ref_id": "turn0search0",
					"url": "https://www.example.com/first", "title": "First", "snippet": "First snippet",
				},
				map[string]any{
					"type": "text_result", "ref_id": "turn0search1",
					"url": "https://docs.example.org/second", "title": "Second", "site_name": "Example Docs",
				},
				map[string]any{
					"type": "text_result", "ref_id": "turn0search2",
					"url": "https://www.example.com/first", "title": "Duplicate",
				},
			},
		})
	}))
	defer server.Close()

	inner := newOpenAICodex(Config{
		Provider:   "openai-codex",
		Model:      "gpt-5.4",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	llm := model.WithRetry(inner, model.RetryConfig{})
	searcher, ok := llm.(model.WebSearcher)
	if !ok {
		t.Fatal("WithRetry(openAICodexLLM) did not preserve WebSearcher")
	}
	ctx := model.WithProviderRequestMetadata(context.Background(), model.ProviderRequestMetadata{SessionAffinity: "search-session"})
	got, err := searcher.SearchWeb(ctx, model.WebSearchRequest{Query: "latest Caelis news", MaxResults: 2})
	if err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}

	if body.ID != "search-session" || body.Model != "gpt-5.4" || body.Input != "latest Caelis news" {
		t.Fatalf("request identity/input = %#v", body)
	}
	if !reflect.DeepEqual(body.Commands.SearchQuery, []openAICodexSearchQuery{{Query: "latest Caelis news"}}) {
		t.Fatalf("search_query = %#v", body.Commands.SearchQuery)
	}
	if !body.Settings.ExternalWebAccess || !reflect.DeepEqual(body.Settings.AllowedCallers, []string{"direct"}) {
		t.Fatalf("settings = %#v, want direct live search", body.Settings)
	}
	if got.Query != "latest Caelis news" || got.Provider != "openai-codex" || got.Model != "gpt-5.4" || got.Answer != "Current answer with cited sources." {
		t.Fatalf("SearchWeb() = %#v", got)
	}
	wantResults := []model.WebSearchResult{
		{RefID: "turn0search0", Title: "First", URL: "https://www.example.com/first", Snippet: "First snippet", Source: "example.com"},
		{RefID: "turn0search1", Title: "Second", URL: "https://docs.example.org/second", Source: "Example Docs"},
	}
	if !reflect.DeepEqual(got.Results, wantResults) {
		t.Fatalf("results = %#v, want %#v", got.Results, wantResults)
	}
	if len(got.Citations) != 1 || got.Citations[0].StartIndex != len(got.Answer) || len(got.Citations[0].Sources) != 2 {
		t.Fatalf("citations = %#v, want two sources at answer end", got.Citations)
	}
}

func TestOpenAICodexSearchWebValidatesQuery(t *testing.T) {
	t.Parallel()

	llm := newOpenAICodex(Config{Provider: "openai-codex", Model: "gpt-5.4"})
	if _, err := llm.SearchWeb(context.Background(), model.WebSearchRequest{Query: "  "}); err == nil {
		t.Fatal("SearchWeb() error = nil, want missing query error")
	}
}
