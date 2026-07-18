package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

const mimoDefaultBaseURL = "https://api.xiaomimimo.com/v1"

func (l *mimoLLM) SearchWeb(ctx context.Context, req model.WebSearchRequest) (model.WebSearchResponse, error) {
	req = model.NormalizeWebSearchRequest(req)
	if req.Query == "" {
		return model.WebSearchResponse{}, fmt.Errorf("model: web search query is required")
	}
	if l == nil || l.openAICompatLLM == nil {
		return model.WebSearchResponse{}, fmt.Errorf("model: llm is nil")
	}
	if !mimoRuntimeProviderMatches(l.provider) {
		return model.WebSearchResponse{}, fmt.Errorf("model: web search is unavailable for provider %q", l.provider)
	}
	return l.searchMimoWeb(ctx, req)
}

func (l *mimoLLM) searchMimoWeb(ctx context.Context, req model.WebSearchRequest) (model.WebSearchResponse, error) {
	searchModel := strings.TrimSpace(l.name)
	if searchModel == "" {
		return model.WebSearchResponse{}, fmt.Errorf("model: xiaomi web search model is required")
	}
	payload := map[string]any{
		"model":                 searchModel,
		"messages":              []map[string]any{{"role": "user", "content": req.Query}},
		"tools":                 []openAICompatTool{mimoProviderWebSearchTool(mimoProviderWebSearchDefaultExtra(req.MaxResults))},
		"max_completion_tokens": 256,
		"temperature":           1.0,
		"top_p":                 0.95,
		"stream":                false,
		"thinking":              map[string]any{"type": "disabled"},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return model.WebSearchResponse{}, err
	}
	runCtx, cancel := context.WithTimeout(ctx, firstPositiveDuration(l.requestTimeout, defaultWebSearchTimeout))
	defer cancel()
	httpReq, err := http.NewRequestWithContext(runCtx, http.MethodPost, mimoChatCompletionsURL(l.baseURL), bytes.NewReader(raw))
	if err != nil {
		return model.WebSearchResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api-key", l.token)
	applyDefaultAttributionHeaders(httpReq, APIMimo)
	applyConfiguredHeaders(httpReq, l.headers)

	resp, err := l.client.Do(httpReq)
	if err != nil {
		return model.WebSearchResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		err := statusError(resp)
		if resp.StatusCode == http.StatusConflict || mimoWebSearchPluginUnavailable(err) {
			return model.WebSearchResponse{}, fmt.Errorf("model: xiaomi web search quota exhausted or networking service plugin unavailable")
		}
		return model.WebSearchResponse{}, err
	}
	var out mimoWebSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return model.WebSearchResponse{}, err
	}
	if len(out.Choices) == 0 {
		return model.WebSearchResponse{}, fmt.Errorf("model: empty xiaomi web search choices")
	}
	choice := out.Choices[0]
	rawAnswer := contentText(choice.Message.Content)
	results := mimoAnnotationResults(choice.Message.Annotations, req.MaxResults)
	answer, citations := trimCitedText(rawAnswer, mimoAnnotationCitations(rawAnswer, choice.Message.Annotations, results))
	return model.WebSearchResponse{
		Query:     req.Query,
		Provider:  l.provider,
		Model:     firstNonEmptyString(out.Model, searchModel),
		Answer:    answer,
		Results:   results,
		Citations: citations,
		Usage:     choice.Usage.toKernelUsageOr(out.Usage.toKernelUsage()),
	}, nil
}

func mimoWebSearchPluginUnavailable(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "websearchenabled") && strings.Contains(lower, "false")
}

type mimoWebSearchResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content     any                 `json:"content"`
			Annotations []mimoWebAnnotation `json:"annotations"`
		} `json:"message"`
		Usage openAICompatUsage `json:"usage"`
	} `json:"choices"`
	Usage openAICompatUsage `json:"usage"`
}

type mimoWebAnnotation struct {
	Type        string `json:"type"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	SiteName    string `json:"site_name"`
	PublishTime string `json:"publish_time"`
	StartIndex  *int   `json:"start_index,omitempty"`
	EndIndex    *int   `json:"end_index,omitempty"`
}

func mimoAnnotationResults(annotations []mimoWebAnnotation, maxResults int) []model.WebSearchResult {
	results := make([]model.WebSearchResult, 0, min(maxResults, len(annotations)))
	for index, annotation := range annotations {
		url := strings.TrimSpace(annotation.URL)
		if url == "" {
			continue
		}
		results = append(results, model.WebSearchResult{
			RefID:       fmt.Sprintf("annotation-%d", index),
			Title:       strings.TrimSpace(annotation.Title),
			URL:         url,
			Snippet:     strings.TrimSpace(annotation.Summary),
			Source:      strings.TrimSpace(annotation.SiteName),
			PublishedAt: strings.TrimSpace(annotation.PublishTime),
		})
		if len(results) >= maxResults {
			break
		}
	}
	return results
}

func mimoAnnotationCitations(answer string, annotations []mimoWebAnnotation, results []model.WebSearchResult) []model.Citation {
	if len(results) == 0 {
		return nil
	}
	byURL := make(map[string]model.WebSearchResult, len(results))
	for _, result := range results {
		byURL[result.URL] = result
	}
	citations := make([]model.Citation, 0, len(annotations))
	unpositioned := make([]model.CitationSource, 0, len(annotations))
	for _, annotation := range annotations {
		result, ok := byURL[strings.TrimSpace(annotation.URL)]
		if !ok {
			continue
		}
		source := citationSourceFromSearchResult(result.RefID, result)
		if annotation.StartIndex != nil && annotation.EndIndex != nil {
			start, end, ok := providerCharacterRangeToBytes(answer, *annotation.StartIndex, *annotation.EndIndex)
			if ok {
				citations = append(citations, model.Citation{
					StartIndex: start,
					EndIndex:   end,
					Sources:    []model.CitationSource{source},
				})
			} else {
				unpositioned = append(unpositioned, source)
			}
		} else {
			unpositioned = append(unpositioned, source)
		}
	}
	if len(unpositioned) > 0 {
		citations = append(citations, model.Citation{
			StartIndex: len(answer),
			EndIndex:   len(answer),
			Sources:    unpositioned,
		})
	}
	return model.NormalizeCitations(answer, citations)
}

func mimoChatCompletionsURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = mimoDefaultBaseURL
	}
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base + "/chat/completions"
}
