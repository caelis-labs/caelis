package tuiapp

import (
	"net/url"
	"strings"
)

// defaults.go provides DefaultCommands and DefaultWizards for the TUI shell.

const defaultConnectTimeoutSeconds = 60

var connectWizardEndpointProviders = map[string]struct{}{
	"volcengine": {},
	"xiaomi":     {},
}

var connectWizardBaseURLProviders = map[string]struct{}{
	"openai-compatible":    {},
	"anthropic-compatible": {},
}

var connectWizardTokenEnvByProvider = map[string]string{
	"minimax":              "MINIMAX_API_KEY",
	"openai":               "OPENAI_API_KEY",
	"openai-compatible":    "OPENAI_COMPATIBLE_API_KEY",
	"openrouter":           "OPENROUTER_API_KEY",
	"gemini":               "GEMINI_API_KEY",
	"anthropic":            "ANTHROPIC_API_KEY",
	"anthropic-compatible": "ANTHROPIC_COMPATIBLE_API_KEY",
	"deepseek":             "DEEPSEEK_API_KEY",
	"xiaomi":               "XIAOMI_API_KEY",
	"volcengine":           "VOLCENGINE_API_KEY",
}

var connectWizardTokenEnvByEndpoint = map[string]string{
	"xiaomi|https://api.xiaomimimo.com/v1":                       "XIAOMI_API_KEY",
	"xiaomi|https://token-plan-cn.xiaomimimo.com/v1":             "MIMO_TOKEN_PLAN_API_KEY",
	"volcengine|https://ark.cn-beijing.volces.com/api/v3":        "VOLCENGINE_API_KEY",
	"volcengine|https://ark.cn-beijing.volces.com/api/coding/v3": "VOLCENGINE_API_KEY",
}

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
		{Name: "approval", Usage: "/approval [auto-review|manual]", Description: "Inspect or change approval review mode"},
		{Name: "sandbox", Usage: "/sandbox [auto|seatbelt|bwrap|landlock]", Description: "Inspect or change the sandbox backend"},
		{Name: "status", Usage: "/status", Description: "Show current provider, model, session, sandbox, and store info"},
		{Name: "doctor", Usage: "/doctor", Description: "Diagnose provider, model, session store, and sandbox readiness"},
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

func connectWizardTokenEnvHint(state map[string]string) string {
	provider := strings.ToLower(strings.TrimSpace(state["provider"]))
	baseURL := strings.ToLower(strings.TrimRight(strings.TrimSpace(state["baseurl"]), "/"))
	if env := connectWizardTokenEnvByEndpoint[provider+"|"+baseURL]; env != "" {
		return env
	}
	if env := connectWizardTokenEnvForKnownEndpoint(provider, baseURL); env != "" {
		return env
	}
	if env := connectWizardTokenEnvByProvider[provider]; env != "" {
		return env
	}
	return "YOUR_API_KEY"
}

func connectWizardTokenEnvForKnownEndpoint(provider string, baseURL string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	host := ""
	if parsed, err := url.Parse(strings.TrimSpace(baseURL)); err == nil {
		host = strings.ToLower(strings.TrimSpace(parsed.Host))
	}
	if host == "" {
		host = strings.ToLower(strings.TrimSpace(strings.Split(strings.TrimPrefix(baseURL, "//"), "/")[0]))
	}
	switch provider {
	case "xiaomi":
		switch host {
		case "api.xiaomimimo.com":
			return "XIAOMI_API_KEY"
		case "token-plan-cn.xiaomimimo.com":
			return "MIMO_TOKEN_PLAN_API_KEY"
		}
	case "volcengine":
		if host == "ark.cn-beijing.volces.com" {
			return "VOLCENGINE_API_KEY"
		}
	}
	return ""
}

func connectWizardProviderHasEndpointStep(provider string) bool {
	_, ok := connectWizardEndpointProviders[strings.ToLower(strings.TrimSpace(provider))]
	return ok
}

func connectWizardProviderHasBaseURLStep(provider string) bool {
	_, ok := connectWizardBaseURLProviders[strings.ToLower(strings.TrimSpace(provider))]
	return ok
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
