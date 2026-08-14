package transcript

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"
)

// CitationSource is one web source projected through
// _meta.caelis.message.citations. RefID is correlation-only and is never a
// user-visible link target.
type CitationSource struct {
	RefID       string `json:"ref_id,omitempty"`
	Title       string `json:"title,omitempty"`
	URL         string `json:"url,omitempty"`
	Snippet     string `json:"snippet,omitempty"`
	Source      string `json:"source,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
}

// Citation uses UTF-8 byte offsets into Event.Text.
type Citation struct {
	StartIndex int              `json:"start_index,omitempty"`
	EndIndex   int              `json:"end_index,omitempty"`
	Sources    []CitationSource `json:"sources,omitempty"`
}

// CitationsFromMeta parses the versioned ACP citation extension. Invalid
// ranges and non-web URLs are ignored so presentation code fails closed.
func CitationsFromMeta(meta map[string]any, text string) []Citation {
	if len(meta) == 0 {
		return nil
	}
	caelis, _ := meta["caelis"].(map[string]any)
	message, _ := caelis["message"].(map[string]any)
	raw, ok := message["citations"]
	if !ok {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var citations []Citation
	if json.Unmarshal(encoded, &citations) != nil {
		return nil
	}
	out := make([]Citation, 0, len(citations))
	for _, citation := range citations {
		if citation.StartIndex < 0 || citation.EndIndex < citation.StartIndex || citation.EndIndex > len(text) ||
			!citationByteBoundary(text, citation.StartIndex) || !citationByteBoundary(text, citation.EndIndex) {
			continue
		}
		sources := make([]CitationSource, 0, len(citation.Sources))
		seen := map[string]struct{}{}
		for _, source := range citation.Sources {
			source.RefID = strings.TrimSpace(source.RefID)
			source.Title = strings.TrimSpace(source.Title)
			source.URL = strings.TrimSpace(source.URL)
			source.Snippet = strings.TrimSpace(source.Snippet)
			source.Source = strings.TrimSpace(source.Source)
			source.PublishedAt = strings.TrimSpace(source.PublishedAt)
			if source.URL != "" && !safeWebCitationURL(source.URL) {
				source.URL = ""
			}
			key := source.URL
			if key == "" {
				key = "ref:" + source.RefID
			}
			if key == "ref:" {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			sources = append(sources, source)
		}
		if len(sources) > 0 {
			citation.Sources = sources
			out = append(out, citation)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].EndIndex != out[j].EndIndex {
			return out[i].EndIndex < out[j].EndIndex
		}
		return out[i].StartIndex < out[j].StartIndex
	})
	return out
}

func citationByteBoundary(text string, index int) bool {
	return index == len(text) || (index >= 0 && index < len(text) && utf8.RuneStart(text[index]))
}

func safeWebCitationURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "https") || strings.EqualFold(parsed.Scheme, "http")
}

// RenderCitationMarkdown materializes structured citations as numbered,
// clickable Markdown links for text-oriented surfaces. GUI surfaces should use
// Citation directly so they can render richer source affordances.
func RenderCitationMarkdown(text string, citations []Citation) string {
	if text == "" || len(citations) == 0 {
		return text
	}
	citations = normalizeRenderableCitations(text, citations)
	if len(citations) == 0 {
		return text
	}
	numbers := map[string]int{}
	nextNumber := 1
	insertions := map[int][]string{}
	for _, citation := range citations {
		for _, source := range citation.Sources {
			if !safeWebCitationURL(source.URL) {
				continue
			}
			number, ok := numbers[source.URL]
			if !ok {
				number = nextNumber
				nextNumber++
				numbers[source.URL] = number
			}
			link := fmt.Sprintf("[%d](<%s>)", number, strings.ReplaceAll(source.URL, ">", "%3E"))
			if !containsString(insertions[citation.EndIndex], link) {
				insertions[citation.EndIndex] = append(insertions[citation.EndIndex], link)
			}
		}
	}
	positions := make([]int, 0, len(insertions))
	for position := range insertions {
		positions = append(positions, position)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(positions)))
	out := text
	for _, position := range positions {
		links := strings.Join(insertions[position], " ")
		if links == "" {
			continue
		}
		prefix := " "
		if position > 0 {
			before, _ := utf8.DecodeLastRuneInString(out[:position])
			if before == ' ' || before == '\t' {
				prefix = ""
			}
		}
		out = out[:position] + prefix + links + out[position:]
	}
	return out
}

func normalizeRenderableCitations(text string, citations []Citation) []Citation {
	meta := map[string]any{"caelis": map[string]any{"message": map[string]any{"citations": citations}}}
	return CitationsFromMeta(meta, text)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
