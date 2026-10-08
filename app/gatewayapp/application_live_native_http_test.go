//go:build darwin || linux || windows

package gatewayapp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/codexauth"
)

// This opt-in sends only synthetic application work to one configured Codex
// provider. Configuration and credentials are copied into a disposable Store;
// the user's Sessions and credential manager are never opened or mutated.
func TestApplicationLiveNativeHTTP(t *testing.T) {
	if os.Getenv("CAELIS_APPLICATION_LIVE_E2E") != "1" {
		t.Skip("set CAELIS_APPLICATION_LIVE_E2E=1 for live native application acceptance")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(home, ".caelis")
	doc, err := gatewayapp.LoadAppConfig(source)
	if err != nil {
		t.Fatal(err)
	}
	alias := os.Getenv("CAELIS_APPLICATION_LIVE_MODEL")
	if alias == "" {
		alias = "gpt-6-luna"
	}
	var selected gatewayapp.ModelConfig
	for _, candidate := range doc.Models.Configs {
		if candidate.Model == alias {
			selected = candidate
			break
		}
	}
	if selected.ID == "" {
		t.Fatalf("configured Codex model %q is missing", alias)
	}
	root := t.TempDir()
	store, workspace := filepath.Join(root, "store"), filepath.Join(root, "应用 空 格")
	for _, path := range []string{store, workspace} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	modelConfig := doc.Models
	modelConfig.Configs = []gatewayapp.ModelConfig{selected}
	modelConfig.ProviderEndpoints = nil
	for _, endpoint := range doc.Models.ProviderEndpoints {
		if endpoint.ID == selected.ProviderEndpointID {
			if endpoint.Provider != "openai-codex" || endpoint.CredentialRef != modelconfig.CodexOAuthCredentialRef {
				t.Fatal("live fixture requires an OAuth Codex endpoint")
			}
			modelConfig.ProviderEndpoints = append(modelConfig.ProviderEndpoints, endpoint)
		}
	}
	if len(modelConfig.ProviderEndpoints) != 1 {
		t.Fatal("configured model lacks exactly one endpoint")
	}
	raw, err := json.Marshal(gatewayapp.AppConfig{SchemaVersion: doc.SchemaVersion, Models: modelConfig})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "config.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	credential, err := os.ReadFile(codexauth.DefaultCredentialPath(source))
	if err != nil {
		t.Fatal(err)
	}
	credentialPath := codexauth.DefaultCredentialPath(store)
	if err := os.MkdirAll(filepath.Dir(credentialPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialPath, credential, 0600); err != nil {
		t.Fatal(err)
	}
	host := startApplicationHTTPHost(t, store, workspace, nil)
	defer host.close(t)
	info, err := host.host.Initialize(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range []string{application.CapabilityNativeExecution, application.CapabilityWorkspaceBinding} {
		if !slices.Contains(info.Capabilities, capability) {
			t.Fatalf("native capability missing: %s", capability)
		}
	}
	client, _ := registerApplicationHTTP(t, ctx, host, "live-native", filepath.Join(root, "application.credential"))
	profile := application.Profile{Version: "live/1", Model: selected.ID, ToolsVersion: "live/1", Execution: "workspace-write",
		Workspace: application.Workspace{CWD: workspace}, NativeTools: []string{"Read", "Write", "RunCommand", "Task"},
		ExecutionConfig: &sandbox.ExecutionConfig{Environment: sandbox.EnvironmentConfig{Set: map[string]string{"CAELIS_APPLICATION_LIVE_VALUE": "LIVE_NATIVE_SENTINEL"}}}}
	created, err := client.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{WriteBase: appserver.WriteBase{OperationID: "live-create"}, Profile: profile})
	if err != nil || created.SessionID == "" {
		t.Fatalf("live application creation = %+v, %v", created, err)
	}
	instructions := "Use Write to create file.txt containing exactly LIVE_NATIVE_SENTINEL with no newline. Use RunCommand to create command.txt containing exactly the value of the CAELIS_APPLICATION_LIVE_VALUE environment variable, with no newline. Stay inside the workspace. If the command returns a running Task, wait for it using Task. Verify both files using Read before answering."
	configured, err := client.UpdateApplicationConfiguration(ctx, created.SessionID, application.UpdateConfigurationRequest{OperationID: "live-configure", ExpectedConfigurationRevision: 1, Patch: application.ConfigurationPatch{Instructions: &instructions}})
	if err != nil || configured.Revision != 2 {
		t.Fatalf("live configuration = %+v, %v", configured, err)
	}
	prompted, err := client.PromptApplication(ctx, appserver.ApplicationPromptRequest{PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: created.SessionID, OperationID: "live-prompt"}, Input: "Carry out the two synthetic file operations now."}, SourceKind: "user"})
	if err != nil {
		t.Fatalf("live application prompt = %+v, %v", prompted, err)
	}
	waitApplicationHTTPIdle(t, ctx, client, created.SessionID)
	for _, name := range []string{"file.txt", "command.txt"} {
		if data, err := os.ReadFile(filepath.Join(workspace, name)); err != nil || string(data) != "LIVE_NATIVE_SENTINEL" {
			t.Fatalf("live %s = %q, %v", name, data, err)
		}
	}
	configuration, err := client.ApplicationConfiguration(ctx, created.SessionID)
	if err != nil || configuration.LastRequest == nil || configuration.LastRequest.Revision != 2 {
		t.Fatalf("live request did not use the committed configuration: %+v, %v", configuration, err)
	}
	if !strings.Contains(configuration.LastRequest.Model, alias) {
		t.Fatalf("live model selector changed: %s", configuration.LastRequest.Model)
	}
	t.Logf("live native application passed: model=%s configuration_revision=2 file_and_command_effects=verified", alias)
}
