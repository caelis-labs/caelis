package statusbar

import (
	"strings"

	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/surfaces/internal/promptview"
)

// FooterYoloLabel is the persistent TUI footer badge for process-owned Host escape mode.
const FooterYoloLabel = "YOLO"

type ViewModel struct {
	Workspace       string
	Model           string
	Provider        string
	ReasoningEffort string
	FastMode        bool
	Mode            string
	Sandbox         string
	Route           string
	Security        string
	Tokens          string
	MissingAPIKey   bool
	Running         bool
	FullAccess      bool
}

func FromSnapshot(status controlstatus.StatusSnapshot) ViewModel {
	modelDisplay := promptview.ModelDisplayFromStatus(status.ModelStatus)
	sandbox := firstNonEmpty(strings.TrimSpace(status.SandboxStatus.ResolvedBackend), strings.TrimSpace(status.SandboxStatus.Type), strings.TrimSpace(status.SandboxStatus.RequestedBackend), "auto")
	security := strings.TrimSpace(status.SandboxStatus.SecuritySummary)
	switch {
	case status.SandboxStatus.FullAccessMode:
		security = firstNonEmpty(security, "full access")
	case status.SandboxStatus.HostExecution:
		security = firstNonEmpty(security, "host")
	}
	return ViewModel{
		Workspace:       strings.TrimSpace(status.Session.Workspace),
		Model:           modelDisplay.Model,
		Provider:        modelDisplay.Provider,
		ReasoningEffort: modelDisplay.ReasoningEffort,
		FastMode:        modelDisplay.FastMode,
		Mode:            firstNonEmpty(strings.TrimSpace(status.Session.ModeLabel), strings.TrimSpace(status.Session.SessionMode), "auto-review"),
		Sandbox:         sandbox,
		Route:           strings.TrimSpace(status.SandboxStatus.Route),
		Security:        security,
		Tokens:          promptview.FormatContextUsage(status.Usage.TotalTokens, status.Usage.ContextWindowTokens),
		MissingAPIKey:   modelDisplay.MissingAPIKey,
		Running:         status.Runtime.Running,
		FullAccess:      status.SandboxStatus.FullAccessMode,
	}
}

func (s ViewModel) HeaderModelText(fallback string) string {
	return (promptview.ModelDisplay{
		Model:           s.Model,
		Provider:        s.Provider,
		ReasoningEffort: s.ReasoningEffort,
		FastMode:        s.FastMode,
		MissingAPIKey:   s.MissingAPIKey,
	}).Text(fallback)
}

func (s ViewModel) FooterModeText(fallback string) string {
	return firstNonEmpty(strings.TrimSpace(s.Mode), strings.TrimSpace(fallback))
}

// FooterYoloText returns the compact YOLO badge when full-access mode is active.
func (s ViewModel) FooterYoloText() string {
	if !s.FullAccess {
		return ""
	}
	return FooterYoloLabel
}

func (s ViewModel) FooterContextText(fallback string) string {
	tokens := strings.TrimSpace(s.Tokens)
	if tokens == "" {
		return strings.TrimSpace(fallback)
	}
	return tokens
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
