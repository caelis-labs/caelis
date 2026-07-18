package providers

import (
	"strings"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func citationSourceFromSearchResult(ref string, result model.WebSearchResult) model.CitationSource {
	return model.CitationSource{
		RefID:       ref,
		Title:       result.Title,
		URL:         result.URL,
		Snippet:     result.Snippet,
		Source:      result.Source,
		PublishedAt: result.PublishedAt,
	}
}

func citationSourcesFromSearchResults(results []model.WebSearchResult) []model.CitationSource {
	out := make([]model.CitationSource, 0, len(results))
	for _, result := range results {
		out = append(out, citationSourceFromSearchResult(result.RefID, result))
	}
	return out
}

// providerCharacterRangeToBytes converts the character offsets used by
// OpenAI-style annotation APIs into the UTF-8 byte offsets owned by model.
// ASCII ranges are unchanged. A byte-offset fallback keeps compatible
// providers that explicitly return bytes usable when the character range is
// outside the text's rune count.
func providerCharacterRangeToBytes(text string, start int, end int) (int, int, bool) {
	if start < 0 || end < start {
		return 0, 0, false
	}
	runeCount := utf8.RuneCountInString(text)
	if end <= runeCount {
		return runeIndexToByteOffset(text, start), runeIndexToByteOffset(text, end), true
	}
	if end <= len(text) && (start == len(text) || utf8.RuneStart(text[start])) && (end == len(text) || utf8.RuneStart(text[end])) {
		return start, end, true
	}
	return 0, 0, false
}

func runeIndexToByteOffset(text string, index int) int {
	if index <= 0 {
		return 0
	}
	if index >= utf8.RuneCountInString(text) {
		return len(text)
	}
	seen := 0
	for byteOffset := range text {
		if seen == index {
			return byteOffset
		}
		seen++
	}
	return len(text)
}

func trimCitedText(text string, citations []model.Citation) (string, []model.Citation) {
	trimmed := strings.TrimSpace(text)
	if trimmed == text {
		return text, model.NormalizeCitations(text, citations)
	}
	if trimmed == "" {
		return "", nil
	}
	leading := strings.Index(text, trimmed)
	if leading < 0 {
		return trimmed, nil
	}
	out := make([]model.Citation, 0, len(citations))
	for _, citation := range citations {
		citation.StartIndex = max(leading, min(leading+len(trimmed), citation.StartIndex)) - leading
		citation.EndIndex = max(leading, min(leading+len(trimmed), citation.EndIndex)) - leading
		out = append(out, citation)
	}
	return trimmed, model.NormalizeCitations(trimmed, out)
}
