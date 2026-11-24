package gatewayapp

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/plugin"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestBrokenBuiltinPluginDoesNotBlockHostOrHTTPPrompt(t *testing.T) {
	for _, beforeHost := range []bool{true, false} {
		t.Run(map[bool]string{true: "Host startup", false: "Session activation"}[beforeHost], func(t *testing.T) {
			store, workspace := t.TempDir(), t.TempDir()
			fixed, err := plugin.ProductContributions(plugin.RuntimePaths{StoreDir: store})
			if err != nil {
				t.Fatal(err)
			}
			if len(fixed.SkillBundles) == 0 {
				t.Skip("no embedded desktop-world for this platform")
			}
			skillPath := filepath.Join(fixed.SkillBundles[0].Root, "dtw", "SKILL.md")
			breakPlugin := func() {
				t.Helper()
				if err := os.WriteFile(skillPath, []byte("damaged Skill"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if beforeHost {
				breakPlugin()
			}
			var modelCalls atomic.Int32
			modelClient := &http.Client{Transport: gatewayAppRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				modelCalls.Add(1)
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"model":"test","message":{"role":"assistant","content":"harness available"},"done":true}`)), Request: req}, nil
			})}
			stack, err := newGatewayAppTestStack(t, Config{
				StoreDir: store, WorkspaceKey: workspace, WorkspaceCWD: workspace,
				Sandbox: SandboxConfig{RequestedType: "host"},
				Model:   ModelConfig{Provider: "ollama", API: model.APIOllama, Model: "test", HTTPClient: modelClient},
			})
			if err != nil {
				t.Fatalf("plugin blocked Host startup: %v", err)
			}
			t.Cleanup(func() { _ = stack.Close() })
			if !beforeHost {
				breakPlugin()
			}
			if _, err := stack.composition.resolveApplicationPlugins(sessionprofile.Profile{}, applicationPluginPaths(store, "unrelated-configuration", workspace)); err != nil {
				t.Fatalf("unavailable default blocked unrelated configuration: %v", err)
			}
			client := newWorkspaceRuntimeHTTPClient(t, stack, "local-user")
			id := createWorkspaceRuntimeTestSession(t, client, "create-after-plugin-failure", "session-after-plugin-failure", workspace, workspace)
			observed, err := client.Reconnect(t.Context(), appserver.ReconnectRequest{SessionID: id})
			if err != nil {
				t.Fatalf("plugin blocked Session observation: %v", err)
			}
			defer observed.Subscription.Close()
			prompted, err := client.Prompt(t.Context(), appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "prompt-after-plugin-failure", SessionID: id}, Input: "continue"})
			if err != nil || prompted.Outcome != appserver.OutcomeCommitted {
				t.Fatalf("plugin blocked HTTP prompt: %+v, %v", prompted, err)
			}
			waitForGatewayAppTestTurn(t, stack, id)
			if modelCalls.Load() == 0 {
				t.Fatal("healthy model execution did not continue")
			}
			configuration, err := stack.composition.authorities.sessionProfiles.NativeConfiguration(t.Context(), sessionprofile.PrincipalScope("local-user"), mustCurrentSession(t, stack, id))
			if err != nil {
				t.Fatal(err)
			}
			loaded, ok := stack.sessionRuntimes.loaded(id)
			if !ok || loaded.instance.capabilityStatus == nil {
				t.Fatal("Session lost its plugin status")
			}
			status := loaded.instance.capabilityStatus(configuration.Revision)
			if len(status.Plugins) != 1 || status.Plugins[0].ID != "desktop-world" || status.Plugins[0].Status != "failed" || !strings.Contains(status.Plugins[0].Warning, "builtin desktop-world: builtin file checksum mismatch") {
				t.Fatalf("plugin status lost the concrete cause: %+v", status.Plugins)
			}
			deadline := time.After(3 * time.Second)
			for {
				select {
				case delivery := <-observed.Subscription.Deliveries():
					for _, event := range delivery.Events {
						if event.Kind == eventstream.KindNotice && strings.Contains(event.Notice, `Plugin "desktop-world" disabled: builtin desktop-world: builtin file checksum mismatch: skills`) {
							if event.Delivery == nil || event.Delivery.Mode != eventstream.DeliveryTransient {
								t.Fatalf("package diagnostic became canonical data: %+v", event)
							}
							t.Log(event.Notice)
							return
						}
					}
				case <-deadline:
					t.Fatal("plugin failure was not reported with its concrete cause")
				}
			}
		})
	}
}

func TestAvailableApplicationPluginsKeepHealthySibling(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	broken := selectedPluginFixture(t, "broken-plugin", "1.0.0")
	healthy := selectedPluginFixture(t, "healthy-plugin", "1.0.0")
	if err := os.WriteFile(filepath.Join(broken, ".claude-plugin", "plugin.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	profile := sessionprofile.Profile{Plugins: []sessionprofile.Plugin{{Root: broken}, {Root: healthy}}}
	paths := applicationPluginPaths(stack.composition.authorities.storeDir, "availability", stack.composition.workspace.CWD)
	if _, err := stack.composition.resolveApplicationPlugins(profile, paths); errorcode.CodeOf(err) != errorcode.InvalidArgument || !strings.Contains(err.Error(), "parse enabled plugin") || !strings.Contains(err.Error(), "unexpected end of JSON input") {
		t.Fatalf("explicit configuration lost its actionable error: %v", err)
	}
	got, err := stack.composition.resolveAvailableApplicationPlugins(profile, paths, stack.composition.authorities.userID)
	if err != nil {
		t.Fatal(err)
	}
	failed, ready := false, false
	for _, source := range got.Sources {
		failed = failed || source.Root == broken && source.Status == "failed" && source.Error != ""
		ready = ready || source.Root == healthy && source.Error == ""
	}
	if !failed || !ready {
		t.Fatalf("package availability lost: %+v", got.Sources)
	}
	for _, spec := range got.MCPServerSpecs {
		if spec.PackageRoot == broken {
			t.Fatal("failed package still contributed executable tools")
		}
	}
}

func TestAvailableApplicationPluginSkillConflictDisablesOnlyItsPackage(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	changing := selectedPluginFixture(t, "changing-plugin", "1.0.0")
	healthy := selectedPluginFixture(t, "healthy-plugin", "1.0.0")
	regular := filepath.Join(t.TempDir(), "ordinary-skill")
	writeSkill := func(root string) {
		t.Helper()
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: new-skill\ndescription: Selected Skill.\n---\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(regular)
	profile := sessionprofile.Profile{Plugins: []sessionprofile.Plugin{{Root: changing}, {Root: healthy}}, SkillRoots: []string{regular}}
	paths := applicationPluginPaths(stack.composition.authorities.storeDir, "skill-conflict", stack.composition.workspace.CWD)
	if _, err := stack.composition.resolveApplicationPlugins(profile, paths); err != nil {
		t.Fatal(err)
	}
	writeSkill(filepath.Join(changing, "skills", "new-skill"))
	got, err := stack.composition.resolveAvailableApplicationPlugins(profile, paths, stack.composition.authorities.userID)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range got.Sources {
		if source.Root == changing && (source.Status != "failed" || !strings.Contains(source.Error, "conflicts with a native plugin")) {
			t.Fatalf("changed package did not retain its Skill conflict: %+v", source)
		}
		if source.Root == healthy && source.Error != "" {
			t.Fatalf("healthy package was disabled: %+v", source)
		}
	}
	if _, _, err := assembleApplicationSkills(profile, false, got.SkillBundles...); err != nil {
		t.Fatalf("plugin conflict blocked remaining Session capabilities: %v", err)
	}
}
