package controlprompt

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/caelis-labs/caelis/protocol/acp/control"
)

// ParseConnectArgs parses the non-interactive model-provider connect form.
func ParseConnectArgs(args string) control.ConnectConfig {
	parts := strings.Fields(args)
	cfg := control.ConnectConfig{}
	if len(parts) >= 1 {
		cfg.Provider = parts[0]
	}
	if len(parts) >= 2 {
		cfg.Model = parts[1]
	}
	if len(parts) >= 3 {
		cfg.BaseURL = dashAsEmpty(parts[2])
	}
	if len(parts) >= 4 {
		if timeout, err := strconv.Atoi(dashAsEmpty(parts[3])); err == nil {
			cfg.TimeoutSeconds = timeout
		}
	}
	if len(parts) >= 5 {
		secret := dashAsEmpty(parts[4])
		if strings.HasPrefix(strings.ToLower(secret), "env:") {
			cfg.TokenEnv = strings.TrimSpace(secret[len("env:"):])
		} else if strings.HasPrefix(secret, "$") {
			cfg.TokenEnv = strings.TrimSpace(strings.TrimPrefix(secret, "$"))
		} else {
			cfg.APIKey = secret
		}
	}
	if len(parts) >= 6 {
		if contextWindow, err := strconv.Atoi(dashAsEmpty(parts[5])); err == nil {
			cfg.ContextWindowTokens = contextWindow
		}
	}
	if len(parts) >= 7 {
		if maxOutput, err := strconv.Atoi(dashAsEmpty(parts[6])); err == nil {
			cfg.MaxOutputTokens = maxOutput
		}
	}
	if len(parts) >= 8 {
		cfg.ReasoningLevels = parseReasoningLevels(parts[7])
	}
	if len(parts) >= 9 {
		if timeout, err := strconv.Atoi(dashAsEmpty(parts[8])); err == nil {
			cfg.StreamFirstEventTimeoutSeconds = timeout
		}
	}
	if len(parts) == 4 && cfg.TimeoutSeconds == 0 && cfg.APIKey == "" && cfg.TokenEnv == "" {
		cfg.TokenEnv = dashAsEmpty(parts[3])
	}
	return cfg
}

// ModeToggleHint formats the result of cycling the session mode.
func ModeToggleHint(status control.StatusSnapshot) string {
	label := firstNonEmpty(strings.TrimSpace(status.Session.ModeLabel), strings.TrimSpace(status.Session.SessionMode), "auto-review")
	switch strings.ToLower(strings.TrimSpace(status.Session.SessionMode)) {
	case "manual":
		return "manual approval mode enabled"
	case "auto-review":
		return "auto-review approval mode enabled"
	default:
		return label + " mode enabled"
	}
}

// FriendlyCommandError adds actionable product guidance to command failures.
func FriendlyCommandError(action string, err error) error {
	if err == nil {
		return nil
	}
	raw := strings.TrimSpace(err.Error())
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "api key is missing"):
		return fmt.Errorf("%s: API key is missing. Use /connect and paste a key, or enter env:YOUR_API_KEY", action)
	case strings.Contains(lower, "base url is invalid"):
		return fmt.Errorf("%s: base URL is invalid. Use a full URL such as https://api.openai.com/v1", action)
	case strings.Contains(lower, "provider is not supported"), strings.Contains(lower, "unknown provider"):
		return fmt.Errorf("%s: provider is not supported. Run /connect and choose one of the listed providers", action)
	case strings.Contains(lower, "provider and model are required"), strings.Contains(lower, "model is required"):
		return fmt.Errorf("%s: provider or model is not configured. Run /connect to add one", action)
	case strings.Contains(lower, "unknown model alias"):
		return fmt.Errorf("%s: model alias was not found. Run /model and choose a configured alias, or use /connect first", action)
	case strings.Contains(lower, "ambiguous model alias"):
		return fmt.Errorf("%s: model alias is ambiguous. Type more of the alias or pick from /model", action)
	case strings.Contains(lower, "agent name is required"), strings.Contains(lower, "agent ") && (strings.Contains(lower, " is not configured") || strings.Contains(lower, " not found")):
		return fmt.Errorf("%s: Agent was not found. Run /connect to add an Agent", action)
	case strings.Contains(lower, "agent ") && strings.Contains(lower, " is ambiguous"):
		return fmt.Errorf("%s: Agent name is ambiguous. Type more of the Agent name", action)
	case strings.Contains(lower, "participant id is required"), strings.Contains(lower, "participant ") && strings.Contains(lower, " is not attached"):
		return fmt.Errorf("%s: Agent run was not found. Pick a /<agent>-<n> command from completion", action)
	case strings.Contains(lower, "participant ") && strings.Contains(lower, " is ambiguous"):
		return fmt.Errorf("%s: Agent run name is ambiguous", action)
	case strings.Contains(lower, "control plane is not available"), strings.Contains(lower, "acp controller backend is not configured"):
		return fmt.Errorf("%s: ACP control plane is not configured for this stack", action)
	case strings.Contains(lower, "unknown sandbox backend"), strings.Contains(lower, "unsupported by"):
		return fmt.Errorf("%s: sandbox backend is unavailable on this machine. Run /status to inspect sandbox readiness", action)
	case strings.Contains(lower, "uac prompt was cancelled"):
		return fmt.Errorf("%s: Windows sandbox repair was cancelled", action)
	case strings.Contains(lower, "acl: write") && strings.Contains(lower, "dacl"):
		return fmt.Errorf("%s: Windows sandbox cannot repair workspace ACLs with the current token. Run /doctor", action)
	case strings.Contains(lower, "windows sandbox setup is required"):
		return fmt.Errorf("%s: Windows sandbox repair is pending. Retry the command to let the sandbox repair workspace ACL state lazily", action)
	case strings.Contains(lower, "session not found"):
		return fmt.Errorf("%s: session could not be loaded. Run /resume to inspect available sessions", action)
	case strings.Contains(lower, "active turn"):
		return fmt.Errorf("%s: another turn is still running. Wait for it to finish or interrupt it before reconfiguring", action)
	default:
		return fmt.Errorf("%s: %w", action, err)
	}
}

// IsLocalAgentTarget reports whether target selects the built-in local Agent.
func IsLocalAgentTarget(target string) bool {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "local", "kernel", "main":
		return true
	default:
		return false
	}
}

func dashAsEmpty(value string) string {
	value = strings.TrimSpace(value)
	if value == "-" {
		return ""
	}
	return value
}

func parseReasoningLevels(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "auto") {
		return nil
	}
	if raw == "-" {
		return []string{}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.ToLower(strings.TrimSpace(part)); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
