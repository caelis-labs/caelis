package services

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

type ConnectWizardState struct {
	Provider            string
	BaseURL             string
	TimeoutSeconds      int
	AuthMode            string
	TokenRef            string
	Model               string
	ContextWindowTokens int
	MaxOutputTokens     int
	ReasoningLevels     []string
}

type ConnectWizardConfirmCandidate struct {
	Value  string
	NoAuth bool
}

func (s ModelService) ConnectWizard(context.Context) (appviewmodel.WizardFlowView, error) {
	return DefaultConnectWizardFlow(), nil
}

func DefaultConnectWizardFlow() appviewmodel.WizardFlowView {
	return appviewmodel.WizardFlowView{
		Command:     "connect",
		DisplayLine: "/connect",
		Steps: []appviewmodel.WizardStepView{
			{
				Key:              "provider",
				HintLabel:        "/connect provider",
				FreeformHint:     "/connect provider: choose a provider; compatible endpoints may ask for a custom base URL",
				RequireCandidate: true,
			},
			{
				Key:          "endpoint",
				HintLabel:    "/connect endpoint",
				FreeformHint: "/connect endpoint: choose a provider endpoint, or paste a custom base URL",
			},
			{
				Key:          "baseurl",
				HintLabel:    "/connect base_url",
				FreeformHint: "/connect base_url: choose the default compatible API root or paste your own full base URL",
			},
			{
				Key:                 "apikey",
				HintLabel:           "/connect api_key",
				HideInput:           true,
				DynamicFreeformHint: true,
			},
			{
				Key:          "model",
				HintLabel:    "/connect model",
				FreeformHint: "/connect model: choose a suggested model or type a custom model name and press enter",
			},
			{
				Key:          "context_window_tokens",
				HintLabel:    "/connect context_window_tokens",
				FreeformHint: "/connect context_window_tokens: type integer and press enter",
				Validator:    appviewmodel.WizardValidatorInt,
			},
			{
				Key:          "max_output_tokens",
				HintLabel:    "/connect max_output_tokens",
				FreeformHint: "/connect max_output_tokens: type integer and press enter",
				Validator:    appviewmodel.WizardValidatorInt,
			},
			{
				Key:          "reasoning_levels",
				HintLabel:    "/connect reasoning_levels(csv)",
				FreeformHint: "/connect reasoning_levels(csv): e.g. low,medium (use - for empty)",
			},
		},
	}
}

func ConnectWizardStateFromMap(state map[string]string) ConnectWizardState {
	if state == nil {
		return ConnectWizardState{TimeoutSeconds: ConnectDefaultTimeoutSeconds}
	}
	tokenRef := strings.TrimSpace(state["apikey"])
	return ConnectWizardState{
		Provider:            strings.TrimSpace(state["provider"]),
		BaseURL:             strings.TrimSpace(state["baseurl"]),
		TimeoutSeconds:      parseConnectPositiveInt(state["timeout"], ConnectDefaultTimeoutSeconds),
		AuthMode:            connectAuthModeForTokenRef(tokenRef),
		TokenRef:            tokenRef,
		Model:               strings.TrimSpace(state["model"]),
		ContextWindowTokens: parseConnectPositiveInt(state["context_window_tokens"], 0),
		MaxOutputTokens:     parseConnectPositiveInt(state["max_output_tokens"], 0),
		ReasoningLevels:     parseConnectReasoningLevels(state["reasoning_levels"]),
	}
}

func (s ConnectWizardState) EncodeCompletionPayload() string {
	timeout := s.TimeoutSeconds
	if timeout <= 0 {
		timeout = ConnectDefaultTimeoutSeconds
	}
	return strings.TrimSpace(s.Provider) +
		"|" + url.QueryEscape(strings.TrimSpace(s.BaseURL)) +
		"|" + strconv.Itoa(timeout) +
		"|" + url.QueryEscape(strings.TrimSpace(s.TokenRef)) +
		"|" + url.QueryEscape(strings.TrimSpace(s.Model))
}

func BuildConnectWizardExecLine(state map[string]string) string {
	if state == nil {
		state = map[string]string{}
	}
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
		connectEmptyAsDash(state["baseurl"]),
		strconv.Itoa(ConnectDefaultTimeoutSeconds),
		apiKey,
		connectEmptyAsDash(state["context_window_tokens"]),
		connectEmptyAsDash(state["max_output_tokens"]),
		reasoningLevels,
	}
	return strings.Join(connectNonEmpty(parts), " ")
}

func ConnectWizardCompletionCommand(stepKey string, state map[string]string) string {
	switch strings.TrimSpace(stepKey) {
	case "provider":
		return "connect"
	case "endpoint", "baseurl":
		return "connect-baseurl:" + connectWizardStateValue(state, "provider")
	case "apikey":
		return "connect-apikey:" + connectWizardStateValue(state, "provider")
	case "model":
		return "connect-model:" + ConnectWizardStateFromMap(state).EncodeCompletionPayload()
	case "context_window_tokens":
		return "connect-context:" + ConnectWizardStateFromMap(state).EncodeCompletionPayload()
	case "max_output_tokens":
		return "connect-maxout:" + ConnectWizardStateFromMap(state).EncodeCompletionPayload()
	case "reasoning_levels":
		return "connect-reasoning-levels:" + ConnectWizardStateFromMap(state).EncodeCompletionPayload()
	default:
		return ""
	}
}

func ConnectWizardShouldSkip(stepKey string, state map[string]string) bool {
	switch strings.TrimSpace(stepKey) {
	case "endpoint":
		return !ConnectWizardProviderHasEndpointStep(connectWizardStateValue(state, "provider"))
	case "baseurl":
		return !ConnectWizardProviderHasBaseURLStep(connectWizardStateValue(state, "provider"))
	case "apikey":
		return connectWizardStateValue(state, "_noauth") == "true" || connectWizardStateValue(state, "_reuseauth") == "true"
	case "context_window_tokens", "max_output_tokens", "reasoning_levels":
		return connectWizardStateValue(state, "_known_model") == "true"
	default:
		return false
	}
}

func ConnectWizardFreeformHint(stepKey string, state map[string]string) string {
	if strings.TrimSpace(stepKey) == "apikey" {
		return "/connect api_key: paste a key, or type env:" + ConnectWizardTokenEnvHint(state) + " to use an environment variable"
	}
	for _, step := range DefaultConnectWizardFlow().Steps {
		if step.Key == strings.TrimSpace(stepKey) {
			return step.FreeformHint
		}
	}
	return ""
}

func ConfirmConnectWizardStep(stepKey string, value string, candidate *ConnectWizardConfirmCandidate, state map[string]string) {
	if state == nil {
		return
	}
	switch strings.TrimSpace(stepKey) {
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
		if candidate != nil && strings.TrimSpace(candidate.Value) != "" && !strings.EqualFold(strings.TrimSpace(candidate.Value), "__custom_model__") {
			state["_known_model"] = "true"
		} else {
			delete(state, "_known_model")
		}
	}
}

func ParseConnectWizardPayload(raw string) ConnectWizardState {
	parts := strings.SplitN(raw, "|", 5)
	for len(parts) < 5 {
		parts = append(parts, "")
	}
	tokenRef := decodeConnectWizardPart(parts[3])
	return ConnectWizardState{
		Provider:       strings.TrimSpace(parts[0]),
		BaseURL:        decodeConnectWizardPart(parts[1]),
		TimeoutSeconds: parseConnectPositiveInt(parts[2], ConnectDefaultTimeoutSeconds),
		AuthMode:       connectAuthModeForTokenRef(tokenRef),
		TokenRef:       tokenRef,
		Model:          decodeConnectWizardPart(parts[4]),
	}
}

func ConnectWizardTokenEnvHint(state map[string]string) string {
	if env := DefaultConnectTokenEnvName(strings.TrimSpace(state["provider"]), strings.TrimSpace(state["baseurl"])); env != "" {
		return env
	}
	return "YOUR_API_KEY"
}

func ConnectWizardProviderHasEndpointStep(provider string) bool {
	tpl, ok := FindConnectProviderTemplate(provider)
	return ok && len(tpl.Endpoints) > 0
}

func ConnectWizardProviderHasBaseURLStep(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai-compatible", "anthropic-compatible":
		return true
	default:
		return false
	}
}

func parseConnectPositiveInt(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func connectAuthModeForTokenRef(tokenRef string) string {
	tokenRef = strings.TrimSpace(tokenRef)
	switch {
	case tokenRef == "":
		return ""
	case strings.HasPrefix(strings.ToLower(tokenRef), "env:"):
		return "env"
	default:
		return "token"
	}
}

func parseConnectReasoningLevels(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "-" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func connectWizardStateValue(state map[string]string, key string) string {
	if state == nil {
		return ""
	}
	return strings.TrimSpace(state[key])
}

func decodeConnectWizardPart(value string) string {
	decoded, err := url.QueryUnescape(strings.TrimSpace(value))
	if err != nil {
		return strings.TrimSpace(value)
	}
	return decoded
}

func connectEmptyAsDash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}

func connectNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}
