package gatewayapp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

func TestHostUnifiedMagpieProfilesSelectAndDeleteByOriginalIdentity(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer fixture" {
			http.Error(w, "fixture authentication required", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"typesafe/jev-latest","kind":"decision"},{"id":"codex/gpt-6-luna","context_window":272000,"max_output_tokens":32768,"reasoning":false,"modalities":{"input":["text"]}}]}`)
	}))
	defer provider.Close()
	stack := newLocalStateTestHost(t)
	principal := appserver.Principal{ID: stack.UserID()}
	revision, err := stack.ControlStatus().ConfigurationRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	connected, err := stack.ConfigurationCommands().ConnectModel(t.Context(), principal, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "mixed-magpie", ExpectedRevision: &revision}, Config: appserver.ConnectConfig{Provider: "magpie", BaseURL: provider.URL + "/v1", Model: "typesafe/jev-latest,codex/gpt-6-luna", APIKey: "fixture"}})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("mixed connect: %#v %v", connected, err)
	}
	doc, err := stack.composition.authorities.store.LoadContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	chatID, judgmentID := "", ""
	for _, cfg := range doc.Models.Configs {
		for _, endpoint := range doc.Models.ProviderEndpoints {
			if endpoint.ID == cfg.ProviderEndpointID {
				cfg = modelconfig.MergeConfigProviderEndpoint(cfg, endpoint)
				break
			}
		}
		if cfg.Provider != "magpie" {
			continue
		}
		profile, ok := modelprofile.Lookup(doc.ModelProfiles, modelprofile.BuildProviderID(cfg.ID))
		if !ok || profile.DisplayName != cfg.Model || profile.Backend.Provider.ModelConfigID != cfg.ID || cfg.CredentialRef == "" || cfg.Token != "" {
			t.Fatalf("public profile or credential identity: %#v %#v", profile, cfg)
		}
		if profile.Judgment {
			judgmentID = cfg.ID
		} else {
			chatID = cfg.ID
		}
	}
	if chatID == "" || judgmentID == "" || chatID == judgmentID {
		t.Fatalf("missing protocol profiles: %#v", doc.ModelProfiles)
	}
	revision = connected.Revision
	used, err := stack.ConfigurationCommands().UseModel(t.Context(), principal, appserver.UseModelRequest{WriteBase: appserver.WriteBase{OperationID: "select-original-model-id", ExpectedRevision: &revision}, Model: chatID})
	if err != nil || used.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("durable model selection: %#v %v", used, err)
	}
	revision = used.Revision
	deleted, err := stack.ConfigurationCommands().DeleteModel(t.Context(), principal, appserver.DeleteModelRequest{WriteBase: appserver.WriteBase{OperationID: "remove-only-judgment", ExpectedRevision: &revision}, Model: judgmentID})
	if err != nil || deleted.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("typed model removal: %#v %v", deleted, err)
	}
	doc, err = stack.composition.authorities.store.LoadContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := modelprofile.Lookup(doc.ModelProfiles, modelprofile.BuildProviderID(judgmentID)); ok {
		t.Fatal("removed judgment profile remains")
	}
	if profile, ok := modelprofile.Lookup(doc.ModelProfiles, modelprofile.BuildProviderID(chatID)); !ok || doc.ModelProfiles.DefaultProfileID != profile.ID {
		t.Fatal("removing judgment changed the main selection")
	}
}
