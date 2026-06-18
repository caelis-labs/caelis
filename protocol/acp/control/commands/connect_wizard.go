package commands

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

const DefaultConnectTimeoutSeconds = 60

// ConnectWizardState is the structured state carried between connect wizard
// steps and driver completions.
type ConnectWizardState struct {
	Provider            string   `json:"provider,omitempty"`
	BaseURL             string   `json:"base_url,omitempty"`
	TimeoutSeconds      int      `json:"timeout_seconds,omitempty"`
	AuthMode            string   `json:"auth_mode,omitempty"`
	TokenRef            string   `json:"token_ref,omitempty"`
	Model               string   `json:"model,omitempty"`
	ContextWindowTokens int      `json:"context_window_tokens,omitempty"`
	MaxOutputTokens     int      `json:"max_output_tokens,omitempty"`
	ReasoningLevels     []string `json:"reasoning_levels,omitempty"`
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

// EncodeCompletionState encodes structured wizard state for slash-completion
// continuation commands.
func (s ConnectWizardState) EncodeCompletionState() string {
	payload, err := json.Marshal(s.normalized())
	if err != nil {
		return ""
	}
	return url.QueryEscape(string(payload))
}

// ParseConnectWizardStatePayload decodes structured wizard state from a
// slash-completion continuation command.
func ParseConnectWizardStatePayload(raw string) ConnectWizardState {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ConnectWizardState{TimeoutSeconds: DefaultConnectTimeoutSeconds}
	}
	decoded, err := url.QueryUnescape(raw)
	if err != nil {
		return ConnectWizardState{TimeoutSeconds: DefaultConnectTimeoutSeconds}
	}
	var state ConnectWizardState
	if err := json.Unmarshal([]byte(decoded), &state); err != nil {
		return ConnectWizardState{TimeoutSeconds: DefaultConnectTimeoutSeconds}
	}
	return state.normalized()
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

func (s ConnectWizardState) normalized() ConnectWizardState {
	s.Provider = strings.TrimSpace(s.Provider)
	s.BaseURL = strings.TrimSpace(s.BaseURL)
	if s.TimeoutSeconds <= 0 {
		s.TimeoutSeconds = DefaultConnectTimeoutSeconds
	}
	s.TokenRef = strings.TrimSpace(s.TokenRef)
	s.AuthMode = authModeForTokenRef(s.TokenRef)
	s.Model = strings.TrimSpace(s.Model)
	if s.ContextWindowTokens < 0 {
		s.ContextWindowTokens = 0
	}
	if s.MaxOutputTokens < 0 {
		s.MaxOutputTokens = 0
	}
	if len(s.ReasoningLevels) > 0 {
		levels := make([]string, 0, len(s.ReasoningLevels))
		for _, level := range s.ReasoningLevels {
			if trimmed := strings.TrimSpace(level); trimmed != "" {
				levels = append(levels, trimmed)
			}
		}
		s.ReasoningLevels = levels
	}
	return s
}
