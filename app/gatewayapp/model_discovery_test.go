package gatewayapp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

func TestHostMagpieDiscoveryAndConnectShareRetainedCredential(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer fixture-gateway-key" {
			http.Error(w, "invalid fixture discovery", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"vendor/a","context_window":200000,"max_output_tokens":32000,"reasoning":false,"modalities":{"input":["text"]}},{"id":"vendor/b","context_window":200000,"max_output_tokens":32000,"reasoning":false,"modalities":{"input":["text"]}}]}`)
	}))
	defer provider.Close()
	stack := newLocalStateTestHost(t)
	principal := appserver.Principal{ID: stack.UserID()}
	revision, err := stack.ControlStatus().ConfigurationRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	first, err := stack.ConfigurationCommands().ConnectModel(t.Context(), principal, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "magpie-first", ExpectedRevision: &revision}, Config: appserver.ConnectConfig{Provider: "magpie", BaseURL: provider.URL + "/v1", Model: "vendor/a", APIKey: "fixture-gateway-key"}})
	if err != nil || first.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("first connect: %+v %v", first, err)
	}
	service := ModelService{composition: &stack.composition}
	models, err := service.ConnectionModels(t.Context(), modelconfig.ConnectRequest{Provider: "magpie", BaseURL: provider.URL + "/v1"})
	if err != nil || len(models) != 2 || models[0].Defaults.ContextWindowTokens != 200000 {
		t.Fatalf("retained discovery: %#v %v", models, err)
	}
	// A credential retained for another protocol is not silently borrowed.
	if _, err := service.ConnectionModels(t.Context(), modelconfig.ConnectRequest{Provider: "magpie-judgment", BaseURL: provider.URL + "/v1"}); err == nil {
		t.Fatal("judgment discovery borrowed a chat credential")
	}
	second, err := stack.ConfigurationCommands().ConnectModel(t.Context(), principal, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "magpie-second", ExpectedRevision: &first.Revision}, Config: appserver.ConnectConfig{Provider: "magpie", BaseURL: provider.URL + "/v1", Model: "vendor/b"}})
	if err != nil || second.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("reused connect: %+v %v", second, err)
	}
	var ref string
	for _, choice := range stack.composition.lookup.ListModelChoices() {
		if choice.Provider != "magpie" {
			continue
		}
		cfg, ok := stack.composition.lookup.Config(choice.ID)
		if !ok || cfg.AuthType != model.AuthAPIKey || cfg.CredentialRef == "" || cfg.Token != "" || cfg.ContextWindowTokens != 200000 {
			t.Fatal("accepted gateway configuration was not retained")
		}
		if ref != "" && ref != cfg.CredentialRef {
			t.Fatal("reconnect duplicated the endpoint credential")
		}
		ref = cfg.CredentialRef
	}
	if ref == "" {
		t.Fatal("no connected gateway model")
	}
}

func TestHostMagpieJudgmentGatewaysKeepSeparateAddressesAndCredentials(t *testing.T) {
	newGateway := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/models" || r.Header.Get("X-Magpie-Deciders") != "1" || r.Header.Get("Authorization") != "Bearer fixture-key" {
				http.Error(w, "wrong discovery scope", http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"data":[{"id":"typesafe/jev-a","kind":"decision"},{"id":"typesafe/jev-b","kind":"decision"}]}`)
		}))
	}
	a, b := newGateway(), newGateway()
	defer a.Close()
	defer b.Close()
	stack := newLocalStateTestHost(t)
	principal := appserver.Principal{ID: stack.UserID()}
	revision, err := stack.ControlStatus().ConfigurationRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var first ModelConfig
	for i, gateway := range []*httptest.Server{a, b} {
		result, err := stack.ConfigurationCommands().ConnectModel(t.Context(), principal, appserver.ConnectModelRequest{
			WriteBase: appserver.WriteBase{OperationID: fmt.Sprintf("judgment-gateway-%d", i), ExpectedRevision: &revision},
			Config:    appserver.ConnectConfig{Provider: "magpie", EndpointID: "judgment", BaseURL: gateway.URL + "/v1", Model: "typesafe/jev-a", APIKey: "fixture-key"},
		})
		if err != nil || result.Outcome != appserver.OutcomeCommitted {
			t.Fatalf("connect gateway %d: %+v %v", i, result, err)
		}
		revision = result.Revision
		id := retainedProviderEndpointID("magpie-judgment", "", gateway.URL+"/v1")
		cfg, err := stack.composition.lookup.ResolveConfig(modelconfig.BuildModelID(id, "magpie/typesafe/jev-a"))
		if err != nil || cfg.API != modelconfig.APISystemOne || cfg.BaseURL != gateway.URL+"/v1" {
			t.Fatalf("resolved gateway %d: %#v %v", i, cfg, err)
		}
		if i == 0 {
			first = cfg
		} else if cfg.ProviderEndpointID == first.ProviderEndpointID || cfg.CredentialRef == first.CredentialRef {
			t.Fatal("different gateways shared endpoint or credential identity")
		}
	}
	current, err := stack.composition.lookup.ResolveConfig(first.ID)
	if err != nil || current.BaseURL != first.BaseURL || current.CredentialRef != first.CredentialRef {
		t.Fatalf("second gateway rewrote first connection: %#v %v", current, err)
	}
	service := ModelService{composition: &stack.composition}
	for _, provider := range []string{"magpie", "magpie-judgment"} {
		models, err := service.ConnectionModels(t.Context(), modelconfig.ConnectRequest{Provider: provider, EndpointID: "judgment", BaseURL: a.URL + "/v1"})
		if err != nil || len(models) != 2 {
			t.Fatalf("retained judgment discovery via %s: %#v %v", provider, models, err)
		}
	}
	result, err := stack.ConfigurationCommands().ConnectModel(t.Context(), principal, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "judgment-alias-reuse", ExpectedRevision: &revision},
		Config:    appserver.ConnectConfig{Provider: "magpie-judgment", BaseURL: a.URL + "/v1", Model: "typesafe/jev-b"},
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("alias did not reuse the same gateway credential: %+v %v", result, err)
	}
}
