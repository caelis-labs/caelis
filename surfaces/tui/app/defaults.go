package tuiapp

import (
	"encoding/json"
	"runtime"
	"strconv"
	"strings"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
	controlcommands "github.com/caelis-labs/caelis/ports/controlcommand"
	"github.com/caelis-labs/caelis/ports/controlprompt/connectwizard"
)

// defaults.go provides DefaultCommands and DefaultWizards for the TUI shell.

func lookupSlashCommandSpec(name string) (controlcommands.CommandSpec, bool) {
	return controlcommands.Lookup(name)
}

func defaultHelpText() string {
	return helpTextForCommands(DefaultCommands())
}

func helpTextForCommands(commands []string) string {
	return controlcommands.HelpText(commands) + "\n\n" + shortcutHelpTextForPlatform(runtime.GOOS, isWSL())
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
		{Keys: []string{"ctrl+u"}, Description: "Update when notified"},
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
	return controlcommands.DefaultNames()
}

func (m *Model) commandCompletionDetail(command string) string {
	name := strings.TrimPrefix(strings.TrimSpace(command), "/")
	if name == "" {
		return ""
	}
	if spec, ok := controlcommands.Lookup(name); ok {
		return strings.TrimSpace(spec.Description)
	}
	if m != nil {
		if detail := strings.TrimSpace(m.cfg.CommandDetails[strings.ToLower(name)]); detail != "" {
			return detail
		}
	}
	return "Send a prompt to the registered ACP agent"
}

// DefaultWizards returns the set of multi-step wizard flows for the TUI.
func DefaultWizards() []WizardDef {
	return []WizardDef{
		connectWizard(),
		subagentWizard(),
	}
}

func connectWizard() WizardDef {
	return WizardDef{
		Command:     "connect",
		DisplayLine: "/connect",
		Steps: []WizardStepDef{{
			Key:               "source",
			HintLabel:         "/connect source",
			FreeformHint:      "/connect source: connect a model provider or local ACP Agent, or disconnect an existing Agent",
			RequireCandidate:  true,
			CompletionCommand: func(map[string]string) string { return "connect" },
		}},
		Branch: func(_ string, value string, _ *SlashArgCandidate, state map[string]string) *WizardDef {
			source := strings.ToLower(strings.TrimSpace(value))
			state["source"] = source
			var next WizardDef
			switch source {
			case "model":
				next = connectModelWizard()
			case "acp":
				next = connectACPWizard()
			case "disconnect":
				next = disconnectACPWizard()
			default:
				return nil
			}
			return &next
		},
	}
}

func connectModelWizard() WizardDef {
	return WizardDef{
		Command: "connect", DisplayLine: "/connect",
		Steps: []WizardStepDef{
			{
				Key: "provider", HintLabel: "/connect provider",
				FreeformHint:      "/connect provider: choose a provider; compatible endpoints may ask for a custom base URL",
				RequireCandidate:  true,
				CompletionCommand: func(map[string]string) string { return "connect-provider" },
			},
			{
				Key: "endpoint", HintLabel: "/connect endpoint",
				FreeformHint:      "/connect endpoint: choose a provider endpoint, or paste a custom base URL",
				CompletionCommand: func(state map[string]string) string { return "connect-baseurl:" + state["provider"] },
				ShouldSkip:        func(state map[string]string) bool { return !connectWizardProviderHasEndpointStep(state["provider"]) },
			},
			{
				Key: "baseurl", HintLabel: "/connect base_url",
				FreeformHint:      "/connect base_url: choose the default compatible API root or paste your own full base URL",
				CompletionCommand: func(state map[string]string) string { return "connect-baseurl:" + state["provider"] },
				ShouldSkip:        func(state map[string]string) bool { return !connectWizardProviderHasBaseURLStep(state["provider"]) },
			},
			{
				Key: "apikey", HintLabel: "/connect api_key", HideInput: true,
				FreeformHintFunc: func(state map[string]string) string {
					return "/connect api_key: paste a key, or type env:" + connectWizardTokenEnvHint(state) + " to use an environment variable"
				},
				CompletionCommand: func(state map[string]string) string { return "connect-apikey:" + state["provider"] },
				ShouldSkip:        func(state map[string]string) bool { return state["_noauth"] == "true" || state["_reuseauth"] == "true" },
			},
			{
				Key: "model", HintLabel: "/connect model · tab adds · enter confirms", MultiSelect: true,
				MultiSelectCandidate: func(candidate SlashArgCandidate) bool { return candidate.ModelMetadataComplete },
				FreeformHintFunc: func(state map[string]string) string {
					if selected := connectWizardSelectedModelCount(state); selected > 0 {
						return "/connect model: press enter to confirm the selected models"
					}
					return "/connect model: choose a suggested model or type one custom model name and press enter"
				},
				CompletionCommand: func(state map[string]string) string { return "connect-model:" + buildConnectWizardPayload(state) },
			},
			{
				Key: "context_window_tokens", HintLabel: "/connect context_window_tokens", Validate: ValidateInt,
				FreeformHint:      "/connect context_window_tokens: type integer and press enter",
				CompletionCommand: func(state map[string]string) string { return "connect-context:" + buildConnectWizardPayload(state) },
				ShouldSkip:        func(state map[string]string) bool { return state["_known_model"] == "true" },
			},
			{
				Key: "max_output_tokens", HintLabel: "/connect max_output_tokens", Validate: ValidateInt,
				FreeformHint:      "/connect max_output_tokens: type integer and press enter",
				CompletionCommand: func(state map[string]string) string { return "connect-maxout:" + buildConnectWizardPayload(state) },
				ShouldSkip:        func(state map[string]string) bool { return state["_known_model"] == "true" },
			},
			{
				Key: "reasoning_levels", HintLabel: "/connect reasoning_levels(csv)",
				FreeformHint: "/connect reasoning_levels(csv): e.g. low,medium (use - for empty)",
				CompletionCommand: func(state map[string]string) string {
					return "connect-reasoning-levels:" + buildConnectWizardPayload(state)
				},
				ShouldSkip: func(state map[string]string) bool { return state["_known_model"] == "true" },
			},
		},
		OnStepConfirm: confirmConnectModelStep,
		BuildExecLine: buildConnectModelExecLine,
	}
}

func connectACPWizard() WizardDef {
	return WizardDef{
		Command: "connect", DisplayLine: "/connect",
		Steps: []WizardStepDef{
			{
				Key: "acp_agent", HintLabel: "/connect ACP agent",
				FreeformHint:      "/connect ACP agent: choose Codex, Claude, or a custom local ACP command",
				RequireCandidate:  true,
				CompletionCommand: func(map[string]string) string { return "connect-acp-agent" },
			},
			{
				Key: "acp_launcher", HintLabel: "/connect launch",
				FreeformHint:     "/connect launch: choose how Caelis starts the local ACP agent",
				RequireCandidate: true,
				CompletionCommand: func(state map[string]string) string {
					return "connect-acp-launcher:" + strings.ToLower(strings.TrimSpace(state["acp_agent"]))
				},
			},
			{
				Key: "acp_command", HintLabel: "/connect ACP command", NoCompletion: true,
				FreeformHint: "/connect ACP command: choose an installed executable or type an absolute command path",
				ShouldSkip: func(state map[string]string) bool {
					launcher := strings.ToLower(strings.TrimSpace(state["acp_launcher"]))
					return launcher == "npx" || launcher == "global" || launcher == "managed" || launcher == "installed"
				},
			},
			{
				Key: "acp_model", HintLabel: "/connect ACP model",
				FreeformHint:     "/connect ACP model: wait for discovery, then pick one model",
				RequireCandidate: true,
				CompletionCommand: func(state map[string]string) string {
					return "connect-acp-model:" + buildACPConnectWizardPayload(state)
				},
			},
			{
				Key: "acp_config", HintLabel: "/connect ACP defaults · tab adds · enter confirms",
				FreeformHint:     "/connect ACP defaults: choose reasoning/config defaults or keep the Agent default",
				RequireCandidate: true, MultiSelect: true,
				MergeMultiSelect:  mergeACPConfigSelection,
				FormatMultiSelect: formatACPConfigSelections,
				CompletionCommand: func(state map[string]string) string {
					return "connect-acp-config:" + buildACPConnectWizardPayload(state)
				},
			},
		},
		BuildExecLine: func(state map[string]string) string { return "/connect acp " + buildACPConnectWizardPayload(state) },
	}
}

func disconnectACPWizard() WizardDef {
	return WizardDef{
		Command: "connect", DisplayLine: "/connect",
		Steps: []WizardStepDef{
			{
				Key: "disconnect_agent", HintLabel: "/connect disconnect Agent",
				FreeformHint: "/connect disconnect: choose a connected local ACP Agent", RequireCandidate: true,
				CompletionCommand: func(map[string]string) string { return "connect-disconnect-agent" },
			},
			{
				Key: "disconnect_confirm", HintLabel: "/connect disconnect confirm",
				FreeformHint: "/connect disconnect: confirm the Agent to disconnect", RequireCandidate: true,
				CompletionCommand: func(state map[string]string) string {
					return "connect-disconnect-confirm:" + strings.TrimSpace(state["disconnect_agent"])
				},
			},
		},
		BuildExecLine: func(state map[string]string) string {
			return "/connect disconnect " + strings.TrimSpace(state["disconnect_agent"]) + " confirmed"
		},
	}
}

func confirmConnectModelStep(stepKey string, value string, candidate *SlashArgCandidate, state map[string]string) {
	switch stepKey {
	case "provider":
		state["provider"] = strings.ToLower(strings.TrimSpace(value))
		delete(state, "_reuseauth")
		delete(state, "_noauth")
		if candidate != nil && candidate.NoAuth {
			state["_noauth"] = "true"
		}
	case "endpoint":
		state["baseurl"] = strings.TrimSpace(value)
		if candidate != nil && candidate.NoAuth {
			state["_reuseauth"] = "true"
		} else {
			delete(state, "_reuseauth")
		}
	case "baseurl":
		if candidate != nil && candidate.NoAuth {
			state["_reuseauth"] = "true"
		} else {
			delete(state, "_reuseauth")
		}
	case "model":
		if candidate != nil && candidate.ModelMetadataComplete {
			state["_known_model"] = "true"
		} else {
			delete(state, "_known_model")
		}
	}
}

func buildConnectModelExecLine(state map[string]string) string {
	apiKey := strings.TrimSpace(state["apikey"])
	if apiKey == "" {
		apiKey = "-"
	}
	reasoningLevels := strings.TrimSpace(state["reasoning_levels"])
	if reasoningLevels == "" {
		reasoningLevels = "-"
		if state["_known_model"] == "true" {
			reasoningLevels = "auto"
		}
	}
	return joinNonEmpty([]string{
		"/connect", state["provider"], state["model"], emptyAsDash(state["baseurl"]),
		connectWizardTimeout(), apiKey, emptyAsDash(state["context_window_tokens"]),
		emptyAsDash(state["max_output_tokens"]), reasoningLevels,
	}, " ")
}

func mergeACPConfigSelection(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "default") {
		return []string{"default"}
	}
	configID, _, hasValue := strings.Cut(value, "=")
	configID = strings.TrimSpace(configID)
	next := make([]string, 0, len(values)+1)
	for _, existing := range values {
		existing = strings.TrimSpace(existing)
		if existing == "" || strings.EqualFold(existing, "default") {
			continue
		}
		existingID, _, existingHasValue := strings.Cut(existing, "=")
		if hasValue && existingHasValue && strings.EqualFold(strings.TrimSpace(existingID), configID) {
			continue
		}
		next = appendUniqueWizardValue(next, existing)
	}
	return appendUniqueWizardValue(next, value)
}

func formatACPConfigSelections(values []string) string {
	configValues := map[string]string{}
	for _, item := range values {
		item = strings.TrimSpace(item)
		if item == "" || strings.EqualFold(item, "default") {
			continue
		}
		configID, value, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(configID) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		configValues[strings.TrimSpace(configID)] = strings.TrimSpace(value)
	}
	payload, _ := json.Marshal(configValues)
	return string(payload)
}

func parseACPConfigSelections(raw string) map[string]string {
	var configValues map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &configValues); err != nil {
		return nil
	}
	return controlagents.NormalizeSessionOptions(controlagents.SessionOptions{ConfigValues: configValues}).ConfigValues
}

func buildACPConnectWizardPayload(state map[string]string) string {
	return controlagents.EncodeConnectState(controlagents.ConnectState{
		Agent:        strings.ToLower(strings.TrimSpace(state["acp_agent"])),
		Launcher:     controlagents.LauncherChoice(strings.ToLower(strings.TrimSpace(state["acp_launcher"]))),
		CommandLine:  strings.TrimSpace(state["acp_command"]),
		Model:        strings.TrimSpace(state["acp_model"]),
		ConfigValues: parseACPConfigSelections(state["acp_config"]),
	})
}

func parseACPConnectWizardPayload(raw string) (controlagents.ConnectState, error) {
	return controlagents.DecodeConnectState(raw)
}

func connectWizardSelectedModelCount(state map[string]string) int {
	if state == nil {
		return 0
	}
	count := 0
	seen := map[string]struct{}{}
	for _, value := range strings.Split(state["model"], ",") {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		count++
	}
	return count
}

func connectWizardTokenEnvHint(state map[string]string) string {
	if env := modelconfig.DefaultTokenEnv(state["provider"], state["baseurl"]); env != "" {
		return env
	}
	return "YOUR_API_KEY"
}

func connectWizardProviderHasEndpointStep(provider string) bool {
	template, ok := modelconfig.LookupProvider(provider)
	return ok && len(template.Endpoints) > 0
}

func connectWizardProviderHasBaseURLStep(provider string) bool {
	template, ok := modelconfig.LookupProvider(provider)
	return ok && template.PromptForBaseURL
}

func buildConnectWizardPayload(state map[string]string) string {
	return connectwizard.ConnectWizardStateFromMap(state).EncodeCompletionState()
}

func connectWizardTimeout() string {
	return strconv.Itoa(connectwizard.DefaultConnectTimeoutSeconds)
}

func emptyAsDash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}
