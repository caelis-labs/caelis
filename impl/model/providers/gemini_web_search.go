package providers

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"google.golang.org/genai"
)

func (l *geminiLLM) SearchWeb(ctx context.Context, req model.WebSearchRequest) (model.WebSearchResponse, error) {
	req = model.NormalizeWebSearchRequest(req)
	if req.Query == "" {
		return model.WebSearchResponse{}, fmt.Errorf("model: web search query is required")
	}
	if l == nil {
		return model.WebSearchResponse{}, fmt.Errorf("model: gemini llm is nil")
	}
	runCtx, cancel := context.WithTimeout(ctx, firstPositiveDuration(l.requestTimeout, defaultWebSearchTimeout))
	defer cancel()
	client, err := l.newClient(runCtx)
	if err != nil {
		return model.WebSearchResponse{}, err
	}
	includeServerSideTools := true
	cfg := &genai.GenerateContentConfig{
		Tools: []*genai.Tool{{GoogleSearch: &genai.GoogleSearch{}}},
		ToolConfig: &genai.ToolConfig{
			IncludeServerSideToolInvocations: &includeServerSideTools,
		},
		MaxOutputTokens: 512,
	}
	out, err := client.Models.GenerateContent(runCtx, l.name, []*genai.Content{{
		Role:  "user",
		Parts: []*genai.Part{genai.NewPartFromText(req.Query)},
	}}, cfg)
	if err != nil {
		return model.WebSearchResponse{}, err
	}
	msg, usage, err := geminiResponseToMessage(out)
	if err != nil {
		return model.WebSearchResponse{}, err
	}
	return model.WebSearchResponse{
		Query:    req.Query,
		Provider: l.provider,
		Model:    firstNonEmptyString(out.ModelVersion, l.name),
		Answer:   strings.TrimSpace(msg.TextContent()),
		Results:  geminiGroundingResults(out, req.MaxResults),
		Usage:    usage,
	}, nil
}

func geminiGroundingResults(out *genai.GenerateContentResponse, maxResults int) []model.WebSearchResult {
	if out == nil || len(out.Candidates) == 0 || out.Candidates[0] == nil || out.Candidates[0].GroundingMetadata == nil {
		return nil
	}
	chunks := out.Candidates[0].GroundingMetadata.GroundingChunks
	results := make([]model.WebSearchResult, 0, min(maxResults, len(chunks)))
	seen := map[string]struct{}{}
	for _, chunk := range chunks {
		if chunk == nil || chunk.Web == nil {
			continue
		}
		url := strings.TrimSpace(chunk.Web.URI)
		if url == "" {
			continue
		}
		if _, ok := seen[url]; ok {
			continue
		}
		seen[url] = struct{}{}
		results = append(results, model.WebSearchResult{
			Title:  strings.TrimSpace(chunk.Web.Title),
			URL:    url,
			Source: strings.TrimSpace(chunk.Web.Domain),
		})
		if len(results) >= maxResults {
			break
		}
	}
	return results
}
