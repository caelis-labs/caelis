package gatewayapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestNativeWorkerPluginConfigurationHTTP(t *testing.T) {
	if !builtinComputerUseSupported() {
		t.Skip("native fixed capability protocol is exercised on supported targets")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 75*time.Second)
	defer cancel()
	root := t.TempDir()
	provider := &nativePluginProvider{}
	store := filepath.Join(root, "store")
	host := startApplicationHTTPHost(t, store, root, provider)
	defer func() { host.close(t) }()
	connectApplicationChildModel(t, ctx, host, "gpt-4.1")
	client, _ := connectSessionOwnerHTTP(t, ctx, host, "owner", filepath.Join(root, "owner.token"))
	// Bot managed native Workers use this ordinary creation route, without Profile.
	created, err := client.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "native-worker-original"}, CWD: root, Model: "custom/gpt-4.1", Title: "native-worker"})
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := client.SessionConfiguration(ctx, created.SessionID)
	if err != nil || configuration.Revision == 0 {
		t.Fatalf("ordinary native Worker configuration: %+v %v", configuration, err)
	}
	if configuration.Native == nil || configuration.Native.Plugins != nil || configuration.Profile.Model != "" {
		t.Fatalf("ordinary inheritance became an explicit execution snapshot: %+v", configuration)
	}
	info, err := client.Initialize(ctx)
	if err != nil || !strings.Contains(strings.Join(info.Capabilities, ","), sessionprofile.CapabilityNativePluginConfiguration) {
		t.Fatal("native plugin configuration capability missing", err)
	}
	foreign, _ := connectSessionOwnerHTTP(t, ctx, host, "foreign-owner", filepath.Join(root, "foreign.token"))
	if _, err := foreign.SessionConfiguration(ctx, created.SessionID); errorcode.CodeOf(err) != errorcode.PermissionDenied {
		t.Fatalf("foreign read=%v", err)
	}
	installed := nativeProfilePackageHTTP(t, "0.1.3")
	audit := filepath.Join(root, "remote-audit")
	launch := map[string]any{"mcpServers": map[string]any{"desktop-world": map[string]any{"command": os.Args[0], "args": []string{"-test.run=^TestNativeProfilePluginMCPHelper$", "--", "native-profile-plugin-helper", audit}}}}
	raw, _ := json.Marshal(launch)
	if err := os.WriteFile(filepath.Join(installed, ".mcp.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	status, _ := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if _, err := client.AddPluginPath(ctx, appserver.AddPluginPathRequest{WriteBase: appserver.WriteBase{OperationID: "standalone-source", ExpectedRevision: &status.Configuration.Revision}, Path: installed}); err != nil {
		t.Fatal(err)
	}
	selected := nativeProfilePackageHTTP(t, "0.3.3")
	plugins := []sessionprofile.Plugin{{Root: selected, MCPServers: []sessionprofile.MCPServer{{Name: "desktop-world", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestNativeProfilePluginMCPHelper$", "--", "native-profile-plugin-helper", audit}, WorkDir: selected}}}}
	request := appserver.UpdateSessionConfigurationRequest{WriteBase: appserver.WriteBase{OperationID: "worker-plugin-original", SessionID: created.SessionID}, ExpectedConfigurationRevision: 1, Patch: sessionprofile.ConfigurationPatch{Plugins: &plugins}}
	if _, err := foreign.UpdateSessionConfiguration(ctx, request); errorcode.CodeOf(err) != errorcode.PermissionDenied {
		t.Fatalf("foreign update=%v", err)
	}
	updated, err := configurationResult(client.UpdateSessionConfiguration(ctx, request))
	if err != nil || updated.Native == nil || updated.Native.Plugins == nil || len(*updated.Native.Plugins) != 1 || updated.Revision != 2 {
		t.Fatalf("native selection commit: %+v %v", updated, err)
	}
	if _, err := client.SessionProfile(ctx, created.SessionID); !errors.Is(err, sessionprofile.ErrNotFound) && errorcode.CodeOf(err) != errorcode.NotFound {
		t.Fatalf("ordinary Session acquired Profile: %v", err)
	}
	feed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: created.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = feed.Subscription.Close() }()
	manual, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: created.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the existing native mode owner. The synthetic provider is not a
	// Guardian reviewer; manual consent keeps this fixture's approval measurable.
	if _, err := client.ConfigureSessionMode(ctx, appserver.SessionModeRequest{WriteBase: appserver.WriteBase{OperationID: "native-manual", SessionID: created.SessionID, ExpectedRevision: &manual.Revision, ExpectedControllerEpoch: manual.Controller.EpochID}, Mode: "manual"}); err != nil {
		t.Fatal(err)
	}
	if n := settleNativeProfileHTTPBeforeApproval(t, ctx, client, created.SessionID, "worker-exec", "FIRST_APP", func() {
		empty := []sessionprofile.Plugin{}
		blocked := request
		blocked.OperationID, blocked.ExpectedConfigurationRevision, blocked.Patch.Plugins = "native-update-during-approval", 2, &empty
		if _, err := client.UpdateSessionConfiguration(ctx, blocked); errorcode.CodeOf(err) != errorcode.Conflict {
			t.Fatalf("pending native approval was displaced: %v", err)
		}
		current, err := client.SessionConfiguration(ctx, created.SessionID)
		if err != nil || current.Revision != 2 || current.Native == nil || current.Native.Plugins == nil || len(*current.Native.Plugins) != 1 {
			t.Fatal("busy update changed selected source", err)
		}
		if raw, err := os.ReadFile(audit); err == nil && len(raw) != 0 {
			t.Fatal("native exec preceded app consent")
		}
	}); n != 1 {
		loaded, _ := host.stack.Sessions().LoadSession(ctx, session.LoadSessionRequest{SessionRef: session.SessionRef{SessionID: created.SessionID}})
		for _, event := range loaded.Events {
			if event.Tool != nil {
				t.Logf("synthetic native event=%s tool=%s status=%s output=%v", event.Type, event.Tool.Name, event.Tool.Status, event.Tool.Output)
			}
		}
		t.Fatalf("native worker app approvals=%d", n)
	}
	health, err := client.SessionMCPStatus(ctx, created.SessionID)
	if err != nil || len(health.Plugins) != 1 || health.Plugins[0].Root != selected || health.Plugins[0].Version != "0.3.3" || len(health.Servers) != 1 || health.Servers[0].PluginID != "desktop-world" || len(health.Skills) != 1 || health.Skills[0].Name != "desktop-world:desktop-world" {
		t.Fatalf("native assembly: %+v %v", health, err)
	}
	// Replaying the exact command returns the saved receipt, not another update.
	replayed, err := configurationResult(client.UpdateSessionConfiguration(ctx, request))
	if err != nil || replayed.Revision != updated.Revision {
		t.Fatalf("original config receipt lost: %+v %v", replayed, err)
	}
	empty := []sessionprofile.Plugin{}
	conflict := request
	conflict.Patch.Plugins = &empty
	if _, err := client.UpdateSessionConfiguration(ctx, conflict); errorcode.CodeOf(err) != errorcode.Conflict {
		t.Fatalf("changed intent under original ID=%v", err)
	}
	conflict.OperationID = "stale-native-config"
	if _, err := client.UpdateSessionConfiguration(ctx, conflict); errorcode.CodeOf(err) != errorcode.Conflict {
		t.Fatalf("stale config CAS=%v", err)
	}
	// Native model selection remains authoritative rather than being frozen in Profile.
	state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: created.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.UseSessionModel(ctx, appserver.SessionModelRequest{WriteBase: appserver.WriteBase{OperationID: "native-model-still-owned", SessionID: created.SessionID, ExpectedRevision: &state.Revision, ExpectedControllerEpoch: state.Controller.EpochID}, Model: "custom/gpt-4.1"}); err != nil {
		t.Fatal("native model authority replaced", err)
	}
	loaded, err := host.stack.Sessions().LoadSession(ctx, session.LoadSessionRequest{SessionRef: session.SessionRef{SessionID: created.SessionID}})
	if err != nil {
		t.Fatal(err)
	}
	executions := 0
	for _, event := range loaded.Events {
		if event.Type == session.EventTypeToolResult && event.Tool != nil && event.Tool.Name == "desktop_world__exec" {
			if event.Tool.Status == "failed" || event.Tool.ModelCall == nil || event.Tool.ModelCall.Name != "mcp_call" || event.Tool.ModelCall.ID != event.Tool.ID {
				t.Fatal("native worker result lost envelope identity")
			}
			executions++
		}
	}
	remote, err := os.ReadFile(audit)
	if err != nil || executions != 2 || len(strings.Fields(string(remote))) != 2 {
		t.Fatalf("native exec counts local=%d remote=%d err=%v", executions, len(strings.Fields(string(remote))), err)
	}
	if !strings.Contains(provider.snapshot(), "PAIRED_VERSION_0.3.3") || !strings.Contains(provider.snapshot(), `"RunCommand"`) {
		t.Fatal("paired native Skill or native tools lost")
	}
	clear := request
	clear.OperationID, clear.ExpectedConfigurationRevision, clear.Patch.Plugins = "native-disable", 2, &empty
	cleared, err := configurationResult(client.UpdateSessionConfiguration(ctx, clear))
	if err != nil || cleared.Native == nil || cleared.Native.Plugins == nil || len(*cleared.Native.Plugins) != 0 {
		t.Fatalf("empty override lost: %+v %v", cleared, err)
	}
	if n := settleNativeProfileHTTP(t, ctx, client, created.SessionID, "native-disabled-turn", "NATIVE_NO_CALL"); n != 0 {
		t.Fatal("disabled plugin requested approval")
	}
	health, err = client.SessionMCPStatus(ctx, created.SessionID)
	if err != nil || len(health.Plugins) != 1 || health.Plugins[0].Root != installed || health.Plugins[0].Version != "0.1.3" {
		t.Fatalf("[] lost the enabled global DTW default: %+v %v", health, err)
	}
	_ = feed.Subscription.Close()
	host.close(t)
	host = startApplicationHTTPHost(t, store, root, provider)
	client, _ = connectSessionOwnerHTTP(t, ctx, host, "owner", filepath.Join(root, "owner.token"))
	recovered, err := client.SessionConfiguration(ctx, created.SessionID)
	if err != nil || recovered.Native == nil || recovered.Native.Plugins == nil || len(*recovered.Native.Plugins) != 0 || recovered.Revision != 3 {
		t.Fatalf("native clear did not survive reconnect: %+v %v", recovered, err)
	}
	if n := settleNativeProfileHTTP(t, ctx, client, created.SessionID, "recovered-disabled", "NATIVE_NO_CALL"); n != 0 {
		t.Fatal("reconnected [] inherited Host authorization")
	}
	standalone, err := client.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "independent-core"}, CWD: root, Model: "custom/gpt-4.1"})
	if err != nil {
		t.Fatal(err)
	}
	independentFeed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: standalone.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	defer independentFeed.Subscription.Close()
	if n := settleNativeProfileHTTP(t, ctx, client, standalone.SessionID, "standalone-sample", "NATIVE_NO_CALL"); n != 0 {
		t.Fatal("no-call standalone turn requested approval")
	}
	independentConfig, err := client.SessionConfiguration(ctx, standalone.SessionID)
	if err != nil || independentConfig.Native == nil || independentConfig.Native.Plugins != nil {
		t.Fatalf("independent native inheritance replaced: %+v %v", independentConfig, err)
	}
	health, err = client.SessionMCPStatus(ctx, standalone.SessionID)
	if err != nil || len(health.Plugins) != 1 || health.Plugins[0].Root != installed || health.Plugins[0].Version != "0.1.3" {
		t.Fatalf("independent Core source overwritten: %+v %v", health, err)
	}
	t.Log("ordinary native Worker: owner/CAS/busy/receipt preserved; synthetic MCP effects=2, OS effects=0, app approval=1; optional [] survives reopen and retains the enabled global DTW default")
}

func TestNativeWorkerPluginImmediateConfigurationAfterModelCreate(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, &nativePluginProvider{})
	defer host.close(t)
	connectApplicationChildModel(t, ctx, host, "gpt-4.1")
	client, _ := connectSessionOwnerHTTP(t, ctx, host, "owner", filepath.Join(root, "owner.token"))
	for index := range 5 {
		created, err := client.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: fmt.Sprintf("fast-native-create-%d", index)}, CWD: root, Model: "custom/gpt-4.1"})
		if err != nil {
			t.Fatal(err)
		}
		configuration, err := client.SessionConfiguration(ctx, created.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		empty := []sessionprofile.Plugin{}
		result, err := configurationResult(client.UpdateSessionConfiguration(ctx, appserver.UpdateSessionConfigurationRequest{WriteBase: appserver.WriteBase{OperationID: fmt.Sprintf("fast-native-select-%d", index), SessionID: created.SessionID}, ExpectedConfigurationRevision: configuration.Revision, Patch: sessionprofile.ConfigurationPatch{Plugins: &empty}}))
		if err != nil || result.Native == nil || result.Native.Plugins == nil {
			t.Fatalf("immediate native configuration index=%d: %+v %v", index, result, err)
		}
	}
}
