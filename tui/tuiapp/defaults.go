package tuiapp

import (
	"net/url"
	"strings"
)

// defaults.go provides DefaultCommands and DefaultWizards for the TUI shell.

const defaultConnectTimeoutSeconds = 60

type slashCommandSpec struct {
	Name        string
	Usage       string
	Description string
	Hidden      bool
}

func slashCommandSpecs() []slashCommandSpec {
	return []slashCommandSpec{
		{Name: "help", Usage: "/help", Description: "Show available slash commands"},
		{Name: "agent", Usage: "/agent list | /agent add <builtin> | /agent install <adapter> | /agent use <agent|local> | /agent remove <agent>", Description: "Manage registered ACP agents and main-controller switching"},
		{Name: "connect", Usage: "/connect", Description: "Open the guided model/provider setup wizard"},
		{Name: "model", Usage: "/model use <alias> | /model del <alias>", Description: "Switch or delete a configured model alias"},
		{Name: "sandbox", Usage: "/sandbox [auto|seatbelt|bwrap|landlock]", Description: "Inspect or change the sandbox backend"},
		{Name: "status", Usage: "/status", Description: "Show current provider, model, session, sandbox, and store info"},
		{Name: "new", Usage: "/new", Description: "Start a fresh session"},
		{Name: "resume", Usage: "/resume [session-id]", Description: "List recent sessions or resume one by id"},
		{Name: "compact", Usage: "/compact", Description: "Compact the current session transcript"},
		{Name: "exit", Usage: "/exit", Description: "Exit the TUI"},
		{Name: "quit", Usage: "/quit", Description: "Exit the TUI"},
	}
}

func visibleSlashCommandSpecs() []slashCommandSpec {
	specs := slashCommandSpecs()
	out := make([]slashCommandSpec, 0, len(specs))
	for _, spec := range specs {
		if spec.Hidden {
			continue
		}
		out = append(out, spec)
	}
	return out
}

func lookupSlashCommandSpec(name string) (slashCommandSpec, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, spec := range slashCommandSpecs() {
		if spec.Name == name {
			return spec, true
		}
	}
	return slashCommandSpec{}, false
}

func defaultHelpText() string {
	return helpTextForCommands(DefaultCommands())
}

func helpTextForCommands(commands []string) string {
	if len(commands) == 0 {
		commands = DefaultCommands()
	}
	lines := []string{"available commands:"}
	seen := map[string]struct{}{}
	for _, command := range commands {
		name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(command, "/")))
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		spec, known := lookupSlashCommandSpec(name)
		if !known {
			lines = append(lines, "  /"+name)
			continue
		}
		usage := strings.TrimSpace(spec.Usage)
		description := strings.TrimSpace(spec.Description)
		switch {
		case usage == "":
			lines = append(lines, "  /"+spec.Name)
		case description == "":
			lines = append(lines, "  "+usage)
		default:
			lines = append(lines, "  "+usage+"  "+description)
		}
	}
	return strings.Join(lines, "\n")
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
	specs := visibleSlashCommandSpecs()
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, spec.Name)
	}
	return out
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
				Key:          "provider",
				HintLabel:    "/connect provider",
				FreeformHint: "/connect provider: choose a provider; compatible endpoints may ask for a custom base URL",
				CompletionCommand: func(state map[string]string) string {
					return "connect"
				},
			},
			{
				Key:          "endpoint",
				HintLabel:    "/connect volcengine endpoint",
				FreeformHint: "/connect volcengine endpoint: choose standard or coding-plan, or paste a custom Ark base URL",
				CompletionCommand: func(state map[string]string) string {
					return "connect-baseurl:" + state["provider"]
				},
				ShouldSkip: func(state map[string]string) bool {
					return !strings.EqualFold(strings.TrimSpace(state["provider"]), "volcengine")
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
					switch strings.ToLower(strings.TrimSpace(state["provider"])) {
					case "openai-compatible", "anthropic-compatible":
						return false
					default:
						return true
					}
				},
			},
			{
				Key:          "apikey",
				HintLabel:    "/connect api_key",
				HideInput:    true,
				FreeformHint: "/connect api_key: paste a key, or type env:OPENAI_API_KEY to use an environment variable",
				CompletionCommand: func(state map[string]string) string {
					return "connect-apikey:" + state["provider"]
				},
				ShouldSkip: func(state map[string]string) bool {
					return state["_noauth"] == "true"
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
			}
			if stepKey == "provider" && candidate != nil && candidate.NoAuth {
				state["_noauth"] = "true"
			}
			if stepKey == "endpoint" {
				state["baseurl"] = strings.TrimSpace(value)
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
			apiKey := strings.TrimSpace(state["apikey"])
			if apiKey == "" {
				apiKey = "-"
			}
			reasoningLevels := strings.TrimSpace(state["reasoning_levels"])
			if reasoningLevels == "" {
				reasoningLevels = "-"
			}
			parts := []string{
				"/connect",
				state["provider"],
				state["model"],
				emptyAsDash(state["baseurl"]),
				connectWizardTimeout(),
				apiKey,
				emptyAsDash(state["context_window_tokens"]),
				emptyAsDash(state["max_output_tokens"]),
				reasoningLevels,
			}
			return joinNonEmpty(parts, " ")
		},
	}
}

func buildConnectWizardPayload(state map[string]string) string {
	return strings.TrimSpace(state["provider"]) +
		"|" + url.QueryEscape(strings.TrimSpace(state["baseurl"])) +
		"|" + connectWizardTimeout() +
		"|" + url.QueryEscape(strings.TrimSpace(state["apikey"])) +
		"|" + url.QueryEscape(strings.TrimSpace(state["model"]))
}

func connectWizardTimeout() string {
	return "60"
}

func emptyAsDash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}
