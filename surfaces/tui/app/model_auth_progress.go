package tuiapp

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/control/modelconfig"
)

type modelAuthProgressMsg struct {
	seq      uint64
	progress modelconfig.AuthProgress
}

type modelAuthInputRequestMsg struct {
	seq      uint64
	request  modelconfig.AuthInputRequest
	response chan PromptResponse
}

type modelAuthInputCancelMsg struct {
	seq      uint64
	response chan PromptResponse
}

func (m *Model) handleModelAuthProgress(msg modelAuthProgressMsg) {
	if m == nil || msg.seq != m.slashArgLoadSeq || !m.slashArgLoadPending {
		return
	}
	progress := msg.progress
	provider := modelAuthProviderDisplayName(progress.Provider)
	m.slashArgLoadAuthURL = strings.TrimSpace(progress.VerificationURL)
	m.slashArgLoadAuthCode = strings.TrimSpace(progress.UserCode)
	switch progress.Phase {
	case modelconfig.AuthProgressOpeningBrowser:
		m.slashArgLoadLabel = "Opening your browser for " + provider + " sign-in"
	case modelconfig.AuthProgressWaitingForBrowser:
		m.slashArgLoadLabel = "Finish signing in to " + provider + " in your browser"
	case modelconfig.AuthProgressRequestingDeviceCode:
		m.slashArgLoadLabel = "Preparing " + provider + " device-code sign-in"
	case modelconfig.AuthProgressWaitingForDevice:
		m.slashArgLoadLabel = "Finish " + provider + " device-code sign-in"
	case modelconfig.AuthProgressAuthenticated:
		m.slashArgLoadLabel = provider + " sign-in complete; loading models"
	default:
		if detail := strings.TrimSpace(progress.Detail); detail != "" {
			m.slashArgLoadLabel = detail
		}
	}
}

func (m *Model) handleModelAuthInputRequest(msg modelAuthInputRequestMsg) {
	if msg.response == nil {
		return
	}
	if m == nil || msg.seq != m.slashArgLoadSeq || !m.slashArgLoadPending {
		msg.response <- PromptResponse{Err: context.Canceled}
		return
	}
	provider := modelAuthProviderDisplayName(msg.request.Provider)
	m.slashArgLoadAuthPrompt = msg.response
	prompt := strings.TrimSpace(msg.request.Prompt)
	if prompt == "" {
		prompt = provider + " authorization code or callback URL"
	}
	m.enqueuePrompt(PromptRequestMsg{
		Title:    provider + " sign-in",
		Prompt:   prompt,
		Secret:   msg.request.Secret,
		Response: msg.response,
	})
	m.ensureViewportLayout()
}

func (m *Model) handleModelAuthInputCancel(msg modelAuthInputCancelMsg) {
	if m == nil || msg.response == nil {
		return
	}
	if m.activePrompt != nil && m.activePrompt.response == msg.response {
		m.finishPrompt("", context.Canceled)
		return
	}
	for index, pending := range m.pendingPrompt {
		if pending.Response != msg.response {
			continue
		}
		m.pendingPrompt = append(m.pendingPrompt[:index], m.pendingPrompt[index+1:]...)
		msg.response <- PromptResponse{Err: context.Canceled}
		if m.slashArgLoadAuthPrompt == msg.response {
			m.slashArgLoadAuthPrompt = nil
		}
		return
	}
}

func modelAuthProviderDisplayName(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "xai", "grok":
		return "Grok"
	case "openai-codex", "codex":
		return "Codex"
	default:
		if provider = strings.TrimSpace(provider); provider != "" {
			return provider
		}
		return "provider"
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
		waitingText := "Waiting for the OAuth callback. Esc cancels."
		if m.slashArgLoadAuthPrompt != nil {
			waitingText = "Automatic callback is active. Paste the browser code or callback URL below, then press Enter. Esc cancels."
		}
		lines = append(lines,
			m.theme.TextStyle().Bold(true).Render("Finish signing in via your browser"),
			m.theme.HelpHintTextStyle().Render("If the browser did not open automatically, open:"),
			m.theme.TextStyle().Render(verificationURL),
			m.theme.HelpHintTextStyle().Render(waitingText),
		)
	}
	return insetRenderedBlock(strings.Join(wrapBTWContentLines(lines, contentWidth), "\n"), inputHorizontalInset)
}
