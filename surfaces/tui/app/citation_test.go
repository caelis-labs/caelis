package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/surfaces/transcript"
)

func TestFinalAssistantCitationBecomesTUILinkMarkdown(t *testing.T) {
	t.Parallel()

	m := NewModel(Config{})
	m = applyTranscriptEventForTest(t, m, TranscriptEvent{
		Kind:          TranscriptEventNarrative,
		NarrativeKind: TranscriptNarrativeAssistant,
		Scope:         ACPProjectionMain,
		ScopeID:       "session-cited",
		Text:          "answer",
		Final:         true,
		Citations: []transcript.Citation{{
			StartIndex: len("answer"),
			EndIndex:   len("answer"),
			Sources:    []transcript.CitationSource{{Title: "Source", URL: "https://example.com/source"}},
		}},
	})
	block := requireMainACPTurnBlockForTest(t, m)
	if len(block.Events) != 1 || !strings.Contains(block.Events[0].Text, "[1](<https://example.com/source>)") {
		t.Fatalf("events = %#v, want citation Markdown in final assistant text", block.Events)
	}
}
