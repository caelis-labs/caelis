package tuiapp

import (
	"strconv"
	"strings"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/controlprompt/connectwizard"
)

// defaults.go provides DefaultCommands and DefaultWizards for the TUI shell.

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
	return controlprompt.DefaultNames()
}

const defaultExternalCommandCompletionDetail = "Send a prompt to the registered ACP agent"

// cacheCommandCompletionDetails resolves configured command descriptions when
// the roster changes so completion rendering never rebuilds the Control
// command registry for every visible row and frame.
func (m *Model) cacheCommandCompletionDetails(commands []string) {
	if m == nil {
		return
	}
	if m.cfg.CommandDetails == nil {
		m.cfg.CommandDetails = make(map[string]string, len(commands))
	}
	for _, command := range commands {
		name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(command, "/")))
		if name == "" || strings.TrimSpace(m.cfg.CommandDetails[name]) != "" {
			continue
		}
		detail := defaultExternalCommandCompletionDetail
		if spec, ok := controlprompt.Lookup(name); ok {
			detail = strings.TrimSpace(spec.Description)
		}
		m.cfg.CommandDetails[name] = detail
	}
}

func (m *Model) commandCompletionDetail(command string) string {
	name := strings.TrimPrefix(strings.TrimSpace(command), "/")
	if name == "theme" {
		return "Change terminal theme"
	}
	if name == "" {
		return ""
	}
	if m != nil {
		if detail := strings.TrimSpace(m.slashDetails[strings.ToLower(name)]); detail != "" {
			return detail
		}
		if detail := strings.TrimSpace(m.cfg.CommandDetails[strings.ToLower(name)]); detail != "" {
			return detail
		}
	}
	if spec, ok := controlprompt.Lookup(name); ok {
		return strings.TrimSpace(spec.Description)
	}
	return defaultExternalCommandCompletionDetail
}

// DefaultWizards returns the set of multi-step wizard flows for the TUI.
func DefaultWizards() []WizardDef {
	return []WizardDef{
		connectWizard(),
		disconnectWizard(),
		pluginWizard(),
	}
}

func connectWizard() WizardDef {
	return WizardDef{
		Command:     "connect",
		DisplayLine: "/connect",
		Steps: []WizardStepDef{{
			Key:               "source",
			RequireCandidate:  true,
			CompletionCommand: func(map[string]string) string { return "connect" },
		}},
		Branch: func(_ string, value string, _ *SlashArgCandidate, state map[string]string) *WizardDef {
			source := strings.ToLower(strings.TrimSpace(value))
			state["source"] = source
			var next WizardDef
			switch source {
			case "account", "api-key":
				next = connectModelWizard(source)
			case "judgment":
				next = connectJudgmentWizard()
			case "acp":
				next = connectACPWizard()
			default:
				return nil
			}
			return &next
		},
	}
}

func connectModelWizard(authSource string) WizardDef {
	providerCommand := "connect-provider-" + authSource
	return WizardDef{
		Command: "connect", DisplayLine: "/connect",
		Steps: []WizardStepDef{
			{
				Key:               "provider",
				RequireCandidate:  true,
				CompletionCommand: func(map[string]string) string { return providerCommand },
			},
			{
				Key:               "endpoint",
				CompletionCommand: func(state map[string]string) string { return "connect-baseurl:" + state["provider"] },
				ShouldSkip:        func(state map[string]string) bool { return !connectWizardProviderHasEndpointStep(state["provider"]) },
			},
			{
				Key:               "baseurl",
				CompletionCommand: func(state map[string]string) string { return "connect-baseurl:" + state["provider"] },
				ShouldSkip:        func(state map[string]string) bool { return !connectWizardProviderHasBaseURLStep(state["provider"]) },
			},
			{
				Key: "apikey", HideInput: true,
				CompletionCommand: func(state map[string]string) string { return "connect-apikey:" + state["provider"] },
				ShouldSkip:        func(state map[string]string) bool { return state["_noauth"] == "true" || state["_reuseauth"] == "true" },
			},
			{
				Key: "model", MultiSelect: true,
				MultiSelectCandidate: func(candidate SlashArgCandidate) bool {
					return candidate.ModelMetadataComplete && candidate.ModelImageInputKnown
				},
				CompletionCommand: func(state map[string]string) string { return "connect-model:" + buildConnectWizardPayload(state) },
			},
			{
				Key:              "image_input",
				RequireCandidate: true,
				CompletionCommand: func(state map[string]string) string {
					return "connect-image-input:" + buildConnectWizardPayload(state)
				},
				ShouldSkip: func(state map[string]string) bool { return state["_known_image_input"] == "true" },
			},
			{
				Key: "context_window_tokens", Validate: ValidateInt,
				CompletionCommand: func(state map[string]string) string { return "connect-context:" + buildConnectWizardPayload(state) },
				ShouldSkip:        func(state map[string]string) bool { return state["_known_model"] == "true" },
			},
			{
				Key: "max_output_tokens", Validate: ValidateInt,
				CompletionCommand: func(state map[string]string) string { return "connect-maxout:" + buildConnectWizardPayload(state) },
				ShouldSkip:        func(state map[string]string) bool { return state["_known_model"] == "true" },
			},
			{
				Key: "reasoning_levels",
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
				Key:               "acp_agent",
				RequireCandidate:  true,
				CompletionCommand: func(map[string]string) string { return "connect-acp-agent" },
			},
			{
				Key:              "acp_launcher",
				RequireCandidate: true,
				CompletionCommand: func(state map[string]string) string {
					return "connect-acp-launcher:" + strings.ToLower(strings.TrimSpace(state["acp_agent"]))
				},
			},
			{
				Key: "acp_install",
				ShouldSkip: func(state map[string]string) bool {
					return state["acp_launcher"] != "install" && state["acp_launcher"] != "manual"
				},
				CompletionCommand: func(state map[string]string) string { return "connect-acp-install:" + state["acp_agent"] },
			},
			{
				Key: "acp_command", NoCompletion: true,
				ShouldSkip: func(state map[string]string) bool {
					launcher := strings.ToLower(strings.TrimSpace(state["acp_launcher"]))
					return launcher == "installed" || launcher == "hosted"
				},
			},
			{
				Key:              "acp_model",
				RequireCandidate: true,
				CompletionCommand: func(state map[string]string) string {
					return "connect-acp-model:" + buildACPConnectWizardPayload(state)
				},
			},
		},
		OnStepConfirm: confirmACPSetupStep,
		BuildExecLine: func(state map[string]string) string { return "/connect acp " + buildACPConnectWizardPayload(state) },
	}
}

func disconnectWizard() WizardDef {
	return WizardDef{
		Command:     "disconnect",
		DisplayLine: "/disconnect",
		Steps: []WizardStepDef{{
			Key:               "kind",
			RequireCandidate:  true,
			CompletionCommand: func(map[string]string) string { return "disconnect" },
		}},
		Branch: func(_ string, value string, _ *SlashArgCandidate, state map[string]string) *WizardDef {
			kind := strings.ToLower(strings.TrimSpace(value))
			state["kind"] = kind
			var next WizardDef
			switch kind {
			case "provider":
				next = disconnectProviderWizard()
			case "acp":
				next = disconnectACPWizard()
			default:
				return nil
			}
			return &next
		},
	}
}

func disconnectProviderWizard() WizardDef {
	return WizardDef{
		Command: "disconnect", DisplayLine: "/disconnect",
		Steps: []WizardStepDef{{
			Key: "provider_model", MultiSelect: true,
			RequireCandidate:  true,
			CompletionCommand: func(map[string]string) string { return "disconnect-provider" },
		}},
		BuildExecLine: func(state map[string]string) string {
			return "/disconnect provider " + strings.TrimSpace(state["provider_model"])
		},
	}
}

func disconnectACPWizard() WizardDef {
	return WizardDef{
		Command: "disconnect", DisplayLine: "/disconnect",
		Steps: []WizardStepDef{{
			Key: "disconnect_agent", MultiSelect: true,
			RequireCandidate:  true,
			CompletionCommand: func(map[string]string) string { return "disconnect-acp" },
		}},
		BuildExecLine: func(state map[string]string) string {
			return "/disconnect acp " + strings.TrimSpace(state["disconnect_agent"])
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
		if candidate != nil && candidate.NoAuth ||
			modelconfig.EndpointUsesNoAuth(state["provider"], value) {
			state["_reuseauth"] = "true"
		} else {
			delete(state, "_reuseauth")
		}
	case "baseurl":
		if candidate != nil && candidate.NoAuth ||
			modelconfig.EndpointUsesNoAuth(state["provider"], value) {
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
		if candidate != nil && candidate.ModelImageInputKnown {
			state["_known_image_input"] = "true"
		} else {
			delete(state, "_known_image_input")
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
	fields := []string{
		"/connect", state["provider"], state["model"], emptyAsDash(state["baseurl"]),
		connectWizardTimeout(), apiKey, emptyAsDash(state["context_window_tokens"]),
		emptyAsDash(state["max_output_tokens"]), reasoningLevels,
	}
	if imageInput := strings.TrimSpace(state["image_input"]); imageInput != "" {
		// Keep the existing ninth positional argument reserved for the stream
		// first-event timeout; image input is the backward-compatible tenth.
		fields = append(fields, "-", imageInput)
	}
	return joinNonEmpty(fields, " ")
}

func buildACPConnectWizardPayload(state map[string]string) string {
	return controlagents.EncodeConnectState(controlagents.ConnectState{
		Agent:       strings.ToLower(strings.TrimSpace(state["acp_agent"])),
		Launcher:    controlagents.LauncherChoice(strings.ToLower(strings.TrimSpace(state["acp_launcher"]))),
		CommandLine: strings.TrimSpace(state["acp_command"]),
		Model:       strings.TrimSpace(state["acp_model"]),
		Install:     confirmedACPInstallation(state),
	})
}

func parseACPConnectWizardPayload(raw string) (controlagents.ConnectState, error) {
	return controlagents.DecodeConnectState(raw)
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
