package gatewayapp

import (
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
