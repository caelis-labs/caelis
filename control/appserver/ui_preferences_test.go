package appserver

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/uipreferences"
)

type fakeUIPreferencesStore struct {
	saved uipreferences.Preferences
}

func (s *fakeUIPreferencesStore) LoadUIPreferences(context.Context) (uipreferences.Preferences, error) {
	return s.saved, nil
}

func (s *fakeUIPreferencesStore) SaveUIPreferences(_ context.Context, p uipreferences.Preferences) error {
	s.saved = p
	return nil
}

func TestUIPreferencesServiceSaveDoesNotApplyDefaults(t *testing.T) {
	store := &fakeUIPreferencesStore{}
	svc := &UIPreferencesService{Store: store}
	principal := Principal{ID: "owner"}
	update := uipreferences.Preferences{Theme: "catppuccin"}
	if err := svc.Save(context.Background(), principal, update); err != nil {
		t.Fatal(err)
	}
	if store.saved != update {
		t.Fatalf("saved=%#v, want sparse %#v", store.saved, update)
	}
	if err := svc.Save(context.Background(), principal, uipreferences.Preferences{HorizontalRatio: 1}); !errorcode.Is(err, errorcode.InvalidArgument) {
		t.Fatalf("invalid ratio=%v", err)
	}
	if store.saved != update {
		t.Fatalf("invalid save mutated store=%#v", store.saved)
	}
}
