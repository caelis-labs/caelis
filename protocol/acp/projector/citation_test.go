package projector

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestEventProjectorAddsStructuredCitationsToAgentMessageMeta(t *testing.T) {
	t.Parallel()

	message := model.NewMessage(model.RoleAssistant, model.NewTextPartWithCitations("cited answer", []model.Citation{{
		StartIndex: 0,
		EndIndex:   len("cited"),
		Sources: []model.CitationSource{{
			RefID: "grounding-0",
			Title: "Source",
			URL:   "https://example.com/source",
		}},
	}}))
	updates, err := (EventProjector{}).ProjectEvent(session.CanonicalizeEvent(&session.Event{
		Type:    session.EventTypeAssistant,
		Message: &message,
	}))
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("updates = %#v", updates)
	}
	chunk, ok := updates[0].(ContentChunk)
	if !ok {
		t.Fatalf("update = %T, want ContentChunk", updates[0])
	}
	caelis, _ := chunk.Meta["caelis"].(map[string]any)
	messageMeta, _ := caelis["message"].(map[string]any)
	citations, _ := messageMeta["citations"].([]any)
	if len(citations) != 1 {
		t.Fatalf("citation meta = %#v", chunk.Meta)
	}
	citation, _ := citations[0].(map[string]any)
	if citation["start_index"] != 0 || citation["end_index"] != len("cited") {
		t.Fatalf("citation = %#v", citation)
	}
}
