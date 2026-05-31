package tuiapp

import (
	"reflect"
	"strings"
	"testing"

	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
)

func TestDefaultCommandsExposeCanonicalCoreCommandsOnly(t *testing.T) {
	got := DefaultCommands()
	want := []string{
		"help",
		"agent",
		"connect",
		"controller",
		"model",
		"approval",
		"status",
		"settings",
		"task",
		"doctor",
		"new",
		"resume",
		"compact",
		"exit",
		"quit",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultCommands() = %#v, want %#v", got, want)
	}
}

func TestDefaultWizardsCoverCoreConfigFlows(t *testing.T) {
	wizards := DefaultWizards()
	if len(wizards) < 1 {
		t.Fatalf("expected core wizards, got %d", len(wizards))
	}
}

func TestDefaultConnectWizardAdaptsSharedStepShape(t *testing.T) {
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
	if got := len(connect.Steps); got != 8 {
		t.Fatalf("connect wizard step count = %d, want 8", got)
	}
	keys := make([]string, 0, len(connect.Steps))
	for _, step := range connect.Steps {
		keys = append(keys, step.Key)
	}
	flow := appservices.DefaultConnectWizardFlow()
	want := make([]string, 0, len(flow.Steps))
	for _, step := range flow.Steps {
		want = append(want, step.Key)
	}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("connect wizard steps = %#v, want %#v", keys, want)
	}
}

func TestDefaultCommandsHideBTWFromDefaultTUI(t *testing.T) {
	for _, command := range DefaultCommands() {
		if command == "btw" {
			t.Fatalf("DefaultCommands() unexpectedly includes hidden command %q", command)
		}
	}
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
