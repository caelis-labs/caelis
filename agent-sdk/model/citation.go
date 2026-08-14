package model

import (
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	privateCitationStart     = '\ue200'
	privateCitationEnd       = '\ue201'
	privateCitationSeparator = '\ue202'
)

// CitationSource is one provider-neutral web source. RefID is an optional,
// provider-local correlation token; renderers must use URL, not RefID, when
// creating a user-visible link.
type CitationSource struct {
	RefID       string `json:"ref_id,omitempty"`
	Title       string `json:"title,omitempty"`
	URL         string `json:"url,omitempty"`
	Snippet     string `json:"snippet,omitempty"`
	Source      string `json:"source,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
}

// Citation attributes a UTF-8 byte range in one TextPart to web sources.
// StartIndex is inclusive and EndIndex is exclusive. Equal indices represent
// an insertion-point citation when a provider supplies sources but no exact
// claim range.
type Citation struct {
	StartIndex int              `json:"start_index,omitempty"`
	EndIndex   int              `json:"end_index,omitempty"`
	Sources    []CitationSource `json:"sources,omitempty"`
}

// NewTextPartWithCitations creates a text part with validated, provider-neutral
// citations. Invalid offsets and source-less citations are omitted.
func NewTextPartWithCitations(text string, citations []Citation) Part {
	return Part{
		Kind: PartKindText,
		Text: &TextPart{Text: text, Citations: NormalizeCitations(text, citations)},
	}
}

// NormalizeCitations validates UTF-8 byte ranges, trims source metadata,
// deduplicates sources, and returns citations in document order.
func NormalizeCitations(text string, citations []Citation) []Citation {
	if len(citations) == 0 {
		return nil
	}
	out := make([]Citation, 0, len(citations))
	for _, citation := range citations {
		if !validCitationOffset(text, citation.StartIndex) ||
			!validCitationOffset(text, citation.EndIndex) ||
			citation.EndIndex < citation.StartIndex {
			continue
		}
		sources := normalizeCitationSources(citation.Sources)
		if len(sources) == 0 {
			continue
		}
		out = append(out, Citation{
			StartIndex: citation.StartIndex,
			EndIndex:   citation.EndIndex,
			Sources:    sources,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartIndex != out[j].StartIndex {
			return out[i].StartIndex < out[j].StartIndex
		}
		return out[i].EndIndex < out[j].EndIndex
	})
	return out
}

func validCitationOffset(text string, offset int) bool {
	if offset < 0 || offset > len(text) {
		return false
	}
	return offset == len(text) || utf8.RuneStart(text[offset])
}

func normalizeCitationSources(sources []CitationSource) []CitationSource {
	if len(sources) == 0 {
		return nil
	}
	out := make([]CitationSource, 0, len(sources))
	seen := map[string]struct{}{}
	for _, source := range sources {
		source.RefID = strings.TrimSpace(source.RefID)
		source.Title = strings.TrimSpace(source.Title)
		source.URL = strings.TrimSpace(source.URL)
		source.Snippet = strings.TrimSpace(source.Snippet)
		source.Source = strings.TrimSpace(source.Source)
		source.PublishedAt = strings.TrimSpace(source.PublishedAt)
		if source.RefID == "" && source.URL == "" {
			continue
		}
		key := source.URL
		if key == "" {
			key = "ref:" + source.RefID
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, source)
	}
	return out
}

// TextContentCitations returns citations relative to Message.TextContent().
// TextPart-local offsets are shifted across the newlines inserted between
// non-empty text parts.
func (m Message) TextContentCitations() []Citation {
	offset := 0
	textParts := 0
	out := make([]Citation, 0)
	for _, part := range m.Parts {
		if part.Text == nil || part.Text.Text == "" {
			continue
		}
		if textParts > 0 {
			offset++
		}
		for _, citation := range NormalizeCitations(part.Text.Text, part.Text.Citations) {
			citation.StartIndex += offset
			citation.EndIndex += offset
			out = append(out, citation)
		}
		offset += len(part.Text.Text)
		textParts++
	}
	return out
}

// ParseCitationMarkers removes OpenAI/Codex private-use citation markers and
// turns them into durable citations. The resolver must scope provider-local
// references to the tool result that produced them. Unknown references remain
// non-renderable RefID-only sources so callers never invent a URL.
func ParseCitationMarkers(text string, resolve func([]string) []CitationSource) (string, []Citation) {
	if !strings.ContainsRune(text, privateCitationStart) {
		return text, nil
	}
	startToken := string(privateCitationStart)
	endToken := string(privateCitationEnd)
	remaining := text
	var clean strings.Builder
	citations := make([]Citation, 0)
	for {
		start := strings.Index(remaining, startToken)
		if start < 0 {
			clean.WriteString(remaining)
			break
		}
		prefix := remaining[:start]
		candidate := remaining[start:]
		end := strings.Index(candidate, endToken)
		if end < 0 {
			clean.WriteString(prefix)
			clean.WriteString(candidate)
			break
		}
		end += len(endToken)
		marker := candidate[:end]
		refs, ok := privateCitationRefs(marker)
		if !ok {
			clean.WriteString(prefix)
			clean.WriteString(marker)
		} else {
			next := candidate[end:]
			if next == "" || strings.HasPrefix(next, "\n") || strings.HasPrefix(next, "\r") {
				prefix = strings.TrimRight(prefix, " \t")
			}
			clean.WriteString(prefix)
			sources := []CitationSource(nil)
			if resolve != nil {
				sources = resolve(append([]string(nil), refs...))
			}
			sources = citationSourcesForRefs(refs, sources)
			citations = append(citations, Citation{
				StartIndex: clean.Len(),
				EndIndex:   clean.Len(),
				Sources:    sources,
			})
		}
		remaining = candidate[end:]
	}
	cleanText := clean.String()
	return cleanText, NormalizeCitations(cleanText, citations)
}

func citationSourcesForRefs(refs []string, resolved []CitationSource) []CitationSource {
	out := make([]CitationSource, 0, len(refs))
	used := make([]bool, len(resolved))
	for _, ref := range refs {
		matched := false
		for i, source := range resolved {
			if used[i] || strings.TrimSpace(source.RefID) != ref {
				continue
			}
			used[i] = true
			out = append(out, source)
			matched = true
			break
		}
		if !matched {
			out = append(out, CitationSource{RefID: ref})
		}
	}
	for i, source := range resolved {
		if !used[i] {
			out = append(out, source)
		}
	}
	return normalizeCitationSources(out)
}

func privateCitationRefs(marker string) ([]string, bool) {
	start := string(privateCitationStart)
	end := string(privateCitationEnd)
	separator := string(privateCitationSeparator)
	if !strings.HasPrefix(marker, start+"cite"+separator) || !strings.HasSuffix(marker, end) {
		return nil, false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(marker, start+"cite"+separator), end)
	parts := strings.Split(body, separator)
	if len(parts) == 0 {
		return nil, false
	}
	refs := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if !validPrivateCitationRef(part) {
			return nil, false
		}
		refs = append(refs, part)
	}
	return refs, true
}

func validPrivateCitationRef(ref string) bool {
	if ref == "" || len(ref) > 256 {
		return false
	}
	for _, r := range ref {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.', r == ':':
		default:
			return false
		}
	}
	return true
}
