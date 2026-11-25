package providers

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
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
	runCtx, cancel := webSearchRequestContext(ctx, l.requestTimeout)
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
		MaxOutputTokens:       512,
		AutomaticContinuation: genai.Ptr(false),
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
	rawAnswer := msg.TextContent()
	results := geminiGroundingResults(out)
	citations := msg.TextContentCitations()
	if len(citations) == 0 && len(results) > 0 {
		citations = []model.Citation{{
			StartIndex: len(rawAnswer),
			EndIndex:   len(rawAnswer),
			Sources:    citationSourcesFromSearchResults(results),
		}}
	}
	answer, citations := trimCitedText(rawAnswer, citations)
	return model.WebSearchResponse{
		Query:     req.Query,
		Provider:  l.provider,
		Model:     firstNonEmptyString(out.ModelVersion, l.name),
		Answer:    answer,
		Results:   results,
		Citations: citations,
		Usage:     usage,
	}, nil
}

func geminiGroundingResults(out *genai.GenerateContentResponse) []model.WebSearchResult {
	if out == nil || len(out.Candidates) == 0 || out.Candidates[0] == nil || out.Candidates[0].GroundingMetadata == nil {
		return nil
	}
	chunks := out.Candidates[0].GroundingMetadata.GroundingChunks
	results := make([]model.WebSearchResult, 0, len(chunks))
	for index, chunk := range chunks {
		if chunk == nil || chunk.Web == nil {
			continue
		}
		url := strings.TrimSpace(chunk.Web.URI)
		if url == "" {
			continue
		}
		results = append(results, model.WebSearchResult{
			RefID:  fmt.Sprintf("grounding-%d", index),
			Title:  strings.TrimSpace(chunk.Web.Title),
			URL:    url,
			Source: strings.TrimSpace(chunk.Web.Domain),
		})
	}
	return results
}

func geminiGroundingMetadata(out *genai.GenerateContentResponse) *genai.GroundingMetadata {
	if out == nil || len(out.Candidates) == 0 || out.Candidates[0] == nil {
		return nil
	}
	return out.Candidates[0].GroundingMetadata
}

func geminiGroundingCitationsForPart(metadata *genai.GroundingMetadata, partIndex int, text string) []model.Citation {
	if metadata == nil {
		return nil
	}
	citations := make([]model.Citation, 0, len(metadata.GroundingSupports))
	for _, support := range metadata.GroundingSupports {
		if support != nil && support.Segment != nil && int(support.Segment.PartIndex) == partIndex {
			citations = append(citations, geminiGroundingCitation(support, metadata.GroundingChunks))
		}
	}
	return model.NormalizeCitations(text, citations)
}

func geminiGroundingCitation(support *genai.GroundingSupport, chunks []*genai.GroundingChunk) model.Citation {
	sources := make([]model.CitationSource, 0, len(support.GroundingChunkIndices))
	for _, rawIndex := range support.GroundingChunkIndices {
		index := int(rawIndex)
		if index < 0 || index >= len(chunks) {
			continue
		}
		chunk := chunks[index]
		if chunk == nil || chunk.Web == nil || strings.TrimSpace(chunk.Web.URI) == "" {
			continue
		}
		sources = append(sources, model.CitationSource{
			RefID:  fmt.Sprintf("grounding-%d", index),
			Title:  strings.TrimSpace(chunk.Web.Title),
			URL:    strings.TrimSpace(chunk.Web.URI),
			Source: strings.TrimSpace(chunk.Web.Domain),
		})
	}
	return model.Citation{
		StartIndex: int(support.Segment.StartIndex),
		EndIndex:   int(support.Segment.EndIndex),
		Sources:    sources,
	}
}
