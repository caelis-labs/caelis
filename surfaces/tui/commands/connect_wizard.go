package commands

import (
	"net/url"
	"strconv"
	"strings"
)

const DefaultConnectTimeoutSeconds = 60

// ConnectWizardState is the structured state carried between connect wizard
// steps and driver completions.
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

// ConnectWizardStateFromMap converts the TUI wizard state map into the
// canonical structured state.
func ConnectWizardStateFromMap(state map[string]string) ConnectWizardState {
	if state == nil {
		return ConnectWizardState{TimeoutSeconds: DefaultConnectTimeoutSeconds}
	}
	tokenRef := strings.TrimSpace(state["apikey"])
	return ConnectWizardState{
		Provider:            strings.TrimSpace(state["provider"]),
		BaseURL:             strings.TrimSpace(state["baseurl"]),
		TimeoutSeconds:      parsePositiveInt(state["timeout"], DefaultConnectTimeoutSeconds),
		AuthMode:            authModeForTokenRef(tokenRef),
		TokenRef:            tokenRef,
		Model:               strings.TrimSpace(state["model"]),
		ContextWindowTokens: parsePositiveInt(state["context_window_tokens"], 0),
		MaxOutputTokens:     parsePositiveInt(state["max_output_tokens"], 0),
		ReasoningLevels:     parseReasoningLevels(state["reasoning_levels"]),
	}
}

// EncodeCompletionPayload preserves the legacy slash-completion command payload
// shape while keeping construction centralized.
func (s ConnectWizardState) EncodeCompletionPayload() string {
	timeout := s.TimeoutSeconds
	if timeout <= 0 {
		timeout = DefaultConnectTimeoutSeconds
	}
	return strings.TrimSpace(s.Provider) +
		"|" + url.QueryEscape(strings.TrimSpace(s.BaseURL)) +
		"|" + strconv.Itoa(timeout) +
		"|" + url.QueryEscape(strings.TrimSpace(s.TokenRef)) +
		"|" + url.QueryEscape(strings.TrimSpace(s.Model))
}

// ParseConnectWizardPayload decodes the legacy slash-completion command
// payload into structured wizard state.
func ParseConnectWizardPayload(raw string) ConnectWizardState {
	parts := strings.SplitN(raw, "|", 5)
	for len(parts) < 5 {
		parts = append(parts, "")
	}
	tokenRef := decodeConnectWizardPart(parts[3])
	return ConnectWizardState{
		Provider:       strings.TrimSpace(parts[0]),
		BaseURL:        decodeConnectWizardPart(parts[1]),
		TimeoutSeconds: parsePositiveInt(parts[2], DefaultConnectTimeoutSeconds),
		AuthMode:       authModeForTokenRef(tokenRef),
		TokenRef:       tokenRef,
		Model:          decodeConnectWizardPart(parts[4]),
	}
}

func parsePositiveInt(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func authModeForTokenRef(tokenRef string) string {
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

func parseReasoningLevels(raw string) []string {
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
