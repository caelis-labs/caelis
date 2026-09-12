package gatewayapp

import (
	"context"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/control/uipreferences"
)

func TestUIPreferencesPersistAndPreserveProductConfiguration(t *testing.T) {
	root := t.TempDir()
	store := newAppConfigStore(root)
	doc, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	doc.SchemaVersion = 2
	doc.Sandbox.RequestedType = "host"
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	want := uipreferences.Preferences{SubagentLayout: uipreferences.Up, HorizontalRatio: 61, VerticalRatio: 37}
	if err := store.SaveUIPreferences(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	reopened := newAppConfigStore(root)
	got, err := reopened.LoadUIPreferences(t.Context())
	if err != nil || got != want {
		t.Fatalf("reopened=%#v,%v", got, err)
	}
	saved, err := reopened.Load()
	if err != nil || saved.Sandbox.RequestedType != "host" {
		t.Fatalf("product configuration changed=%#v,%v", saved.Sandbox, err)
	}
	want.VerticalRatio = 71
	if err := reopened.SaveUIPreferences(t.Context(), want); err == nil {
		t.Fatal("saved out-of-bounds split ratio")
	}
}

func TestUIPreferencesLegacyAndDefaultHaveNoTheme(t *testing.T) {
	root := t.TempDir()
	store := newAppConfigStore(root)
	got, err := store.LoadUIPreferences(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.Theme != "" {
		t.Fatalf("empty store theme=%q", got.Theme)
	}
	defaults := (uipreferences.Preferences{}).WithDefaults()
	if got.SubagentLayout != defaults.SubagentLayout || got.HorizontalRatio != defaults.HorizontalRatio || got.VerticalRatio != defaults.VerticalRatio {
		t.Fatalf("empty store defaults=%#v", got)
	}
	doc, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	doc.SchemaVersion = 2
	doc.UI = uipreferences.Preferences{SubagentLayout: uipreferences.Left, HorizontalRatio: 40, VerticalRatio: 45}
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	reopened := newAppConfigStore(root)
	got, err = reopened.LoadUIPreferences(t.Context())
	if err != nil || got.Theme != "" || got.SubagentLayout != uipreferences.Left || got.HorizontalRatio != 40 || got.VerticalRatio != 45 {
		t.Fatalf("legacy=%#v,%v", got, err)
	}
}

func TestUIPreferencesPersistSelectedThemeName(t *testing.T) {
	for _, name := range []string{"auto", "catppuccin", "nord"} {
		root := t.TempDir()
		store := seedUIPreferencesStore(t, root)
		if err := store.SaveUIPreferences(t.Context(), uipreferences.Preferences{Theme: name}); err != nil {
			t.Fatal(err)
		}
		reopened := newAppConfigStore(root)
		got, err := reopened.LoadUIPreferences(t.Context())
		if err != nil || got.Theme != name {
			t.Fatalf("theme %q reopen=%#v,%v", name, got, err)
		}
		saved, err := reopened.Load()
		if err != nil || saved.UI.Theme != name || saved.UI.SubagentLayout != "" || saved.UI.HorizontalRatio != 0 || saved.UI.VerticalRatio != 0 {
			t.Fatalf("theme %q persisted extra fields=%#v,%v", name, saved.UI, err)
		}
	}
}

func TestUIPreferencesSparseSavePreservesOtherUIAndProductFields(t *testing.T) {
	root := t.TempDir()
	store := seedUIPreferencesStore(t, root)
	layout := uipreferences.Preferences{SubagentLayout: uipreferences.Right, HorizontalRatio: 62, VerticalRatio: 39}
	if err := store.SaveUIPreferences(t.Context(), layout); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveUIPreferences(t.Context(), uipreferences.Preferences{Theme: "catppuccin"}); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := uipreferences.Preferences{SubagentLayout: uipreferences.Right, HorizontalRatio: 62, VerticalRatio: 39, Theme: "catppuccin"}
	if saved.UI != want {
		t.Fatalf("ui=%#v", saved.UI)
	}
	if saved.Sandbox.RequestedType != "host" {
		t.Fatalf("product configuration changed=%#v", saved.Sandbox)
	}
}

func TestUIPreferencesEmptySaveIsNoOpRevision(t *testing.T) {
	root := t.TempDir()
	store := seedUIPreferencesStore(t, root)
	if err := store.SaveUIPreferences(t.Context(), uipreferences.Preferences{Theme: "auto"}); err != nil {
		t.Fatal(err)
	}
	before, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveUIPreferences(t.Context(), uipreferences.Preferences{}); err != nil {
		t.Fatal(err)
	}
	after, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if after.ConfigurationRevision != before.ConfigurationRevision || after.UI != before.UI {
		t.Fatalf("empty save mutated revision=%d ui=%#v", after.ConfigurationRevision, after.UI)
	}
}

func TestUIPreferencesConcurrentDisjointWrites(t *testing.T) {
	root := t.TempDir()
	seedUIPreferencesStore(t, root)
	start := make(chan struct{})
	var ready, done sync.WaitGroup
	errs := make(chan error, 2)
	ready.Add(2)
	done.Add(2)
	go func() {
		defer done.Done()
		store := newAppConfigStore(root)
		ready.Done()
		<-start
		errs <- store.SaveUIPreferences(context.Background(), uipreferences.Preferences{Theme: "catppuccin"})
	}()
	go func() {
		defer done.Done()
		store := newAppConfigStore(root)
		ready.Done()
		<-start
		errs <- store.SaveUIPreferences(context.Background(), uipreferences.Preferences{SubagentLayout: uipreferences.Left})
	}()
	ready.Wait()
	close(start)
	done.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	saved, err := newAppConfigStore(root).Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.UI.Theme != "catppuccin" || saved.UI.SubagentLayout != uipreferences.Left {
		t.Fatalf("disjoint writes=%#v", saved.UI)
	}
	if saved.Sandbox.RequestedType != "host" {
		t.Fatalf("product configuration changed=%#v", saved.Sandbox)
	}
}

func seedUIPreferencesStore(t *testing.T, root string) *appConfigStore {
	t.Helper()
	store := newAppConfigStore(root)
	doc, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	doc.SchemaVersion = 2
	doc.Sandbox.RequestedType = "host"
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	return store
}
