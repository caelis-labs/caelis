package model

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestParseCitationMarkersCreatesInsertionPointCitations(t *testing.T) {
	t.Parallel()

	raw := "上海今天有雨。 \ue200cite\ue202turn0search2\ue202turn0search3\ue201"
	clean, citations := ParseCitationMarkers(raw, func(refs []string) []CitationSource {
		return []CitationSource{
			{RefID: refs[0], Title: "Weather", URL: "https://example.com/weather"},
			{RefID: refs[1], Title: "Forecast", URL: "https://example.com/forecast"},
		}
	})
	if clean != "上海今天有雨。" {
		t.Fatalf("clean = %q", clean)
	}
	if len(citations) != 1 {
		t.Fatalf("citations = %#v", citations)
	}
	if citations[0].StartIndex != len(clean) || citations[0].EndIndex != len(clean) {
		t.Fatalf("citation range = %d:%d, want %d:%d", citations[0].StartIndex, citations[0].EndIndex, len(clean), len(clean))
	}
	if got := citations[0].Sources[1].URL; got != "https://example.com/forecast" {
		t.Fatalf("second URL = %q", got)
	}
}

func TestParseCitationMarkersPreservesMalformedAndTracksUnknownRefs(t *testing.T) {
	t.Parallel()

	malformed := "keep \ue200cite\ue202bad ref\ue201"
	if clean, citations := ParseCitationMarkers(malformed, nil); clean != malformed || citations != nil {
		t.Fatalf("malformed = (%q, %#v)", clean, citations)
	}

	clean, citations := ParseCitationMarkers("answer\ue200cite\ue202turn0search9\ue201", nil)
	if clean != "answer" || len(citations) != 1 || len(citations[0].Sources) != 1 {
		t.Fatalf("unknown = (%q, %#v)", clean, citations)
	}
	if got := citations[0].Sources[0]; got.RefID != "turn0search9" || got.URL != "" {
		t.Fatalf("unknown source = %#v", got)
	}
}

func TestNormalizeCitationsUsesUTF8ByteOffsets(t *testing.T) {
	t.Parallel()

	text := "上海 weather"
	got := NormalizeCitations(text, []Citation{
		{StartIndex: 0, EndIndex: len("上海"), Sources: []CitationSource{{URL: " https://example.com "}}},
		{StartIndex: 1, EndIndex: 2, Sources: []CitationSource{{URL: "https://invalid.example"}}},
	})
	want := []Citation{{
		StartIndex: 0,
		EndIndex:   len("上海"),
		Sources:    []CitationSource{{URL: "https://example.com"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeCitations() = %#v, want %#v", got, want)
	}
}

func TestMessageTextContentCitationsShiftsPartOffsets(t *testing.T) {
	t.Parallel()

	message := NewMessage(RoleAssistant,
		NewTextPartWithCitations("first", []Citation{{StartIndex: 5, EndIndex: 5, Sources: []CitationSource{{URL: "https://first.example"}}}}),
		NewTextPartWithCitations("第二", []Citation{{StartIndex: 0, EndIndex: len("第二"), Sources: []CitationSource{{URL: "https://second.example"}}}}),
	)
	got := message.TextContentCitations()
	if len(got) != 2 {
		t.Fatalf("citations = %#v", got)
	}
	if got[1].StartIndex != len("first\n") || got[1].EndIndex != len("first\n第二") {
		t.Fatalf("second range = %d:%d", got[1].StartIndex, got[1].EndIndex)
	}

	clone := CloneMessage(message)
	clone.Parts[0].Text.Citations[0].Sources[0].URL = "changed"
	if message.Parts[0].Text.Citations[0].Sources[0].URL != "https://first.example" {
		t.Fatal("CloneMessage shared citation source storage")
	}
}

func TestCitationMessageJSONRoundTrip(t *testing.T) {
	t.Parallel()

	want := NewMessage(RoleAssistant, NewTextPartWithCitations("answer", []Citation{{
		StartIndex: len("answer"),
		EndIndex:   len("answer"),
		Sources: []CitationSource{{
			RefID:       "turn0search2",
			Title:       "Source",
			URL:         "https://example.com/source",
			Snippet:     "snippet",
			Source:      "example.com",
			PublishedAt: "2026-07-18",
		}},
	}}))
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var got Message
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
}
