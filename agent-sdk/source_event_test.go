package agentsdk

import (
	"errors"
	"fmt"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestCloneSourceEventClonesCanonicalOnly(t *testing.T) {
	t.Parallel()

	native := map[string]any{"opaque": true}
	original := SourceEvent{
		Canonical: &session.Event{ID: "e1", Type: session.EventTypeAssistant, Text: "hi"},
		Native:    native,
	}
	cloned := CloneSourceEvent(original)
	if cloned.Canonical == nil || cloned.Canonical.ID != "e1" {
		t.Fatalf("cloned canonical = %#v, want assistant event", cloned.Canonical)
	}
	if cloned.Canonical == original.Canonical {
		t.Fatal("cloned canonical should be a deep copy")
	}
	clonedNative, ok := cloned.Native.(map[string]any)
	if !ok {
		t.Fatalf("cloned native = %T, want opaque map passthrough", cloned.Native)
	}
	native["opaque"] = false
	if got := clonedNative["opaque"]; got != false {
		t.Fatalf("cloned native opaque = %#v, want passthrough by reference", got)
	}
}

func TestAsEventStreamGapClassifiesWrappedGap(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("observer: %w", &EventStreamGapError{Dropped: 7})
	gap, ok := AsEventStreamGap(err)
	if !ok || gap.Dropped != 7 {
		t.Fatalf("AsEventStreamGap() = %#v, %v; want dropped 7", gap, ok)
	}
	if !errors.Is(err, ErrEventStreamGap) {
		t.Fatalf("errors.Is(%v, ErrEventStreamGap) = false", err)
	}
}
