package services

import (
	"net/url"
	"strconv"
	"strings"
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
