package tuiapp

import (
	"image/color"
	"net/http"
	"net/http/httptest"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/uipreferences"
	"github.com/caelis-labs/caelis/internal/gatewayapptest/localclient"
	"github.com/charmbracelet/colorprofile"
)

func TestProductUIPreferencesSurviveHostRestart(t *testing.T) {
	t.Setenv("CAELIS_THEME", "")
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("presentation preferences invoked the model provider")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer provider.Close()
	store, workspace := t.TempDir(), t.TempDir()
	clients, closeHost, err := localclient.New(t.Context(), store, workspace, provider.URL, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeHost != nil {
			if err := closeHost(); err != nil {
				t.Error(err)
			}
		}
	})
	m := NewModel(Config{UIPreferences: clients.UIPreferences, ColorProfile: colorprofile.TrueColor})
	runTeaCmds(t, m, m.loadUIPreferences())
	runTeaCmds(t, m, m.setSubagentLayout(uipreferences.Right))
	m.workspace.resizeRatio = 60
	runTeaCmds(t, m, m.applyPaneResize())
	runTeaCmds(t, m, m.submitThemeCommand("/theme catppuccin"))
	if err := closeHost(); err != nil {
		t.Fatal(err)
	}
	closeHost = nil

	clients, closeHost, err = localclient.New(t.Context(), store, workspace, provider.URL, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	reopened := NewModel(Config{UIPreferences: clients.UIPreferences, ColorProfile: colorprofile.TrueColor, NoAnimation: true})
	reopened.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	reopened.Update(tea.BackgroundColorMsg{Color: color.White})
	before := reopened.View().Content
	runTeaCmds(t, reopened, reopened.loadUIPreferences())
	want := (uipreferences.Preferences{Theme: "catppuccin", SubagentLayout: uipreferences.Right, HorizontalRatio: 60}).WithDefaults()
	if reopened.uiPreferences.value != want || reopened.themeName != "catppuccin" || reopened.theme.Name != "catppuccin-latte" {
		t.Fatalf("restarted Host/TUI preferences = %#v, theme = %s", reopened.uiPreferences.value, reopened.theme.Name)
	}
	after := reopened.View().Content
	assertPhysicalFullscreenFrame(t, 100, 30, after, renderFullscreenFramesForTest(t, 100, 30, before, after))
}
