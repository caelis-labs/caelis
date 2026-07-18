package providers

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestProviderCharacterRangeToBytesNormalizesUnicodeOffsets(t *testing.T) {
	t.Parallel()

	text := "上海天气"
	start, end, ok := providerCharacterRangeToBytes(text, 1, 3)
	if !ok {
		t.Fatal("providerCharacterRangeToBytes() ok = false")
	}
	if got := text[start:end]; got != "海天" {
		t.Fatalf("range = %d:%d (%q), want 海天", start, end, got)
	}
}

func TestTrimCitedTextShiftsAndClampsOffsets(t *testing.T) {
	t.Parallel()

	text, citations := trimCitedText("  cited answer  ", []model.Citation{{
		StartIndex: 2,
		EndIndex:   len("  cited"),
		Sources:    []model.CitationSource{{URL: "https://example.com/claim"}},
	}, {
		StartIndex: len("  cited answer  "),
		EndIndex:   len("  cited answer  "),
		Sources:    []model.CitationSource{{URL: "https://example.com/end"}},
	}})
	if text != "cited answer" || len(citations) != 2 {
		t.Fatalf("trim = (%q, %#v)", text, citations)
	}
	if citations[0].StartIndex != 0 || citations[0].EndIndex != len("cited") {
		t.Fatalf("claim range = %#v", citations[0])
	}
	if citations[1].StartIndex != len(text) || citations[1].EndIndex != len(text) {
		t.Fatalf("end range = %#v", citations[1])
	}
}
