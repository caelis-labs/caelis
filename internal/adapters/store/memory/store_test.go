package memory

import (
	"context"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
)

func TestStoreEventsSkipsTransientByDefault(t *testing.T) {
	ctx := context.Background()
	store := New()
	active, err := store.Create(ctx, session.StartRequest{AppName: "caelis", UserID: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Append(ctx, active.Ref, []session.Event{
		{
			Type:       session.EventAssistant,
			Visibility: session.VisibilityUIOnly,
			Message:    &model.Message{Role: model.RoleAssistant, Parts: []model.Part{model.NewTextPart("stream")}},
		},
		{
			Type:       session.EventAssistant,
			Visibility: session.VisibilityCanonical,
			Message:    &model.Message{Role: model.RoleAssistant, Parts: []model.Part{model.NewTextPart("final")}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.Events(ctx, session.EventQuery{Ref: active.Ref})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(page.Events))
	}
	if got := session.EventText(page.Events[0]); got != "final" {
		t.Fatalf("event text = %q, want final", got)
	}
}
