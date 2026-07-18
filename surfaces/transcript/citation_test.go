package transcript

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestProjectACPContentChunkParsesCitationsForFutureSurfaces(t *testing.T) {
	t.Parallel()

	text := "上海天气"
	meta := map[string]any{
		"caelis": map[string]any{
			"version": 1,
			"message": map[string]any{
				"citations": []any{map[string]any{
					"start_index": 0,
					"end_index":   len(text),
					"sources": []any{map[string]any{
						"ref_id": "turn0search2",
						"title":  "天气",
						"url":    "https://example.com/weather",
					}},
				}},
			},
		},
	}
	events := ProjectACPEventToEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: text},
			Meta:          meta,
		},
		Final: true,
	}, nil)
	if len(events) != 1 || len(events[0].Citations) != 1 {
		t.Fatalf("events = %#v", events)
	}
	if got := events[0].Citations[0].Sources[0].URL; got != "https://example.com/weather" {
		t.Fatalf("source URL = %q", got)
	}
	markdown := RenderCitationMarkdown(events[0].Text, events[0].Citations)
	if !strings.Contains(markdown, "[1](<https://example.com/weather>)") {
		t.Fatalf("markdown = %q", markdown)
	}
}

func TestCitationsFromMetaRejectsUnsafeURLsAndMidRuneOffsets(t *testing.T) {
	t.Parallel()

	meta := map[string]any{"caelis": map[string]any{"message": map[string]any{"citations": []any{
		map[string]any{"start_index": 1, "end_index": 2, "sources": []any{map[string]any{"url": "https://example.com/mid-rune"}}},
		map[string]any{"start_index": 0, "end_index": len("上海"), "sources": []any{map[string]any{"ref_id": "unsafe", "url": "javascript:alert(1)"}}},
	}}}}
	citations := CitationsFromMeta(meta, "上海")
	if len(citations) != 1 || citations[0].Sources[0].URL != "" || citations[0].Sources[0].RefID != "unsafe" {
		t.Fatalf("citations = %#v", citations)
	}
	if rendered := RenderCitationMarkdown("上海", citations); rendered != "上海" {
		t.Fatalf("rendered = %q, want no unsafe link", rendered)
	}
}
