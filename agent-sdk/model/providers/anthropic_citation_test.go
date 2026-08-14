package providers

import (
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
)

func TestAnthropicTextCitationsMapCitedTextRange(t *testing.T) {
	t.Parallel()

	text := "The current release is 2.0."
	citations := anthropicTextCitations(text, []anthropic.TextCitationUnion{{
		Type:      "web_search_result_location",
		CitedText: "release is 2.0",
		Title:     "Release notes",
		URL:       "https://example.com/releases/2.0",
	}})
	if len(citations) != 1 {
		t.Fatalf("citations = %#v", citations)
	}
	if got := text[citations[0].StartIndex:citations[0].EndIndex]; got != "release is 2.0" {
		t.Fatalf("cited range = %q", got)
	}
	if citations[0].Sources[0].Source != "example.com" {
		t.Fatalf("source = %#v", citations[0].Sources[0])
	}
}
