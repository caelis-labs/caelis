package providers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

type openAICodexSearchRequest struct {
	ID       string                    `json:"id"`
	Model    string                    `json:"model"`
	Input    string                    `json:"input,omitempty"`
	Commands openAICodexSearchCommands `json:"commands"`
	Settings openAICodexSearchSettings `json:"settings"`
}

type openAICodexSearchCommands struct {
	SearchQuery []openAICodexSearchQuery `json:"search_query"`
}

type openAICodexSearchQuery struct {
	Query string `json:"q"`
}

type openAICodexSearchSettings struct {
	AllowedCallers    []string `json:"allowed_callers"`
	ExternalWebAccess bool     `json:"external_web_access"`
}

type openAICodexSearchResponse struct {
	Output  string            `json:"output"`
	Results []json.RawMessage `json:"results"`
}

type openAICodexSearchResult struct {
	Type        string `json:"type"`
	RefID       string `json:"ref_id"`
	URL         string `json:"url"`
	Link        string `json:"link"`
	Title       string `json:"title"`
	Name        string `json:"name"`
	Snippet     string `json:"snippet"`
	Summary     string `json:"summary"`
	Source      string `json:"source"`
	SiteName    string `json:"site_name"`
	PublishedAt string `json:"published_at"`
	PublishTime string `json:"publish_time"`
}

// SearchWeb uses the standalone search endpoint exposed by the Codex backend.
// The capability remains explicit: ordinary Generate calls do not gain
// provider-executed web access merely because this adapter supports search.
func (l *openAICodexLLM) SearchWeb(ctx context.Context, req model.WebSearchRequest) (model.WebSearchResponse, error) {
	req = model.NormalizeWebSearchRequest(req)
	if req.Query == "" {
		return model.WebSearchResponse{}, fmt.Errorf("model: web search query is required")
	}
	if l == nil {
		return model.WebSearchResponse{}, fmt.Errorf("model: openai codex llm is nil")
	}

	runCtx, cancel := context.WithTimeout(ctx, defaultWebSearchTimeout)
	defer cancel()
	requestAffinity := ""
	if metadata, ok := model.ProviderRequestMetadataFromContext(ctx); ok {
		requestAffinity = openAICodexRequestAffinity(metadata.SessionAffinity)
	}
	payload := openAICodexSearchRequest{
		ID:    openAICodexSearchID(requestAffinity, req.Query),
		Model: strings.TrimSpace(l.name),
		Input: req.Query,
		Commands: openAICodexSearchCommands{SearchQuery: []openAICodexSearchQuery{{
			Query: req.Query,
		}}},
		Settings: openAICodexSearchSettings{
			AllowedCallers:    []string{"direct"},
			ExternalWebAccess: true,
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return model.WebSearchResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(runCtx, http.MethodPost, l.baseURL+"/alpha/search", bytes.NewReader(raw))
	if err != nil {
		return model.WebSearchResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	setHeaderDefault(httpReq.Header, "originator", "caelis")
	if requestAffinity != "" {
		setHeaderDefault(httpReq.Header, "session-id", requestAffinity)
	}
	applyDefaultAttributionHeaders(httpReq, APIOpenAICodex)
	applyConfiguredHeaders(httpReq, l.headers)

	resp, err := l.client.Do(httpReq)
	if err != nil {
		return model.WebSearchResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusMultipleChoices {
		err := statusError(resp)
		if errorcode.Is(err, errorcode.Unauthenticated) || errorcode.Is(err, errorcode.PermissionDenied) {
			err = &openAICodexTerminalError{cause: err}
		}
		return model.WebSearchResponse{}, err
	}
	var out openAICodexSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return model.WebSearchResponse{}, fmt.Errorf("openai codex: decode web search response: %w", err)
	}
	if strings.TrimSpace(out.Output) == "" && len(out.Results) == 0 {
		return model.WebSearchResponse{}, fmt.Errorf("openai codex: empty web search response")
	}
	results := openAICodexSearchResults(out.Results, req.MaxResults)
	answer, citations := model.ParseCitationMarkers(strings.TrimSpace(out.Output), func(refs []string) []model.CitationSource {
		return openAICodexSearchCitationSources(results, refs)
	})
	return model.WebSearchResponse{
		Query:     req.Query,
		Provider:  l.provider,
		Model:     l.name,
		Answer:    answer,
		Results:   results,
		Citations: citations,
	}, nil
}

func openAICodexSearchID(requestAffinity string, query string) string {
	if requestAffinity = strings.TrimSpace(requestAffinity); requestAffinity != "" {
		return requestAffinity
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(query)))
	return fmt.Sprintf("caelis-search-%x", sum[:12])
}

func openAICodexSearchResults(items []json.RawMessage, maxResults int) []model.WebSearchResult {
	if maxResults <= 0 {
		maxResults = model.NormalizeWebSearchRequest(model.WebSearchRequest{}).MaxResults
	}
	results := make([]model.WebSearchResult, 0, min(maxResults, len(items)))
	seen := map[string]struct{}{}
	for _, raw := range items {
		var item openAICodexSearchResult
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}
		resultURL := firstNonEmptyString(item.URL, item.Link)
		if resultURL == "" {
			continue
		}
		seenKey := strings.TrimSpace(item.RefID)
		if seenKey == "" {
			seenKey = resultURL
		}
		if _, ok := seen[seenKey]; ok {
			continue
		}
		seen[seenKey] = struct{}{}
		results = append(results, model.WebSearchResult{
			RefID:       strings.TrimSpace(item.RefID),
			Title:       firstNonEmptyString(item.Title, item.Name),
			URL:         resultURL,
			Snippet:     firstNonEmptyString(item.Snippet, item.Summary),
			Source:      firstNonEmptyString(item.Source, item.SiteName, openAICodexSearchHostname(resultURL)),
			PublishedAt: firstNonEmptyString(item.PublishedAt, item.PublishTime),
		})
		if len(results) >= maxResults {
			break
		}
	}
	return results
}

func openAICodexSearchCitationSources(results []model.WebSearchResult, refs []string) []model.CitationSource {
	byRef := make(map[string]model.WebSearchResult, len(results))
	for _, result := range results {
		if ref := strings.TrimSpace(result.RefID); ref != "" {
			byRef[ref] = result
		}
	}
	out := make([]model.CitationSource, 0, len(refs))
	for _, ref := range refs {
		result, ok := byRef[strings.TrimSpace(ref)]
		if !ok || strings.TrimSpace(result.URL) == "" {
			continue
		}
		out = append(out, citationSourceFromSearchResult(ref, result))
	}
	return out
}

func openAICodexSearchHostname(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
}

var _ model.WebSearcher = (*openAICodexLLM)(nil)
