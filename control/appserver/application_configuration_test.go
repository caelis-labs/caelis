package appserver

import (
	"github.com/caelis-labs/caelis/control/sessionprofile"
	"testing"
)

func TestSessionCreationReceiptReadIgnoresCurrentCatalogValidation(t *testing.T) {
	p := Principal{ID: "owner"}
	profile := applicationTestProfile()
	profile.Execution = "workspace-write"
	profile.Tools = []sessionprofile.ToolDefinition{{Name: "Read", InputSchema: map[string]any{"type": "object"}}}
	req := CreateSessionRequest{WriteBase: WriteBase{OperationID: "original-create"}, Profile: &profile}
	intent, err := commandOperationIntent(p, ActionSessionCreate, req.WriteBase, createSessionTarget(req), req)
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryOperationStore()
	if _, _, err := store.Begin(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	want := CommandResult{OperationID: req.OperationID, SessionID: "existing", Outcome: OutcomeCommitted}
	if _, err := store.Complete(t.Context(), intent, want); err != nil {
		t.Fatal(err)
	}
	if err := sessionprofile.ValidateProfile(profile); err == nil {
		t.Fatal("requires old callback conflicting with new native catalog")
	}
	backend := &applicationBoundaryBackend{}
	commands := newTestCommandService(t, allowAuthorizer{}, store, backend)
	got, err := commands.Operation(t.Context(), p, req.OperationID)
	if err != nil || !sameCommandResult(got, want) || backend.effects != 0 {
		t.Fatalf("receipt=%+v effects=%d err=%v", got, backend.effects, err)
	}
}
