package appserver

import (
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/control/application"
)

func TestApplicationCreationRetryPrecedesCurrentCatalogValidation(t *testing.T) {
	store, _ := applicationTestStore(t)
	principal := enrollTestApplication(t, store, "owner", "catalog-migration", "a")
	scope, _ := ApplicationScope(principal)
	// Read was a valid application callback before native file tools existed.
	// The unchanged creation operation remains recoverable after that expansion.
	profile := applicationTestProfile()
	profile.Execution = "workspace-write"
	profile.Tools = []application.ToolDefinition{{Name: "Read", InputSchema: map[string]any{"type": "object"}}}
	req := CreateApplicationSessionRequest{WriteBase: WriteBase{OperationID: "legacy-create"}, Profile: profile}
	if _, _, err := store.BeginOperation(t.Context(), scope, req.OperationID, req); err != nil {
		t.Fatal(err)
	}
	want := CommandResult{OperationID: req.OperationID, SessionID: "existing", Outcome: OutcomeCommitted}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteOperation(t.Context(), scope, req.OperationID, raw); err != nil {
		t.Fatal(err)
	}
	if err := application.ValidateProfile(profile); err == nil {
		t.Fatal("test requires an old callback conflicting with the current default native set")
	}
	// No command backend is bound: recovering the receipt must never redispatch.
	service := &ApplicationService{config: ApplicationServiceConfig{Store: store}}
	got, err := service.Create(t.Context(), principal, req)
	if err != nil || got.SessionID != want.SessionID || got.Outcome != want.Outcome {
		t.Fatalf("unchanged creation retry = %+v, %v", got, err)
	}
}
