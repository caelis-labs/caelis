package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/google/uuid"
)

const xAIWebSearchMaxOutputTokens = 8192

type xAIWebSearchRequest struct {
	Model           string             `json:"model"`
	Input           string             `json:"input"`
	Tools           []xAIWebSearchTool `json:"tools"`
	Store           bool               `json:"store"`
	Temperature     float64            `json:"temperature"`
	TopP            float64            `json:"top_p"`
	MaxOutputTokens int                `json:"max_output_tokens"`
}

type xAIWebSearchTool struct {
	Type string `json:"type"`
}

// SearchWeb bridges one explicit Caelis WebSearch call to xAI's server-side
// Responses web_search tool. Ordinary Generate calls remain unchanged and do
// not gain implicit network access.
func (l *xAIResponsesLLM) SearchWeb(ctx context.Context, req model.WebSearchRequest) (model.WebSearchResponse, error) {
	req = model.NormalizeWebSearchRequest(req)
	if req.Query == "" {
		return model.WebSearchResponse{}, fmt.Errorf("model: web search query is required")
	}
	if l == nil {
		return model.WebSearchResponse{}, fmt.Errorf("model: xai responses llm is nil")
	}

	temporarySessionID := uuid.NewString()
	payload := xAIWebSearchRequest{
		Model:           strings.TrimSpace(l.name),
		Input:           req.Query,
		Tools:           []xAIWebSearchTool{{Type: "web_search"}},
		Store:           false,
		Temperature:     0.1,
		TopP:            0.95,
		MaxOutputTokens: xAIWebSearchMaxOutputTokens,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return model.WebSearchResponse{}, err
	}
	runCtx, cancel := webSearchRequestContext(ctx, l.requestTimeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(runCtx, http.MethodPost, l.baseURL+"/responses", bytes.NewReader(raw))
	if err != nil {
		return model.WebSearchResponse{}, err
	}
	applyConfiguredHeaders(httpReq, l.headers)
	// Each hosted search is an isolated temporary Responses conversation. It
	// must not join the parent model Session or its prompt-cache affinity.
	applyXAIResponsesHeaders(httpReq, "application/json", l.name, xAIResponsesWireIdentity{
		conversationID: temporarySessionID,
		requestID:      uuid.NewString(),
		sessionID:      temporarySessionID,
	})

	resp, err := l.client.Do(httpReq)
	if err != nil {
		return model.WebSearchResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusMultipleChoices {
		err := statusError(resp)
		if errorcode.Is(err, errorcode.Unauthenticated) || errorcode.Is(err, errorcode.PermissionDenied) {
			err = &xAIResponsesTerminalError{cause: err}
		}
		return model.WebSearchResponse{}, err
	}

	var out openAICodexResponseWire
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return model.WebSearchResponse{}, fmt.Errorf("xai responses: decode web search response: %w", err)
	}
	if out.Error != nil {
		return model.WebSearchResponse{}, xAIWebSearchResponseError(out.Error)
	}
	switch status := strings.ToLower(strings.TrimSpace(out.Status)); status {
	case "", "completed":
	case "failed":
		return model.WebSearchResponse{}, fmt.Errorf("xai responses: web search failed without error details")
	case "incomplete":
		reason := ""
		if out.IncompleteDetails != nil {
			reason = strings.TrimSpace(out.IncompleteDetails.Reason)
		}
		if reason == "" {
			reason = "unknown reason"
		}
		return model.WebSearchResponse{}, fmt.Errorf("xai responses: web search incomplete: %s", reason)
	default:
		return model.WebSearchResponse{}, fmt.Errorf("xai responses: web search returned non-terminal status %q", status)
	}

	accumulator := newOpenAIResponsesAccumulator("xai")
	for index, item := range out.Output {
		accumulator.applyItem(item, index)
	}
	message, err := accumulator.message()
	if err != nil {
		return model.WebSearchResponse{}, err
	}
	rawAnswer := message.TextContent()
	results := xAIWebSearchResults(out.Citations, out.Output)
	answer, citations := trimCitedText(rawAnswer, xAIWebSearchCitations(message.TextContentCitations(), results))
	usage := model.Usage{}
	if out.Usage != nil {
		usage = out.Usage.toKernelUsage()
	}
	return model.WebSearchResponse{
		Query:     req.Query,
		Provider:  l.provider,
		Model:     firstNonEmptyString(out.Model, l.name),
		Answer:    answer,
		Results:   results,
		Citations: citations,
		Usage:     usage,
	}, nil
}

type xAIWebSearchAnnotation struct {
	title string
	url   string
}

func xAIWebSearchResults(citations []string, items []openAICodexOutputItem) []model.WebSearchResult {
	annotations := xAIWebSearchAnnotations(items)
	if len(citations) == 0 {
		results := make([]model.WebSearchResult, 0, len(annotations))
		for _, annotation := range annotations {
			results = append(results, model.WebSearchResult{
				RefID:  fmt.Sprintf("citation-%d", len(results)),
				Title:  annotation.title,
				URL:    annotation.url,
				Source: openAICodexSearchHostname(annotation.url),
			})
		}
		return results
	}

	annotationTitles := make(map[string][]string)
	for _, annotation := range annotations {
		annotationTitles[annotation.url] = append(annotationTitles[annotation.url], annotation.title)
	}
	results := make([]model.WebSearchResult, 0, len(citations))
	for _, citationURL := range citations {
		citationURL = strings.TrimSpace(citationURL)
		if citationURL == "" {
			continue
		}
		title := ""
		if titles := annotationTitles[citationURL]; len(titles) > 0 {
			title = titles[0]
			annotationTitles[citationURL] = titles[1:]
		}
		results = append(results, model.WebSearchResult{
			RefID:  fmt.Sprintf("citation-%d", len(results)),
			Title:  title,
			URL:    citationURL,
			Source: openAICodexSearchHostname(citationURL),
		})
	}
	return results
}

func xAIWebSearchAnnotations(items []openAICodexOutputItem) []xAIWebSearchAnnotation {
	var annotations []xAIWebSearchAnnotation
	for _, item := range items {
		if item.Type != "message" {
			continue
		}
		for _, content := range item.Content {
			if content.Type != "output_text" {
				continue
			}
			for _, annotation := range content.Annotations {
				resultURL := strings.TrimSpace(annotation.URL)
				if annotation.Type != "url_citation" || resultURL == "" {
					continue
				}
				annotations = append(annotations, xAIWebSearchAnnotation{
					title: strings.TrimSpace(annotation.Title),
					url:   resultURL,
				})
			}
		}
	}
	return annotations
}

func xAIWebSearchCitations(citations []model.Citation, results []model.WebSearchResult) []model.Citation {
	refsByURL := make(map[string][]string)
	for _, result := range results {
		resultURL := strings.TrimSpace(result.URL)
		if resultURL == "" {
			continue
		}
		refsByURL[resultURL] = append(refsByURL[resultURL], result.RefID)
	}
	firstRefByURL := make(map[string]string, len(refsByURL))
	for resultURL, refs := range refsByURL {
		if len(refs) > 0 {
			firstRefByURL[resultURL] = refs[0]
		}
	}

	nextFallbackRef := 0
	for citationIndex := range citations {
		for sourceIndex := range citations[citationIndex].Sources {
			source := &citations[citationIndex].Sources[sourceIndex]
			sourceURL := strings.TrimSpace(source.URL)
			if refs := refsByURL[sourceURL]; len(refs) > 0 {
				source.RefID = refs[0]
				refsByURL[sourceURL] = refs[1:]
				continue
			}
			if ref := firstRefByURL[sourceURL]; ref != "" {
				source.RefID = ref
				continue
			}
			source.RefID = fmt.Sprintf("annotation-%d", nextFallbackRef)
			nextFallbackRef++
		}
	}
	return citations
}

func xAIWebSearchResponseError(payload *openAICodexErrorPayload) error {
	event := openAICodexStreamWire{Response: &openAICodexResponseWire{Error: payload}}
	err := xAIResponsesStreamError(event)
	code := strings.ToLower(strings.TrimSpace(payload.Code))
	switch {
	case strings.Contains(code, "permission"),
		strings.Contains(code, "forbidden"),
		strings.Contains(code, "entitle"):
		return &xAIResponsesTerminalError{cause: errorcode.Wrap(errorcode.PermissionDenied, err.Error(), err)}
	case strings.Contains(code, "auth"),
		strings.Contains(code, "unauthorized"),
		strings.Contains(code, "token"):
		return &xAIResponsesTerminalError{cause: errorcode.Wrap(errorcode.Unauthenticated, err.Error(), err)}
	default:
		return err
	}
}

var _ model.WebSearcher = (*xAIResponsesLLM)(nil)
