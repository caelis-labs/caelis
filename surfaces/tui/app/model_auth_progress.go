package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/control/modelconfig"
)

type modelAuthProgressMsg struct {
	seq      uint64
	progress modelconfig.AuthProgress
}

func (m *Model) handleModelAuthProgress(msg modelAuthProgressMsg) {
	if m == nil || msg.seq != m.slashArgLoadSeq || !m.slashArgLoadPending {
		return
	}
	progress := msg.progress
	m.slashArgLoadAuthURL = strings.TrimSpace(progress.VerificationURL)
	m.slashArgLoadAuthCode = strings.TrimSpace(progress.UserCode)
	switch progress.Phase {
	case modelconfig.AuthProgressOpeningBrowser:
		m.slashArgLoadLabel = "Opening your browser for Codex sign-in"
	case modelconfig.AuthProgressWaitingForBrowser:
		m.slashArgLoadLabel = "Finish signing in to Codex in your browser"
	case modelconfig.AuthProgressRequestingDeviceCode:
		m.slashArgLoadLabel = "Preparing Codex device-code sign-in"
	case modelconfig.AuthProgressWaitingForDevice:
		m.slashArgLoadLabel = "Finish Codex device-code sign-in"
	case modelconfig.AuthProgressAuthenticated:
		m.slashArgLoadLabel = "Codex sign-in complete; loading models"
	default:
		if detail := strings.TrimSpace(progress.Detail); detail != "" {
			m.slashArgLoadLabel = detail
		}
	}
}

func (m *Model) renderModelAuthDrawer() string {
	if m == nil || !m.slashArgLoadPending || strings.TrimSpace(m.slashArgLoadAuthURL) == "" || m.width <= 0 {
		return ""
	}
	contentWidth := maxInt(1, m.mainColumnWidth()-(inputHorizontalInset*2))
	lines := []string{m.theme.SeparatorStyle().Render(strings.Repeat("─", contentWidth))}
	verificationURL := strings.TrimSpace(m.slashArgLoadAuthURL)
	if code := strings.TrimSpace(m.slashArgLoadAuthCode); code != "" {
		lines = append(lines,
			m.theme.TextStyle().Bold(true).Render("Finish signing in with a device code"),
			m.theme.HelpHintTextStyle().Render("Open: ")+m.theme.TextStyle().Render(verificationURL),
			m.theme.HelpHintTextStyle().Render("Enter code: ")+m.theme.TextStyle().Bold(true).Render(code)+m.theme.HelpHintTextStyle().Render("  (expires in 15 minutes)"),
			m.theme.HelpHintTextStyle().Render("Continue only if you started this login in Caelis. Esc cancels."),
		)
	} else {
		lines = append(lines,
			m.theme.TextStyle().Bold(true).Render("Finish signing in via your browser"),
			m.theme.HelpHintTextStyle().Render("If the browser did not open automatically, open:"),
			m.theme.TextStyle().Render(verificationURL),
			m.theme.HelpHintTextStyle().Render("Waiting for the OAuth callback. Esc cancels."),
		)
	}
	return insetRenderedBlock(strings.Join(wrapBTWContentLines(lines, contentWidth), "\n"), inputHorizontalInset)
}
