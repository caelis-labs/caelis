package controlserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

type bindingStatusService struct {
	appserver.AgentService
	status agentbinding.Status
}

func (s bindingStatusService) AgentBindingStatus(context.Context, appserver.Principal, appserver.AgentRequest) (agentbinding.Status, error) {
	return s.status, nil
}

// This is the binding status shape consumed by strict v1 Go clients before
// eligibility negotiation. Do not alias HandleStatus: that would hide additions.
type legacyBindingStatus struct {
	Handles []struct {
		Definition agentbinding.Definition
		Binding    agentbinding.Binding
		Profile    modelprofile.ModelProfile
	}
	Targets []modelprofile.ModelProfile
	Sets    []agentbinding.BindingSetStatus
}

func TestAgentBindingStatusNegotiatesEligibilityWithoutBreakingStrictClients(t *testing.T) {
	status := agentbinding.Status{
		Handles: []agentbinding.HandleStatus{
			{Definition: agentbinding.Definition{Handle: agentbinding.HandleGuardian, Configurable: true}, EligibleProfileIDs: []string{"provider:model"}},
			{Definition: agentbinding.Definition{Handle: agentbinding.HandleSelf}, EligibleProfileIDs: []string{}},
		},
		Targets: []modelprofile.ModelProfile{}, Sets: []agentbinding.BindingSetStatus{},
	}
	services := testAppServerServices(&fakeService{}, staticStatusService{})
	services.Agents = bindingStatusService{status: status}
	server, err := New(HandlerConfig{Services: services, Authenticator: testAuthenticator(), AllowedHosts: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server)
	t.Cleanup(host.Close)
	readStatus := func(query string) (int, []byte) {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, host.URL+apiPrefix+"/agents/binding-status"+query, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer test-token")
		response, err := host.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, raw
	}

	code, raw := readStatus("")
	var legacy legacyBindingStatus
	if err := wirev1.Unmarshal(raw, &legacy); code != http.StatusOK || err != nil || len(legacy.Handles) != 2 {
		t.Fatalf("strict legacy client could not read new Host: status %d, error %v, body %s", code, err, raw)
	}
	if strings.Contains(string(raw), "eligible_profile_ids") {
		t.Fatalf("unrequested eligibility entered legacy response: %s", raw)
	}
	code, raw = readStatus("?include=eligible_profile_ids")
	var expanded agentbinding.Status
	if err := wirev1.Unmarshal(raw, &expanded); code != http.StatusOK || err != nil || !reflect.DeepEqual(expanded, status) {
		t.Fatalf("negotiated response = %+v, status %d, error %v; want %+v", expanded, code, err, status)
	}
	if err := wirev1.Unmarshal(raw, &legacy); err == nil {
		t.Fatal("legacy decoder fixture no longer rejects unknown eligibility")
	}
	client, err := httpclient.New(httpclient.Config{BaseURL: host.URL, BearerToken: "test-token", Compatibility: appserver.CurrentCompatibility()})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.AgentBindingStatus(t.Context(), appserver.AgentRequest{})
	if err != nil || !reflect.DeepEqual(got, status) {
		t.Fatalf("current HTTP client did not request eligibility: %+v, %v", got, err)
	}
	for _, query := range []string{"?include=unknown", "?include=", "?include=eligible_profile_ids&include=eligible_profile_ids"} {
		if code, raw := readStatus(query); code != http.StatusBadRequest {
			t.Errorf("unsupported selection %q returned %d: %s", query, code, raw)
		}
	}
}

func TestAgentBindingStatusClientReadsLegacyHostWithoutInventingEligibility(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != apiPrefix+"/agents/binding-status" || r.URL.Query().Get("include") != "eligible_profile_ids" {
			t.Errorf("unexpected status request: %s %s", r.Method, r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Handles":[{"Definition":{"Handle":"orbit"},"Binding":{},"Profile":{"backend":{},"effort":{}}}],"Targets":[],"Sets":[]}`)
	}))
	t.Cleanup(host.Close)
	client, err := httpclient.New(httpclient.Config{BaseURL: host.URL, BearerToken: "test-token", Compatibility: appserver.CurrentCompatibility()})
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.AgentBindingStatus(t.Context(), appserver.AgentRequest{})
	if err != nil || len(status.Handles) != 1 || status.Handles[0].EligibleProfileIDs != nil {
		t.Fatalf("old Host eligibility should remain unknown: %+v, %v", status, err)
	}
}
