package tuiapp

import (
	"image/color"
	"net/url"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/x/ansi"
)

func (m *Model) renderWizardOverlay() string {
	s := m.wizardOverlay
	if s == nil {
		return ""
	}
	s.text.lines, s.text.rows = nil, nil
	width := min(112, max(16, m.width-4))
	inner := max(8, width-m.overlayBorderChromeWidth())
	border, inset := 0, 0
	if m.overlayUsesBorder() {
		border, inset = 1, 2
	}
	// Leave the fixed composer visible even when the form or catalog scrolls.
	maxHeight := max(8, m.height-7)
	muted := m.theme.HelpHintTextStyle()
	path := m.wizardBreadcrumb()
	title := tuikit.RenderBreadcrumb(m.theme, inner-4, path[:len(path)-1], path[len(path)-1])
	body := []string{padRightDisplay(title, inner-3) + m.wizardButtonStyle("close", m.wizardCloseEnabled(), false).Render("[×]")}
	body = append(body, muted.Render(strings.Repeat("─", inner)))
	if m.isWizardAuthChoice() {
		body = append(body, muted.Render(truncateTailDisplay(wizardPlainText(m.activePrompt.prompt), inner)))
	}
	busy := m.wizardBusy()
	if m.slashArgQuery != "" && len(s.fields) == 0 && !s.blocked && !busy && !m.isWizardAuthChoice() {
		search := "/ " + truncateDisplayCellsFromEnd(m.slashArgQuery, inner-3) + "▏"
		body = append(body, muted.Render(search))
	}
	footer := []string{}
	if m.wizard.def.Command == "disconnect" && m.wizardMultiSelectStep() && !busy {
		if c, ok := m.currentSlashArgCandidate(); ok && c.Detail != "" {
			footer = append(footer, muted.Render(truncateTailDisplay(wizardPlainText(c.Detail), inner)))
		}
	}
	if s.err != "" {
		// Keep diagnostics readable without pushing actions outside the overlay.
		styled := m.theme.ErrorStyle().Render(wizardPlainText(s.err))
		lines := splitStyledPhysicalLines(ansi.Wrap(styled, inner, " "))
		limit := max(1, maxHeight-len(body)-len(footer)-2*border-3)
		if len(lines) > limit {
			lines = lines[:limit]
			lines[limit-1] = ansi.Truncate(lines[limit-1]+" …", inner, "…")
		}
		footer = append(footer, lines...)
	}
	help := "↑↓ select  esc back"
	if len(s.parents) == 0 {
		help = "↑↓ select  esc close"
	}
	if len(s.fields) > 0 {
		help = "tab field  esc back"
	}
	if s.field < len(s.fields) && s.fields[s.field].choice {
		help = "←→ change  tab field  esc back"
	}
	if m.wizardMultiSelectStep() && len(s.fields) == 0 {
		for _, candidate := range m.slashArgCandidates {
			if wizardCandidateSupportsMultiSelect(m.wizard.currentStep(), candidate) {
				help = "↑↓ select  space toggle  esc back"
				break
			}
		}
	}
	if m.isWizardAuthChoice() {
		help = "↑↓ select  esc back"
	}
	if s.err != "" && len(s.fields) == 0 {
		help = "esc back"
	}
	if busy {
		help = "esc back"
	}
	if s.pending {
		help = ""
	}
	if s.blocked {
		help = "esc close"
	}
	footer = append(footer, muted.Render(strings.Repeat("─", inner)))
	buttons := m.renderWizardFooter(inner, help)
	footer = append(footer, buttons.lines...)
	budget := max(1, maxHeight-len(body)-len(footer)-2*border)
	rows, offsets := m.wizardBodyLines(inner, len(body), budget)
	body = append(body, rows...)
	actionOffset := len(body) + len(footer) - 1
	body = append(body, footer...)
	m.collectWizardReadonlyText(body, offsets)
	m.renderWizardTextSelection(body)
	frame := tuikit.RenderResponsiveOverlayFrame(m.theme, tuikit.ResponsiveOverlayFrameModel{Body: body, Width: width, UseBorder: m.overlayUsesBorder()})
	w, h := lipgloss.Width(frame), lipgloss.Height(frame)
	x, y := max(0, (m.width-w)/2), max(0, (m.height-5-h)/2)
	for i := range offsets {
		if offsets[i] >= 0 {
			offsets[i] += y + border
		}
	}
	s.geometry = wizardOverlayGeometry{
		x: x, y: y, width: w, height: h, closeX: x + inset + inner - 3, closeY: y + border,
		rows: offsets, actionX: x + inset + inner - buttons.actionWidth, actionY: y + border + actionOffset, actionWidth: buttons.actionWidth,
		backX: x + inset, backY: y + border + actionOffset, backWidth: buttons.backWidth,
		contentX: x + inset, contentY: y + border, contentWidth: inner,
	}
	return frame
}

func (m *Model) wizardBreadcrumb() []string {
	command := m.wizard.def.Command
	path := []string{"/" + command}
	if command == "connect" {
		state := m.wizard.state
		if state["source"] == "acp" {
			if agent := state["acp_agent"]; agent != "" {
				path = append(path, agent)
			}
		} else if provider := state["provider"]; provider != "" {
			path = append(path, provider)
			if address := state["baseurl"]; address != "" && m.wizardStepKey() != "endpoint" && m.wizardStepKey() != "baseurl" {
				if template, ok := modelconfig.LookupProvider(provider); ok {
					if endpoint, ok := modelconfig.EndpointForBaseURL(template, address); ok {
						path = append(path, firstNonEmpty(endpoint.Display, "default"))
					} else if parsed, err := url.Parse(address); err == nil && parsed.Host != "" {
						path = append(path, parsed.Host)
					}
				}
			}
		}
	}
	if step := m.wizardTitle(); step != "" {
		path = append(path, step)
	}
	return path
}

func (m *Model) wizardTitle() string {
	s := m.wizardOverlay
	if m.isWizardAuthChoice() {
		return m.activePrompt.title
	}
	if m.wizard.def.Command == "plugin" && !s.pending && !s.blocked {
		return m.pluginWizardTitle()
	}
	switch {
	case s.pending:
		if m.wizard.def.Command == "disconnect" {
			return "Disconnecting"
		}
		if m.wizard.def.Command == "connect" {
			return "Connecting"
		}
		return "Saving"
	case s.blocked:
		return "Check result"
	case m.slashArgLoadPending:
		if m.slashArgLoadAuthURL != "" {
			return "Sign in"
		}
		if m.wizardStepKey() == "source" {
			return ""
		}
		if m.wizardStepKey() == "acp_model" {
			return "Preparing Agent"
		}
		return "Loading models"
	case m.connectCapabilitiesForm():
		return "Model details"
	}
	if m.wizard.def.Command == "disconnect" {
		return "Connected models"
	}
	if m.wizard.def.Command == "plugin" {
		return m.pluginWizardTitle()
	}
	switch m.wizardStepKey() {
	case "source":
		return ""
	case "provider":
		return "Provider"
	case "endpoint":
		if len(s.fields) > 0 {
			return "Custom endpoint"
		}
		return "Endpoint"
	case "baseurl":
		return "Endpoint"
	case "apikey":
		return "API key"
	case "acp_agent":
		return "ACP Agent"
	case "acp_launcher":
		return "Launch method"
	case "acp_command":
		return "Command"
	case "model", "acp_model":
		return "Models"
	}
	return "Connection"
}

func (m *Model) wizardBodyLines(width, offset, budget int) ([]string, []int) {
	s := m.wizardOverlay
	muted := m.theme.HelpHintTextStyle()
	if s.blocked {
		return []string{muted.Render(truncateTailDisplay(m.wizardResultHint(), width))}, nil
	}
	if m.wizardBusy() {
		label := "Loading…"
		if s.pending {
			label = m.wizardTitle() + "…"
		} else if m.slashArgLoadPending {
			label = m.slashArgLoadStatusText()
		}
		lines := []string{m.theme.SpinnerStyle().Render(m.runningFrame()) + " " + muted.Render(truncateTailDisplay(label, max(1, width-2)))}
		if code := m.slashArgLoadAuthCode; code != "" {
			lines = append(lines, m.theme.TitleStyle().Render("Code: "+code))
		}
		if url := m.slashArgLoadAuthURL; url != "" {
			lines = append(lines, wrapBTWContentLines([]string{url}, width)...)
		}
		return lines[:min(len(lines), budget)], nil
	}
	candidates, index := m.slashArgCandidates, m.slashArgIndex
	if m.isWizardAuthChoice() {
		candidates = nil
		for _, choice := range m.visiblePromptChoices() {
			candidates = append(candidates, SlashArgCandidate{Value: choice.value, Display: choice.label, Detail: choice.detail})
		}
		index = m.activePrompt.choiceIndex
	}
	count := len(candidates)
	endpointStep := m.wizardStepKey() == "endpoint"
	form := len(s.fields) > 0 && !m.isWizardAuthChoice()
	if form {
		count, index = len(s.fields), min(s.field, len(s.fields)-1)
	} else if !m.isWizardAuthChoice() && (m.wizardStepKey() == "model" || endpointStep) {
		count++
	}
	if count == 0 {
		if s.err != "" {
			return nil, nil
		}
		text := "No matches"
		if m.slashArgQuery == "" {
			text = "Nothing configured"
		}
		return []string{muted.Render(text)}, nil
	}
	offsets := make([]int, count)
	for i := range offsets {
		offsets[i] = -1
	}
	// Scroll indicators share the body budget, never the fixed footer.
	rowBudget := budget
	if count > budget && budget >= 3 {
		rowBudget -= 2
	}
	s.window = clampInt(s.window, 0, max(0, count-rowBudget))
	if index < s.window {
		s.window = index
	}
	if index >= s.window+rowBudget {
		s.window = index - rowBudget + 1
	}
	end := min(count, s.window+rowBudget)
	lines := []string{}
	if count > budget && budget >= 3 {
		hint := ""
		if s.window > 0 {
			hint = "↑ more"
		}
		lines = append(lines, muted.Render(hint))
	}
	connectSource := m.wizard.def.Command == "connect" && m.wizardStepKey() == "source"
	var sourceDetail, activeSourceDetail color.Color
	if connectSource {
		sourceDetail = m.theme.ReadableTextColor(m.theme.CommandSubText, m.theme.ModalBg)
		if active := m.theme.CommandActiveStyle(); !active.GetReverse() {
			activeSourceDetail = m.theme.ReadableTextColor(m.theme.CommandSubText, active.GetBackground())
		}
	}
	for i := s.window; i < end; i++ {
		offsets[i] = offset + len(lines)
		if form {
			line := m.renderWizardField(s.fields[i], i == s.field, width)
			if s.hovered == wizardRowTarget(i) && i != s.field {
				line = m.theme.CommandActiveStyle().Padding(0).Render(padRightDisplay(ansi.Strip(line), width))
			}
			lines = append(lines, line)
			continue
		}
		label, detail := "+ Custom model", ""
		if endpointStep {
			label = "+ Custom endpoint"
		}
		selected := i == index
		if i < len(candidates) {
			candidate := candidates[i]
			label, detail = slashArgCandidateIdentity(candidate), candidate.Detail
			if endpointStep {
				if address := strings.TrimSpace(candidate.Value); address != label {
					detail = joinNonEmpty([]string{address, detail}, " · ")
				}
			} else if !selected {
				detail = ""
			}
			if m.wizard.def.Command == "plugin" && m.wizardStepKey() != "target" {
				label = pluginActionLabel(candidate.Value, label)
				detail = ""
			}
			if m.wizard.def.Command == "disconnect" {
				detail = ""
				if slashArgIdentityDuplicated(label, candidates) {
					detail = candidate.Detail
				}
			}
			if connectSource {
				detail = candidate.Detail
				switch candidate.Value {
				case "account":
					label = "Sign in"
				case "api-key":
					label = "API key / local"
				case "acp":
					label = "local acp agent"
				}
			}
			if m.wizardStepKey() == "acp_agent" && candidate.Value == "codex" {
				label = "Codex CLI"
			}
			if m.wizardStepKey() == "acp_agent" || (m.wizardStepKey() == "acp_model" || m.wizardStepKey() == "provider") && !selected {
				detail = ""
			}
			if marker, ok := m.wizardMultiSelectMarker(candidate); ok {
				label = marker + label
			}
		}
		marker := "· "
		if selected {
			marker = "› "
		}
		labelWidth := min(46, max(14, (width-4)*3/5))
		if endpointStep {
			labelWidth = min(labelWidth, max(16, displayColumns(marker+label)))
		}
		var detailColor color.Color
		if connectSource {
			labelWidth = min(20, max(14, (width-4)*2/5), max(1, width-5))
			detailColor = sourceDetail
			if selected {
				detailColor = activeSourceDetail
			}
		}
		lines = append(lines, m.renderPickerColumnsLine(marker+wizardPlainText(label), wizardPlainText(detail), labelWidth, max(1, width-2), selected, detailColor))
	}
	if count > budget && budget >= 3 {
		hint := ""
		if end < count {
			hint = "↓ more"
		}
		lines = append(lines, muted.Render(hint))
	}
	return lines, offsets
}

const wizardURLSeparator = ":\u034f//"

func wizardPlainText(text string) string {
	// ttyd's xterm WebLinksAddon detects URLs even without OSC 8. A zero-width
	// combining mark in the scheme delimiter survives terminal cell layout.
	return strings.ReplaceAll(tuikit.SanitizeLogText(text), "://", wizardURLSeparator)
}

func (m *Model) renderWizardField(field wizardField, selected bool, width int) string {
	labelWidth := min(14, max(6, width/3))
	available := max(1, width-labelWidth-4)
	displayValue := func(value string) string {
		if field.secret {
			return strings.Repeat("•", len([]rune(value)))
		}
		return strings.ReplaceAll(value, "\n", " ↵ ")
	}
	value := displayValue(field.value)
	if field.choice {
		value = "No"
		if field.value == "true" {
			value = "Yes"
		}
		if selected {
			value = "‹ " + value + " ›"
		}
	} else if selected {
		runes := []rune(field.value)
		cursor := clampInt(m.wizardOverlay.cursor, 0, len(runes))
		prefix := truncateDisplayCellsFromEnd(displayValue(string(runes[:cursor])), available-1)
		value = prefix + "▏" + truncateDisplayCells(displayValue(string(runes[cursor:])), max(0, available-displayColumns(prefix)-1))
	}
	if field.value == "" && selected && !field.choice {
		placeholder := field.placeholder
		if field.key == "apikey" && m.wizard.state["_reuseauth"] == "optional" {
			placeholder = "Saved key · leave blank"
		}
		value += placeholder
	}
	value = truncateDisplayCells(value, available)
	return m.renderPickerColumnsLine(field.label, value, labelWidth, max(1, width-2), selected, nil)
}

func (m *Model) wizardResultHint() string {
	if m.wizard.def.Command == "plugin" {
		return "Review /plugin before retrying."
	}
	return "Review /model before retrying."
}
