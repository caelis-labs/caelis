package tuiapp

import (
	"reflect"
	"strings"
	"testing"

	controlcommands "github.com/caelis-labs/caelis/ports/controlcommand"
	"github.com/caelis-labs/caelis/protocol/acp/control"
)

func TestDefaultCommandsExposePlatformCoreCommands(t *testing.T) {
	got := controlcommands.DefaultNamesForPlatform("linux")
	want := []string{
		"help",
		"review",
		"lead",
		"connect",
		"plugin",
		"model",
		"status",
		"new",
		"resume",
		"compact",
		"exit",
		"quit",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultNamesForPlatform(linux) = %#v, want %#v", got, want)
	}

	windows := controlcommands.DefaultNamesForPlatform("windows")
	if !sliceContainsString(windows, "doctor") {
		t.Fatalf("DefaultNamesForPlatform(windows) = %#v, want doctor", windows)
	}
	if sliceContainsString(got, "doctor") {
		t.Fatalf("DefaultNamesForPlatform(linux) = %#v, should hide doctor", got)
	}
}

func TestACPSlashCommandsFilterLocalAndReservedRemoteCommands(t *testing.T) {
	got := acpSlashCommands([]string{"help"}, control.AgentStatusSnapshot{ControllerCommands: []string{
		"/remote", "/compact", "/new now", "/status", "/sandbox",
	}})
	if !sliceContainsString(got, "remote") {
		t.Fatalf("acpSlashCommands() = %#v, want routable remote command", got)
	}
	for _, filtered := range []string{"compact", "new", "status", "sandbox"} {
		if sliceContainsString(got, filtered) {
			t.Fatalf("acpSlashCommands() = %#v, should filter reserved /%s", got, filtered)
		}
	}
}

func TestDefaultWizardsCoverCoreConfigFlows(t *testing.T) {
	wizards := DefaultWizards()
	if len(wizards) < 1 {
		t.Fatalf("expected core wizards, got %d", len(wizards))
	}
}

func TestDefaultConnectWizardSeparatesModelAndACPConnectionSteps(t *testing.T) {
	wizards := DefaultWizards()
	var connect *WizardDef
	for i := range wizards {
		if wizards[i].Command == "connect" {
			connect = &wizards[i]
			break
		}
	}
	if connect == nil {
		t.Fatalf("connect wizard not found")
		return
	}
	if connect.DisplayLine != "/connect" {
		t.Fatalf("DisplayLine = %q, want /connect", connect.DisplayLine)
	}
	if got := len(connect.Steps); got != 1 || connect.Steps[0].Key != "source" || connect.Branch == nil {
		t.Fatalf("connect root wizard = %#v, want one explicit branching step", connect)
	}
	tests := []struct {
		name string
		def  WizardDef
		want []string
	}{
		{name: "model", def: connectModelWizard(), want: []string{"provider", "endpoint", "baseurl", "apikey", "model", "context_window_tokens", "max_output_tokens", "reasoning_levels"}},
		{name: "acp", def: connectACPWizard(), want: []string{"acp_agent", "acp_launcher", "acp_command", "acp_model", "acp_config"}},
		{name: "disconnect", def: disconnectACPWizard(), want: []string{"disconnect_agent", "disconnect_confirm"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys := make([]string, 0, len(tt.def.Steps))
			for _, step := range tt.def.Steps {
				keys = append(keys, step.Key)
			}
			if !reflect.DeepEqual(keys, tt.want) {
				t.Fatalf("steps = %#v, want %#v", keys, tt.want)
			}
		})
	}
}

func TestDefaultCommandsHideBTWFromDefaultTUI(t *testing.T) {
	for _, command := range DefaultCommands() {
		if command == "btw" {
			t.Fatalf("DefaultCommands() unexpectedly includes hidden command %q", command)
		}
	}
}

func sliceContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestShortcutHelpUsesPlatformImagePasteKeys(t *testing.T) {
	windows := shortcutHelpTextForPlatform("windows", false)
	if !strings.Contains(windows, "Ctrl+Alt+V") || !strings.Contains(windows, "Paste clipboard image") {
		t.Fatalf("windows shortcut help = %q, want Ctrl+Alt+V image paste", windows)
	}
	linux := shortcutHelpTextForPlatform("linux", false)
	if !strings.Contains(linux, "Ctrl+V") || !strings.Contains(linux, "Paste clipboard image") {
		t.Fatalf("linux shortcut help = %q, want Ctrl+V image paste", linux)
	}
}
