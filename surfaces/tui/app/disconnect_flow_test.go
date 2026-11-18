package tuiapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
	"github.com/charmbracelet/x/ansi"
)

func TestDisconnectWizardChecksHostCapability(t *testing.T) {
	const upgrade = "Upgrade the Control Host to use /disconnect."
	for _, tc := range []struct {
		name       string
		supported  bool
		candidates []appserver.SlashArgCandidate
		want       string
	}{
		{name: "current Host", supported: true, candidates: []appserver.SlashArgCandidate{{Value: "provider:codex/model", Display: "Codex / Model"}}, want: "Codex / Model"},
		{name: "empty current Host", supported: true, want: "Nothing configured"},
		{name: "compatible older Host", want: upgrade},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.Method + " " + strings.TrimPrefix(r.URL.Path, wirev1.APIPrefix) {
				case "GET /initialize":
					info := appserver.ServerInfo{ProtocolVersion: acpsdk.ProtocolVersionNumber, EnvelopeVersion: appserver.EnvelopeVersion, APIVersion: appserver.HTTPAPIVersion}
					if tc.supported {
						info.Capabilities = []string{appserver.CapabilityDeleteModels}
					}
					_ = json.NewEncoder(w).Encode(info)
				case "POST /agents/binding-status", "POST /agents/status":
					_, _ = w.Write([]byte(`{}`))
				case "POST /completion/slash-arguments":
					var request appserver.CompletionRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Command != "disconnect-models" {
						t.Errorf("unexpected completion request %+v, %v", request, err)
					}
					if !tc.supported {
						t.Error("queried disconnect completion on an older Host")
					}
					_ = json.NewEncoder(w).Encode(tc.candidates)
				default:
					t.Errorf("unexpected Host request %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(host.Close)
			client, err := httpclient.New(httpclient.Config{
				BaseURL: host.URL, BearerToken: "fixture-token", HTTPClient: host.Client(), Compatibility: appserver.CurrentCompatibility(),
			})
			if err != nil {
				t.Fatal(err)
			}
			adapter, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
				Sessions: client, Participants: client, Status: client, Configuration: client,
				Agents: client, Completion: client, Plugins: client, Surface: "tui",
			})
			if err != nil {
				t.Fatal(err)
			}
			m := NewModel(ConfigFromControlService(adapter, nil, Config{Context: t.Context(), Commands: DefaultCommands(), Wizards: DefaultWizards()}))
			m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			_, cmd := m.submitInteractiveLine("/disconnect", "/disconnect", nil)
			runConnectTestCmd(m, cmd)
			frame := ansi.Strip(m.View().Content)
			if !strings.Contains(frame, tc.want) || (!tc.supported && strings.Contains(frame, "Nothing configured")) {
				t.Fatalf("expected %q in disconnect picker:\n%s", tc.want, frame)
			}
			if !tc.supported {
				removed, err := adapter.DeleteModels(t.Context(), []string{"provider:codex/model"})
				if len(removed) != 0 || errorcode.CodeOf(err) != errorcode.Unsupported {
					t.Fatalf("DeleteModels on older Host = %v, %v", removed, err)
				}
			}
		})
	}
}

func TestDisconnectWizardSelectsMixedModelsFromOneList(t *testing.T) {
	var called string
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		ExecuteLine: func(submission Submission) TaskResultMsg {
			called = submission.Text
			return TaskResultMsg{}
		},
		SlashArgComplete: func(_ context.Context, command, query string, _ int) ([]SlashArgCandidate, error) {
			if command != "disconnect-models" {
				t.Fatalf("unexpected completion command %q", command)
			}
			return []SlashArgCandidate{
				{Value: "provider:codex/model", Display: "Codex / Model"},
				{Value: "acp:codex:model", Display: "ACP · Codex / Model"},
			}, nil
		},
	})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	runConnectTestCmd(m, m.openSlashArgPicker("disconnect"))
	connectPress(m, "space")
	connectPress(m, "down")
	connectPress(m, "space")
	if called != "" {
		t.Fatalf("selection submitted before confirmation: %q", called)
	}
	waiting := m.View().Content
	for _, want := range []string{"Connected models", "[x] Codex / Model", "[x] ACP · Codex / Model", "space toggle", "Disconnect 2"} {
		if !strings.Contains(ansi.Strip(waiting), want) {
			t.Fatalf("disconnect picker missing %q:\n%s", want, ansi.Strip(waiting))
		}
	}
	connectPress(m, "enter")
	if want := `/disconnect ["provider:codex/model","acp:codex:model"]`; called != want {
		t.Fatalf("submitted = %q, want %q", called, want)
	}
	updates := renderFullscreenFramesForTest(t, 100, 30, waiting)
	assertPhysicalFullscreenFrame(t, 100, 30, waiting, updates)
}
