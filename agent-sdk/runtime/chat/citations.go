package chat

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
)

const maxPrivateCitationMarkerBytes = 4096

type citationMarkerStreamFilter struct {
	pending string
}

func (f *citationMarkerStreamFilter) Push(delta string) string {
	if f == nil || delta == "" {
		return delta
	}
	f.pending += delta
	const startToken = "\ue200"
	const endToken = "\ue201"
	var visible strings.Builder
	for f.pending != "" {
		start := strings.Index(f.pending, startToken)
		if start < 0 {
			visible.WriteString(f.pending)
			f.pending = ""
			break
		}
		visible.WriteString(f.pending[:start])
		f.pending = f.pending[start:]
		end := strings.Index(f.pending, endToken)
		if end < 0 {
			if len(f.pending) > maxPrivateCitationMarkerBytes {
				visible.WriteString(startToken)
				f.pending = f.pending[len(startToken):]
				continue
			}
			break
		}
		end += len(endToken)
		marker := f.pending[:end]
		clean, citations := model.ParseCitationMarkers(marker, nil)
		if clean != "" || len(citations) == 0 {
			visible.WriteString(marker)
		}
		f.pending = f.pending[end:]
	}
	return visible.String()
}

func (f *citationMarkerStreamFilter) Flush() string {
	if f == nil {
		return ""
	}
	out := f.pending
	f.pending = ""
	return out
}

// normalizeAssistantCitations resolves provider-private citation markers
// against the nearest preceding WebSearch result. Search reference IDs are
// provider-local and may repeat across calls, so resolution intentionally
// fails closed rather than combining sources from different tool results.
func normalizeAssistantCitations(message model.Message, history []model.Message) model.Message {
	out := model.CloneMessage(message)
	for i := range out.Parts {
		part := out.Parts[i].Text
		if part == nil || !strings.ContainsRune(part.Text, '\ue200') {
			continue
		}
		clean, parsed := model.ParseCitationMarkers(part.Text, func(refs []string) []model.CitationSource {
			return resolveWebSearchRefs(history, refs)
		})
		part.Text = clean
		part.Citations = model.NormalizeCitations(clean, append(part.Citations, parsed...))
	}
	return out
}

type webSearchCitationPayload struct {
	Status  string                  `json:"status"`
	Results []model.WebSearchResult `json:"results"`
}

func resolveWebSearchRefs(history []model.Message, refs []string) []model.CitationSource {
	if len(refs) == 0 {
		return nil
	}
	for messageIndex := len(history) - 1; messageIndex >= 0; messageIndex-- {
		results := history[messageIndex].ToolResults()
		for resultIndex := len(results) - 1; resultIndex >= 0; resultIndex-- {
			result := results[resultIndex]
			if !strings.EqualFold(names.ExecutableOrSelf(result.Name), names.WebSearch) {
				continue
			}
			for contentIndex := len(result.Content) - 1; contentIndex >= 0; contentIndex-- {
				raw := result.Content[contentIndex].JSONValue()
				if len(raw) == 0 {
					continue
				}
				var payload webSearchCitationPayload
				if json.Unmarshal(raw, &payload) != nil || !strings.EqualFold(strings.TrimSpace(payload.Status), "completed") {
					continue
				}
				if sources, ok := citationSourcesFromSearchResults(payload.Results, refs); ok {
					return sources
				}
			}
		}
	}
	return nil
}

func citationSourcesFromSearchResults(results []model.WebSearchResult, refs []string) ([]model.CitationSource, bool) {
	if len(results) == 0 || len(refs) == 0 {
		return nil, false
	}
	byRef := make(map[string]model.WebSearchResult, len(results))
	for _, result := range results {
		if ref := strings.TrimSpace(result.RefID); ref != "" {
			byRef[ref] = result
		}
	}
	out := make([]model.CitationSource, 0, len(refs))
	for _, ref := range refs {
		result, ok := byRef[ref]
		if !ok {
			if len(byRef) > 0 {
				return nil, false
			}
			index, legacy := legacyCodexSearchIndex(ref)
			if !legacy || index < 0 || index >= len(results) {
				return nil, false
			}
			result = results[index]
		}
		if strings.TrimSpace(result.URL) == "" {
			return nil, false
		}
		out = append(out, citationSourceFromWebSearchResult(ref, result))
	}
	return out, true
}

func legacyCodexSearchIndex(ref string) (int, bool) {
	ref = strings.TrimSpace(ref)
	search := strings.LastIndex(ref, "search")
	if search <= 0 || !strings.HasPrefix(ref, "turn") {
		return 0, false
	}
	index, err := strconv.Atoi(ref[search+len("search"):])
	return index, err == nil
}

func citationSourceFromWebSearchResult(ref string, result model.WebSearchResult) model.CitationSource {
	return model.CitationSource{
		RefID:       strings.TrimSpace(ref),
		Title:       result.Title,
		URL:         result.URL,
		Snippet:     result.Snippet,
		Source:      result.Source,
		PublishedAt: result.PublishedAt,
	}
}
