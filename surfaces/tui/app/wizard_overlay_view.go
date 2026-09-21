package tuiapp

import (
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
	if m.isACPManualSetup() {
		width = min(max(16, m.width-4), max(112, min(180, m.width*9/10)))
	}
	inner := max(8, width-m.overlayBorderChromeWidth())
	border, inset := 0, 0
	if m.overlayUsesBorder() {
		border, inset = 1, 2
	}
	// Leave the fixed composer visible even when the form or catalog scrolls.
	maxHeight := max(8, m.height-7)
	if m.isACPManualSetup() {
		maxHeight = max(8, m.height-5)
	}
	muted := m.theme.HelpHintTextStyle()
	title := "/" + m.wizard.def.Command + " · " + m.wizardTitle()
	body := []string{m.theme.TitleStyle().Render(padRightDisplay(truncateTailDisplay(title, inner-4), inner-3)) + m.wizardButtonStyle("close", m.wizardCloseEnabled(), false).Render("[×]")}
	if context := m.connectContext(); context != "" {
		body = append(body, muted.Render(truncateTailDisplay(context, inner)))
	}
	if m.isWizardAuthChoice() {
		body = append(body, muted.Render(truncateTailDisplay(m.activePrompt.prompt, inner)))
	}
	body = append(body, muted.Render(strings.Repeat("─", inner)))
	busy := m.wizardBusy()
	if s.runtimeSetup != nil && !busy {
		switch {
		case m.wizardStepKey() == "acp_launcher":
			body = append(body, muted.Render(truncateTailDisplay("The ACP runtime is not installed.", inner)))
		case m.wizardStepKey() == "acp_install":
			source, _ := url.Parse(s.runtimeSetup.ArchiveURL)
			if source != nil && !m.isACPManualSetup() {
				body = append(body, m.theme.LinkStyle().Hyperlink(tuikit.EscapeHyperlinkURI(s.runtimeSetup.ArchiveURL)).Render(truncateTailDisplay("Download from "+source.Hostname()+" ↗", inner)))
			}
			if m.isACPManualSetup() {
				body = append(body, muted.Render(truncateTailDisplay("Drag to select text · release to copy", inner)))
			} else {
				body = append(body, muted.Render(truncateTailDisplay("New or empty directory on the Host.", inner)))
			}
		}
	}
	if len(s.fields) == 0 && s.runtimeSetup == nil && m.wizardStepKey() != "acp_install" && !s.blocked && !busy && !m.isWizardAuthChoice() {
		search := "/ " + truncateDisplayCellsFromEnd(m.slashArgQuery, inner-3) + "▏"
		if m.slashArgQuery == "" {
			search += "Search"
		}
		body = append(body, muted.Render(truncateTailDisplay(search, inner)))
	}
	footer := []string{}
	if m.wizard.def.Command == "disconnect" && m.wizardMultiSelectStep() && !busy {
		if c, ok := m.currentSlashArgCandidate(); ok && c.Detail != "" {
			footer = append(footer, muted.Render(truncateTailDisplay(c.Detail, inner)))
		}
	}
	if s.err != "" {
		// Keep diagnostics readable without pushing actions outside the overlay.
		styled := m.theme.ErrorStyle().Render(tuikit.LinkifyText(tuikit.SanitizeLogText(s.err), m.theme.LinkStyle()))
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
	if m.wizardStepKey() == "acp_model" {
		help = "↑↓ select  esc back"
	}
	if s.bot != nil {
		if s.bot.choosingModel {
			help = "↑↓ select  ←→ effort  esc back"
			if c, ok := m.currentSlashArgCandidate(); ok && c.ModelSelection != nil && c.ModelSelection.FastSupported {
				help = "↑↓ select  tab field  ←→ change  esc back"
			}
		} else {
			help = "tab field  enter next  esc close"
		}
	}
	if m.isACPManualSetup() {
		help = "↑↓ scroll  esc back"
		if m.canSendACPInstallPrompt() {
			help = "↑↓ scroll  tab action  esc back"
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
		if s.bot != nil && s.bot.loading {
			help = "esc close"
		}
	}
	if s.blocked {
		help = "esc close"
	}
	footer = append(footer, muted.Render(strings.Repeat("─", inner)))
	buttons := m.renderWizardFooter(inner, help)
	sendFooterOffset := len(footer)
	footer = append(footer, buttons.lines...)
	budget := max(1, maxHeight-len(body)-len(footer)-2*border)
	rows, offsets := m.wizardBodyLines(inner, len(body), budget)
	body = append(body, rows...)
	sendOffset := len(body) + sendFooterOffset
	actionOffset := len(body) + len(footer) - 1
	body = append(body, footer...)
	if !m.isACPManualSetup() || s.runtimeSetup == nil {
		m.collectWizardReadonlyText(body, offsets)
	}
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
		rows: offsets, actionX: x + inset + inner - buttons.actionRightInset - buttons.actionWidth, actionY: y + border + actionOffset, actionWidth: buttons.actionWidth,
		backX: x + inset, backY: y + border + actionOffset, backWidth: buttons.backWidth,
		sendX: x + inset + inner - buttons.sendWidth, sendY: y + border + sendOffset, sendWidth: buttons.sendWidth,
		contentX: x + inset, contentY: y + border, contentWidth: inner,
	}
	return frame
}

func (m *Model) wizardTitle() string {
	s := m.wizardOverlay
	if m.isWizardAuthChoice() {
		return m.activePrompt.title
	}
	if s.bot != nil {
		return m.botSettingsTitle()
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
		if m.wizardStepKey() == "acp_model" {
			if confirmedACPInstallation(m.wizard.state) != nil {
				return "Installing and connecting"
			}
			return "Preparing Agent"
		}
		if m.wizardStepKey() == "acp_install" {
			return "Checking official download"
		}
		return "Loading models"
	case m.connectCapabilitiesForm():
		return "Model details"
	case len(s.fields) > 0 && m.wizardStepKey() != "acp_command":
		if m.wizardStepKey() == "acp_install" {
			return "Install runtime"
		}
		return "Connection"
	}
	if m.wizard.def.Command == "disconnect" {
		switch m.wizardStepKey() {
		case "kind":
			return "Source"
		case "provider_model":
			return "Provider models"
		default:
			return "ACP Agents"
		}
	}
	if m.wizard.def.Command == "plugin" {
		return m.pluginWizardTitle()
	}
	switch m.wizardStepKey() {
	case "source":
		return "Source"
	case "provider":
		return "Provider"
	case "endpoint":
		return "Endpoint"
	case "acp_agent":
		return "ACP Agent"
	case "acp_launcher":
		if s.runtimeSetup != nil {
			return "Set up runtime"
		}
		return "Launch method"
	case "acp_command":
		return "Command"
	case "acp_install":
		if m.isACPManualSetup() {
			return "Manual setup"
		}
		return "Install runtime"
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
	if m.isACPManualSetup() && s.runtimeSetup != nil {
		return m.acpManualBodyLines(width, offset, budget), nil
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
	form := len(s.fields) > 0 && !m.isWizardAuthChoice()
	if form {
		count, index = len(s.fields), min(s.field, len(s.fields)-1)
	} else if !m.isWizardAuthChoice() && (m.wizardStepKey() == "model" || m.wizardStepKey() == "endpoint") {
		count++
	}
	if count == 0 {
		if m.wizardStepKey() == "acp_install" && s.err != "" {
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
		if m.wizardStepKey() == "endpoint" {
			label = "+ Custom endpoint"
		}
		selected := i == index
		if i < len(candidates) {
			candidate := candidates[i]
			label, detail = slashArgCandidateIdentity(candidate), candidate.Detail
			if m.isBotSettingsModel() {
				detail = m.modelPickerControls(candidate, selected, width)
				if candidate.ModelConfigID == s.bot.config.Model {
					label = "● " + label
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
			}
			if m.wizardStepKey() == "source" {
				detail = ""
				if m.wizard.def.Command == "connect" {
					detail = candidate.Detail
				}
				switch candidate.Value {
				case "account":
					label = "Sign in"
				case "api-key":
					label = "API key / local"
				case "acp":
					label = "Local ACP Agent"
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
		if s.runtimeSetup != nil {
			labelWidth = max(1, width-4)
			detail = ""
		}
		if m.wizard.def.Command == "connect" && m.wizardStepKey() == "source" {
			labelWidth = min(32, max(14, (width-4)*2/5), max(1, width-5))
			// Keep examples visible on every row, with subdued text even when selected.
			detail = truncateTailDisplay(detail, max(1, width-labelWidth-4))
			if selected && detail != "" {
				base := m.theme.CommandActiveStyle().Padding(0, 0).UnsetBold()
				hint := base
				if m.theme.HelpHintFg != nil {
					hint = hint.Foreground(m.theme.HelpHintFg)
				} else if !m.theme.NoColor {
					hint = hint.Faint(true)
				}
				identity := padRightDisplay(truncateTailDisplay(marker+label, labelWidth), labelWidth)
				trail := strings.Repeat(" ", max(0, width-labelWidth-displayColumns(detail)-4))
				lines = append(lines, base.Bold(true).Render(" "+identity)+base.Render("  ")+hint.Render(detail)+base.Render(trail+" "))
				continue
			}
		}
		lines = append(lines, m.renderPickerColumnsLine(marker+label, detail, labelWidth, max(1, width-2), selected))
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
	} else if field.key == "bot_model" {
		if displayColumns(value) > available-3 {
			value = "…" + truncateDisplayCellsFromEnd(value, max(1, available-4))
		}
		value += "  ›"
	} else if selected {
		runes := []rune(field.value)
		cursor := clampInt(m.wizardOverlay.cursor, 0, len(runes))
		prefix := truncateDisplayCellsFromEnd(displayValue(string(runes[:cursor])), available-1)
		value = prefix + "▏" + truncateDisplayCells(displayValue(string(runes[cursor:])), max(0, available-displayColumns(prefix)-1))
	}
	if field.value == "" && selected && !field.choice {
		placeholder := field.placeholder
		if field.key == "apikey" && m.connectKeyOptional() {
			placeholder = "Saved key · leave blank"
		}
		value += placeholder
	}
	value = truncateDisplayCells(value, available)
	return m.renderPickerColumnsLine(field.label, value, labelWidth, max(1, width-2), selected)
}

func (m *Model) connectDisplayEndpoint() string {
	if value := m.wizard.state["baseurl"]; value != "" {
		return value
	}
	if template, ok := modelconfig.LookupProvider(m.wizard.state["provider"]); ok {
		return template.DefaultBaseURL
	}
	return ""
}

func (m *Model) wizardResultHint() string {
	if m.wizardOverlay.bot != nil {
		return "Reopen /settings or /bots to review."
	}
	if m.wizard.def.Command == "plugin" {
		return "Review /plugin before retrying."
	}
	return "Review /model before retrying."
}
