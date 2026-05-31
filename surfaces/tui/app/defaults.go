package tuiapp

import (
	"runtime"
	"strings"

	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	tuicommands "github.com/OnslaughtSnail/caelis/surfaces/tui/commands"
)

// defaults.go provides DefaultCommands and DefaultWizards for the TUI shell.

func lookupSlashCommandSpec(name string) (tuicommands.CommandSpec, bool) {
	return tuicommands.Lookup(name)
}

func defaultHelpText() string {
	return helpTextForCommands(DefaultCommands())
}

func helpTextForCommands(commands []string) string {
	return tuicommands.HelpText(commands) + "\n\n" + shortcutHelpTextForPlatform(runtime.GOOS, isWSL())
}

type shortcutHelpRow struct {
	Keys        []string
	Description string
}

func shortcutHelpTextForPlatform(goos string, wsl bool) string {
	rows := []shortcutHelpRow{
		{Keys: []string{"enter"}, Description: "Send message; queue guidance while running"},
		{Keys: []string{"shift+enter", "ctrl+j"}, Description: "Insert newline"},
		{Keys: []string{"tab"}, Description: "Complete selected command, file, agent, or skill"},
		{Keys: []string{"up", "down"}, Description: "Input history or menu selection"},
		{Keys: []string{"pgup", "pgdn"}, Description: "Scroll transcript"},
		{Keys: []string{"shift+tab", "ctrl+o"}, Description: "Toggle session mode"},
		{Keys: []string{"ctrl+u"}, Description: "Clear input"},
		{Keys: imagePasteKeysForPlatform(goos, wsl), Description: "Paste clipboard image"},
		{Keys: textPasteKeysForPlatform(goos, wsl), Description: "Paste clipboard text"},
		{Keys: []string{"esc"}, Description: "Interrupt running turn or close overlay"},
		{Keys: []string{"ctrl+c"}, Description: "Quit after confirmation"},
	}
	width := 0
	for _, row := range rows {
		if n := len([]rune(formatShortcutKeys(row.Keys))); n > width {
			width = n
		}
	}
	lines := []string{"Shortcuts (" + platformHelpLabel(goos, wsl) + "):"}
	for _, row := range rows {
		keys := formatShortcutKeys(row.Keys)
		if keys == "" {
			continue
		}
		lines = append(lines, "  "+padRightRunes(keys, width)+"  "+strings.TrimSpace(row.Description))
	}
	return strings.Join(lines, "\n")
}

func platformHelpLabel(goos string, wsl bool) string {
	if wsl {
		return "WSL"
	}
	switch strings.ToLower(strings.TrimSpace(goos)) {
	case "darwin":
		return "macOS"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	default:
		if trimmed := strings.TrimSpace(goos); trimmed != "" {
			return trimmed
		}
		return "current platform"
	}
}

func formatShortcutKeys(keys []string) string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out = append(out, formatShortcutKey(key))
	}
	return strings.Join(out, " / ")
}

func formatShortcutKey(key string) string {
	parts := strings.Split(strings.TrimSpace(key), "+")
	for i, part := range parts {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "ctrl":
			parts[i] = "Ctrl"
		case "shift":
			parts[i] = "Shift"
		case "alt":
			parts[i] = "Alt"
		case "cmd":
			parts[i] = "Cmd"
		case "super":
			parts[i] = "Super"
		case "pgup":
			parts[i] = "PgUp"
		case "pgdn":
			parts[i] = "PgDn"
		case "esc":
			parts[i] = "Esc"
		case "enter":
			parts[i] = "Enter"
		case "tab":
			parts[i] = "Tab"
		case "up":
			parts[i] = "Up"
		case "down":
			parts[i] = "Down"
		default:
			trimmed := strings.TrimSpace(part)
			if trimmed == "" {
				parts[i] = ""
			} else {
				runes := []rune(trimmed)
				runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
				parts[i] = string(runes)
			}
		}
	}
	return strings.Join(parts, "+")
}

func padRightRunes(value string, width int) string {
	count := len([]rune(value))
	if count >= width {
		return value
	}
	return value + strings.Repeat(" ", width-count)
}

// joinNonEmpty joins non-empty parts with the given separator.
func joinNonEmpty(parts []string, sep string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// DefaultCommands returns the set of slash commands available in the TUI.
func DefaultCommands() []string {
	return tuicommands.DefaultNames()
}

// DefaultWizards returns the set of multi-step wizard flows for the TUI.
func DefaultWizards() []WizardDef {
	return []WizardDef{
		connectWizardFromFlow(appservices.DefaultConnectWizardFlow()),
	}
}

func connectWizardFromFlow(flow appviewmodel.WizardFlowView) WizardDef {
	return WizardDef{
		Command:     flow.Command,
		DisplayLine: flow.DisplayLine,
		Steps:       connectWizardStepsFromFlow(flow),
		OnStepConfirm: func(stepKey string, value string, candidate *SlashArgCandidate, state map[string]string) {
			var sharedCandidate *appservices.ConnectWizardConfirmCandidate
			if candidate != nil {
				sharedCandidate = &appservices.ConnectWizardConfirmCandidate{
					Value:  candidate.Value,
					NoAuth: candidate.NoAuth,
				}
			}
			appservices.ConfirmConnectWizardStep(stepKey, value, sharedCandidate, state)
		},
		BuildExecLine: func(state map[string]string) string {
			return appservices.BuildConnectWizardExecLine(state)
		},
	}
}

func connectWizardStepsFromFlow(flow appviewmodel.WizardFlowView) []WizardStepDef {
	steps := make([]WizardStepDef, 0, len(flow.Steps))
	for _, shared := range flow.Steps {
		shared := shared
		step := WizardStepDef{
			Key:              shared.Key,
			HintLabel:        shared.HintLabel,
			FreeformHint:     shared.FreeformHint,
			HideInput:        shared.HideInput,
			NoCompletion:     shared.NoCompletion,
			RequireCandidate: shared.RequireCandidate,
			Validate:         connectWizardValidator(shared.Validator),
			CompletionCommand: func(state map[string]string) string {
				return appservices.ConnectWizardCompletionCommand(shared.Key, state)
			},
			ShouldSkip: func(state map[string]string) bool {
				return appservices.ConnectWizardShouldSkip(shared.Key, state)
			},
		}
		if shared.DynamicFreeformHint {
			step.FreeformHintFunc = func(state map[string]string) string {
				return appservices.ConnectWizardFreeformHint(shared.Key, state)
			}
		}
		steps = append(steps, step)
	}
	return steps
}

func connectWizardValidator(name string) func(string) error {
	switch strings.TrimSpace(name) {
	case appviewmodel.WizardValidatorInt:
		return ValidateInt
	default:
		return nil
	}
}
