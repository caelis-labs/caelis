package gatewayapp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/control/experimental"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/control/modelprofile/builder"
)

// Bot settings read Host catalogs, bind one role at a time, and save/apply a
// Team by name. The Host owns the full configuration and saved binding sets.
// Exercise those HTTP shapes against a legacy document, including invalid
// retired references that must not poison an unrelated configuration save.
func TestRetiredMemoryHTTPPreservesSharedConfigurationUpdates(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	storeDir := filepath.Join(root, "store")
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := modelconfig.Config{Provider: "openai", Model: "gpt-4.1", BaseURL: "https://provider.invalid/v1", AuthType: "none", ContextWindowTokens: 128000}
	profile, err := builder.FromProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := modelprofile.Upsert(modelprofile.Configuration{}, profile)
	if err != nil {
		t.Fatal(err)
	}
	legacy := []agentbinding.Binding{
		{Handle: "steward", ProfileID: "deleted-model", Effort: "invalid"},
		{Handle: "memory-verifier", ProfileID: "deleted-model"},
		{Handle: "steward"},
	}
	orbit := agentbinding.Binding{Handle: "orbit", ProfileID: profile.ID, Effort: "none"}
	doc := configstore.AppConfig{
		SchemaVersion: configstore.SchemaVersionV2, ConfigurationRevision: 7,
		Models:        configstore.PersistedModelConfig{Configs: []modelconfig.Config{cfg}},
		ModelProfiles: profiles,
		Memory:        json.RawMessage(`{"default_binding_ref":"unavailable","future":{"keep":[1,2]}}`),
		AgentBindings: agentbinding.Configuration{
			Bindings: append([]agentbinding.Binding{orbit}, legacy...),
			Sets:     []agentbinding.BindingSet{{Name: "legacy-team", Bindings: append([]agentbinding.Binding{orbit}, legacy...)}},
		},
	}
	// Deliberately bypass current normalization to represent an older writer.
	before, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(storeDir, "config.json")
	if err := os.WriteFile(configPath, before, 0600); err != nil {
		t.Fatal(err)
	}
	provider := &applicationHTTPModel{}
	host := startApplicationHTTPHost(t, storeDir, root, provider)
	defer host.close(t)
	if _, err := host.host.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	read := func() (uint64, agentbinding.Status) {
		t.Helper()
		before, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
		if err != nil {
			t.Fatal(err)
		}
		bindings, err := host.host.AgentBindingStatus(ctx, appserver.AgentRequest{})
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range bindings.Handles {
			if agentbinding.IsRetired(h.Definition.Handle) {
				t.Fatalf("retired role projected to Bot: %+v", h)
			}
		}
		for _, set := range bindings.Sets {
			if !set.Available || len(set.Bindings) != 1 || set.Bindings[0] != orbit {
				t.Fatalf("legacy Team unavailable or polluted: %+v", set)
			}
		}
		for _, command := range []string{"model", "disconnect-provider"} {
			catalog, err := host.host.CompleteSlashArg(ctx, appserver.CompletionRequest{Command: command})
			if err != nil || len(catalog) != 1 {
				t.Fatalf("%s catalog: %+v, %v", command, catalog, err)
			}
		}
		disconnected, err := host.host.DisconnectCandidates(ctx, appserver.AgentRequest{})
		if err != nil || disconnected.Revision != before.Configuration.Revision {
			t.Fatalf("disconnect read: %+v, %v", disconnected, err)
		}
		after, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
		if err != nil || after.Configuration.Revision != before.Configuration.Revision {
			t.Fatalf("incoherent configuration read: %+v, %v", after, err)
		}
		return after.Configuration.Revision, bindings
	}
	revision, _ := read()
	loaded, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(loaded, before) {
		t.Fatalf("startup/catalog reads rewrote old configuration: %v", err)
	}
	// These are the JSON bodies emitted by Bot's ChangeRuntimeConfiguration.
	// In particular, set operations contain a name, never a client binding table.
	sequence := 0
	send := func(path, fields string, rejected bool) {
		t.Helper()
		sequence++
		op := fmt.Sprintf("settings-%d", sequence)
		raw := fmt.Sprintf(`{"operation_id":%q,"expected_revision":"%d",%s}`, op, revision, fields)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, host.server.URL+wirev1.APIPrefix+path, strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+fixtureHostToken("owner"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", op)
		req.Header.Set("If-Match", fmt.Sprintf(`"%d"`, revision))
		response, err := host.server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var result struct {
			Outcome, Detail, Error, Revision string
		}
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if rejected {
			if response.StatusCode != http.StatusBadRequest || result.Outcome != "rejected" || !strings.Contains(result.Detail+result.Error, "Core Memory is retired") {
				t.Fatalf("retired request = HTTP %d: %+v", response.StatusCode, result)
			}
			current, _ := read()
			if current != revision {
				t.Fatal("retired request changed configuration revision")
			}
			return
		}
		if response.StatusCode != http.StatusOK || result.Outcome != "committed" {
			t.Fatalf("%s = HTTP %d: %+v", path, response.StatusCode, result)
		}
		revision, _ = read()
	}
	send("/agents/bind", `"binding":{"handle":"steward","profile_id":"deleted-model","effort":"high","speed":""}`, true)
	send("/agents/reset-binding", `"handle":"memory-verifier"`, true)
	loaded, err = os.ReadFile(configPath)
	if err != nil || !bytes.Equal(loaded, before) {
		t.Fatalf("retired direct writes changed configuration: %v", err)
	}
	send("/agents/bind", fmt.Sprintf(`"binding":{"handle":"orbit","profile_id":%q,"effort":"none","speed":""}`, profile.ID), false)
	send("/agents/apply-binding-set", `"set_name":"legacy-team"`, false)
	send("/agents/save-binding-set", `"set_name":"saved-team"`, false)
	send("/configuration/experimental", `"values":{"async_compact":true,"mcp_catalog_refresh":true,"deferred_tool_retirement":true}`, false)
	_, status := read()
	if len(status.Sets) != 2 {
		t.Fatalf("saved Team missing: %+v", status.Sets)
	}
	loadedDoc, err := configstore.New(storeDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, feature := range experimental.Definitions() {
		if !loadedDoc.Experimental[feature.ID] {
			t.Fatalf("experimental save lost %s: %+v", feature.ID, loadedDoc.Experimental)
		}
	}
	var originalMemory, retainedMemory any
	if err := json.Unmarshal(doc.Memory, &originalMemory); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(loadedDoc.Memory, &retainedMemory); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(originalMemory, retainedMemory) || !reflect.DeepEqual(loadedDoc.AgentBindings.Bindings, []agentbinding.Binding{orbit}) {
		t.Fatalf("unrelated save lost opaque Memory or ordinary binding: %+v", loadedDoc)
	}
	if provider.requestCount() != 0 {
		t.Fatal("configuration access scheduled a model request")
	}
	if _, err := os.Stat(filepath.Join(storeDir, "memory")); !os.IsNotExist(err) {
		t.Fatalf("configuration access initialized Memory: %v", err)
	}
}
