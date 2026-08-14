package chat

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestNormalizeAssistantCitationsScopesRepeatedCodexRefsToNearestSearch(t *testing.T) {
	t.Parallel()

	history := []model.Message{
		webSearchResultMessage("first", []model.WebSearchResult{
			{Title: "June", URL: "https://example.com/june"},
			{Title: "June backup", URL: "https://example.com/june-backup"},
			{Title: "Wrong reused ref", URL: "https://example.com/wrong"},
		}),
		webSearchResultMessage("second", []model.WebSearchResult{
			{Title: "July zero", URL: "https://example.com/july-0"},
			{Title: "July one", URL: "https://example.com/july-1"},
			{Title: "July", URL: "https://example.com/july"},
			{Title: "July backup", URL: "https://example.com/july-backup"},
		}),
	}
	message := model.NewTextMessage(model.RoleAssistant, "上海天气。  \ue200cite\ue202turn0search2\ue202turn0search3\ue201")
	got := normalizeAssistantCitations(message, history)
	if got.TextContent() != "上海天气。" {
		t.Fatalf("text = %q", got.TextContent())
	}
	citations := got.TextContentCitations()
	if len(citations) != 1 || len(citations[0].Sources) != 2 {
		t.Fatalf("citations = %#v", citations)
	}
	if citations[0].Sources[0].URL != "https://example.com/july" || citations[0].Sources[1].URL != "https://example.com/july-backup" {
		t.Fatalf("sources = %#v, want nearest search scope", citations[0].Sources)
	}
}

func TestResolveWebSearchRefsFailsClosedAcrossDifferentResults(t *testing.T) {
	t.Parallel()

	history := []model.Message{
		webSearchResultMessage("one", []model.WebSearchResult{{RefID: "ref-a", URL: "https://example.com/a"}}),
		webSearchResultMessage("two", []model.WebSearchResult{{RefID: "ref-b", URL: "https://example.com/b"}}),
	}
	if got := resolveWebSearchRefs(history, []string{"ref-a", "ref-b"}); got != nil {
		t.Fatalf("resolveWebSearchRefs() = %#v, want no cross-call merge", got)
	}
}

func TestCitationMarkerStreamFilterHandlesSplitMarkers(t *testing.T) {
	t.Parallel()

	var filter citationMarkerStreamFilter
	parts := []string{"rain ", "\ue200ci", "te\ue202turn0", "search2\ue201", " done"}
	visible := ""
	for _, part := range parts {
		visible += filter.Push(part)
	}
	visible += filter.Flush()
	if visible != "rain  done" {
		t.Fatalf("visible = %q", visible)
	}

	var incomplete citationMarkerStreamFilter
	if got := incomplete.Push("answer\ue200cite\ue202turn0"); got != "answer" {
		t.Fatalf("initial visible = %q", got)
	}
	if got := incomplete.Flush(); got != "\ue200cite\ue202turn0" {
		t.Fatalf("flush = %q", got)
	}
}

func webSearchResultMessage(id string, results []model.WebSearchResult) model.Message {
	return model.NewMessage(model.RoleTool, model.NewToolResultJSONPart(id, "WebSearch", map[string]any{
		"status":  "completed",
		"results": results,
	}, false))
}
