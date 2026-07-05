package agentsdk

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestCloneSourceEventClonesCanonicalOnly(t *testing.T) {
	t.Parallel()

	original := SourceEvent{
		Canonical: &session.Event{ID: "e1", Type: session.EventTypeAssistant, Text: "hi"},
		Native:    map[string]any{"opaque": true},
	}
	cloned := CloneSourceEvent(original)
	if cloned.Canonical == nil || cloned.Canonical.ID != "e1" {
		t.Fatalf("cloned canonical = %#v, want assistant event", cloned.Canonical)
	}
	if cloned.Canonical == original.Canonical {
		t.Fatal("cloned canonical should be a deep copy")
	}
	if cloned.Native != original.Native {
		t.Fatalf("cloned native = %#v, want opaque passthrough by reference", cloned.Native)
	}
}
