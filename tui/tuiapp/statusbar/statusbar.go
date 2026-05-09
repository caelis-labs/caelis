package statusbar

import (
	"fmt"
	"strings"

	tuidriver "github.com/OnslaughtSnail/caelis/tui/driver"
)

type ViewModel struct {
	Workspace       string
	Model           string
	Provider        string
	ReasoningEffort string
	Mode            string
	Sandbox         string
	Route           string
	Security        string
	Jobs            int
	Tokens          string
	MissingAPIKey   bool
	Running         bool
}

func FromSnapshot(status tuidriver.StatusSnapshot) ViewModel {
	model := firstNonEmpty(strings.TrimSpace(status.Model), strings.TrimSpace(status.ModelName), "not configured")
	provider := firstNonEmpty(strings.TrimSpace(status.Provider), deriveProviderFromAlias(status.Model), "not configured")
	sandbox := firstNonEmpty(strings.TrimSpace(status.SandboxResolvedBackend), strings.TrimSpace(status.SandboxType), strings.TrimSpace(status.SandboxRequestedBackend), "auto")
	security := strings.TrimSpace(status.SecuritySummary)
	switch {
	case status.FullAccessMode:
		security = firstNonEmpty(security, "full access")
	case status.HostExecution:
		security = firstNonEmpty(security, "host")
	}
	jobs := 0
	if status.ActiveJobs > 0 {
		jobs = status.ActiveJobs
	} else if status.Running {
		jobs = 1
	}
	return ViewModel{
		Workspace:       strings.TrimSpace(status.Workspace),
		Model:           model,
		Provider:        provider,
		ReasoningEffort: strings.TrimSpace(status.ReasoningEffort),
		Mode:            firstNonEmpty(strings.TrimSpace(status.ModeLabel), strings.TrimSpace(status.SessionMode), "auto-review"),
		Sandbox:         sandbox,
		Route:           strings.TrimSpace(status.Route),
		Security:        security,
		Jobs:            jobs,
		Tokens:          formatContextUsageStatus(status.TotalTokens, status.ContextWindowTokens),
		MissingAPIKey:   status.MissingAPIKey,
		Running:         status.Running,
	}
}

func (s ViewModel) HeaderModelText(fallback string) string {
	model := firstNonEmpty(strings.TrimSpace(s.Model), strings.TrimSpace(fallback), "not configured")
	provider := strings.TrimSpace(s.Provider)
	if provider != "" && provider != "not configured" && !strings.Contains(strings.ToLower(model), strings.ToLower(provider)+"/") {
		model = provider + "/" + model
	}
	if effort := strings.TrimSpace(s.ReasoningEffort); effort != "" && !strings.Contains(model, "["+effort+"]") {
		model += " [" + effort + "]"
	}
	if s.MissingAPIKey {
		model += " · key missing"
	}
	return strings.TrimSpace(model)
}

func (s ViewModel) FooterModeText(fallback string) string {
	parts := compactNonEmpty([]string{
		firstNonEmpty(strings.TrimSpace(s.Mode), strings.TrimSpace(fallback)),
		strings.TrimSpace(s.Sandbox),
		strings.TrimSpace(s.Route),
		strings.TrimSpace(s.Security),
	})
	return strings.Join(parts, " · ")
}

func (s ViewModel) FooterContextText(fallback string) string {
	parts := compactNonEmpty([]string{
		strings.TrimSpace(s.Tokens),
		jobsText(s.Jobs, s.Running),
	})
	if len(parts) == 0 {
		return strings.TrimSpace(fallback)
	}
	return strings.Join(parts, " · ")
}

func jobsText(jobs int, running bool) string {
	if jobs <= 0 && !running {
		return ""
	}
	if jobs <= 0 {
		jobs = 1
	}
	if jobs == 1 {
		return "1 job"
	}
	return fmt.Sprintf("%d jobs", jobs)
}

func formatContextUsageStatus(totalTokens, contextWindowTokens int) string {
	if contextWindowTokens <= 0 {
		if totalTokens <= 0 {
			return ""
		}
		return compactTokenCount(totalTokens)
	}
	if totalTokens < 0 {
		totalTokens = 0
	}
	percent := totalTokens * 100 / contextWindowTokens
	return fmt.Sprintf("%s/%s(%d%%)", compactTokenCount(totalTokens), compactTokenCount(contextWindowTokens), percent)
}

func compactTokenCount(tokens int) string {
	switch {
	case tokens >= 1_000_000:
		return fmt.Sprintf("%.1fm", float64(tokens)/1_000_000)
	case tokens >= 10_000:
		return fmt.Sprintf("%.0fk", float64(tokens)/1_000)
	case tokens >= 1_000:
		return fmt.Sprintf("%.1fk", float64(tokens)/1_000)
	default:
		return fmt.Sprintf("%d", tokens)
	}
}

func deriveProviderFromAlias(model string) string {
	model = strings.TrimSpace(model)
	if before, _, ok := strings.Cut(model, "/"); ok {
		return strings.TrimSpace(before)
	}
	return ""
}

func compactNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
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
