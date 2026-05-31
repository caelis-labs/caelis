package tuiapp

import (
	"runtime"
	"strings"

	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
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
		connectWizard(),
	}
}

func connectWizard() WizardDef {
	return WizardDef{
		Command:     "connect",
		DisplayLine: "/connect",
		Steps: []WizardStepDef{
			{
				Key:              "provider",
				HintLabel:        "/connect provider",
				FreeformHint:     "/connect provider: choose a provider; compatible endpoints may ask for a custom base URL",
				RequireCandidate: true,
				CompletionCommand: func(state map[string]string) string {
					return "connect"
				},
			},
			{
				Key:          "endpoint",
				HintLabel:    "/connect endpoint",
				FreeformHint: "/connect endpoint: choose a provider endpoint, or paste a custom base URL",
				CompletionCommand: func(state map[string]string) string {
					return "connect-baseurl:" + state["provider"]
				},
				ShouldSkip: func(state map[string]string) bool {
					return !connectWizardProviderHasEndpointStep(state["provider"])
				},
			},
			{
				Key:          "baseurl",
				HintLabel:    "/connect base_url",
				FreeformHint: "/connect base_url: choose the default compatible API root or paste your own full base URL",
				CompletionCommand: func(state map[string]string) string {
					return "connect-baseurl:" + state["provider"]
				},
				ShouldSkip: func(state map[string]string) bool {
					return !connectWizardProviderHasBaseURLStep(state["provider"])
				},
			},
			{
				Key:       "apikey",
				HintLabel: "/connect api_key",
				HideInput: true,
				FreeformHintFunc: func(state map[string]string) string {
					return "/connect api_key: paste a key, or type env:" + connectWizardTokenEnvHint(state) + " to use an environment variable"
				},
				CompletionCommand: func(state map[string]string) string {
					return "connect-apikey:" + state["provider"]
				},
				ShouldSkip: func(state map[string]string) bool {
					return state["_noauth"] == "true" || state["_reuseauth"] == "true"
				},
			},
			{
				Key:          "model",
				HintLabel:    "/connect model",
				FreeformHint: "/connect model: choose a suggested model or type a custom model name and press enter",
				CompletionCommand: func(state map[string]string) string {
					return "connect-model:" + buildConnectWizardPayload(state)
				},
			},
			{
				Key:          "context_window_tokens",
				HintLabel:    "/connect context_window_tokens",
				Validate:     ValidateInt,
				FreeformHint: "/connect context_window_tokens: type integer and press enter",
				CompletionCommand: func(state map[string]string) string {
					return "connect-context:" + buildConnectWizardPayload(state)
				},
				ShouldSkip: func(state map[string]string) bool {
					return state["_known_model"] == "true"
				},
			},
			{
				Key:          "max_output_tokens",
				HintLabel:    "/connect max_output_tokens",
				Validate:     ValidateInt,
				FreeformHint: "/connect max_output_tokens: type integer and press enter",
				CompletionCommand: func(state map[string]string) string {
					return "connect-maxout:" + buildConnectWizardPayload(state)
				},
				ShouldSkip: func(state map[string]string) bool {
					return state["_known_model"] == "true"
				},
			},
			{
				Key:          "reasoning_levels",
				HintLabel:    "/connect reasoning_levels(csv)",
				FreeformHint: "/connect reasoning_levels(csv): e.g. low,medium (use - for empty)",
				CompletionCommand: func(state map[string]string) string {
					return "connect-reasoning-levels:" + buildConnectWizardPayload(state)
				},
				ShouldSkip: func(state map[string]string) bool {
					return state["_known_model"] == "true"
				},
			},
		},
		OnStepConfirm: func(stepKey string, value string, candidate *SlashArgCandidate, state map[string]string) {
			if stepKey == "provider" {
				state["provider"] = strings.ToLower(strings.TrimSpace(value))
				delete(state, "_reuseauth")
				delete(state, "_noauth")
			}
			if stepKey == "provider" && candidate != nil && candidate.NoAuth {
				state["_noauth"] = "true"
			}
			if stepKey == "endpoint" {
				state["baseurl"] = strings.TrimSpace(value)
				if candidate != nil && candidate.NoAuth {
					state["_reuseauth"] = "true"
				} else {
					delete(state, "_reuseauth")
				}
			}
			if stepKey == "baseurl" {
				if candidate != nil && candidate.NoAuth {
					state["_reuseauth"] = "true"
				} else {
					delete(state, "_reuseauth")
				}
			}
			if stepKey == "model" {
				if candidate != nil && strings.TrimSpace(candidate.Value) != "" && !strings.EqualFold(strings.TrimSpace(candidate.Value), "__custom_model__") {
					state["_known_model"] = "true"
				} else {
					delete(state, "_known_model")
				}
			}
		},
		BuildExecLine: func(state map[string]string) string {
			return appservices.BuildConnectWizardExecLine(state)
		},
	}
}

func connectWizardTokenEnvHint(state map[string]string) string {
	return appservices.ConnectWizardTokenEnvHint(state)
}

func connectWizardProviderHasEndpointStep(provider string) bool {
	return appservices.ConnectWizardProviderHasEndpointStep(provider)
}

func connectWizardProviderHasBaseURLStep(provider string) bool {
	return appservices.ConnectWizardProviderHasBaseURLStep(provider)
}

func buildConnectWizardPayload(state map[string]string) string {
	return appservices.ConnectWizardStateFromMap(state).EncodeCompletionPayload()
}
