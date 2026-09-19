package tuiapp

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func (m *Model) renderWizardOverlay() string {
	s := m.wizardOverlay
	if s == nil {
		return ""
	}
	width := min(112, max(16, m.width-4))
	inner := max(8, width-m.overlayBorderChromeWidth())
	muted := m.theme.HelpHintTextStyle()
	title := "/" + m.wizard.def.Command + " · " + m.wizardTitle()
	body := []string{m.theme.TitleStyle().Render(padRightDisplay(truncateTailDisplay(title, inner-2), inner-1)) + muted.Render("×")}
	if context := m.connectContext(); context != "" {
		body = append(body, muted.Render(truncateTailDisplay(context, inner)))
	}
	body = append(body, muted.Render(strings.Repeat("─", inner)))
	busy := s.pending || m.slashArgLoadPending || m.slashArgRequestPending
	if len(s.fields) == 0 && !s.blocked && !m.slashArgLoadPending && !s.pending {
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
		footer = append(footer, m.theme.ErrorStyle().Render(truncateTailDisplay(s.err, inner)))
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
	if displayColumns(help) > inner {
		help = "↑↓  tab  enter  esc"
	}
	footer = append(footer, muted.Render(strings.Repeat("─", inner)))
	action := m.wizardAction() + " ↵"
	if busy {
		action = ""
	}
	helpWidth := max(0, inner-displayColumns(action)-2)
	if displayColumns(help) > helpWidth {
		help = "↑↓  enter  esc"
		if len(s.fields) > 0 {
			help = "tab  enter  esc"
		}
		if m.wizardMultiSelectStep() {
			help = "↑↓  space  esc"
		}
		if m.isBotSettingsModel() {
			help = "↑↓  ←→  tab  esc"
		}
	}
	actionStyle := m.theme.CommandStyle().Padding(0)
	if len(s.fields) > 0 && s.field == len(s.fields) {
		actionStyle = m.theme.SelectionStyle().Bold(true)
	}
	footer = append(footer, muted.Render(padRightDisplay(truncateTailDisplay(help, helpWidth), helpWidth))+"  "+actionStyle.Render(action))
	border, inset := 0, 0
	if m.overlayUsesBorder() {
		border, inset = 1, 2
	}
	// Leave the fixed composer visible even when the form or catalog scrolls.
	maxHeight := max(8, m.height-7)
	budget := max(1, maxHeight-len(body)-len(footer)-2*border)
	rows, offsets := m.wizardBodyLines(inner, len(body), budget)
	body = append(body, rows...)
	actionOffset := len(body) + len(footer) - 1
	body = append(body, footer...)
	frame := tuikit.RenderResponsiveOverlayFrame(m.theme, tuikit.ResponsiveOverlayFrameModel{Body: body, Width: width, UseBorder: m.overlayUsesBorder()})
	w, h := lipgloss.Width(frame), lipgloss.Height(frame)
	x, y := max(0, (m.width-w)/2), max(0, (m.height-5-h)/2)
	for i := range offsets {
		if offsets[i] >= 0 {
			offsets[i] += y + border
		}
	}
	s.geometry = wizardOverlayGeometry{x: x, y: y, width: w, height: h, closeX: x + inset + inner - 1, closeY: y + border, rows: offsets, actionX: x + inset + inner - displayColumns(action), actionY: y + border + actionOffset}
	return frame
}

func (m *Model) wizardTitle() string {
	s := m.wizardOverlay
	if s.bot != nil {
		return m.botSettingsTitle()
	}
	if m.wizard.def.Command == "plugin" && !s.pending && !s.blocked {
		return m.pluginWizardTitle()
	}
	switch {
	case s.pending:
		return "Saving"
	case s.blocked:
		return "Check result"
	case m.slashArgLoadPending:
		if m.slashArgLoadAuthURL != "" {
			return "Sign in"
		}
		if m.wizardStepKey() == "acp_model" {
			return "Preparing Agent"
		}
		return "Loading models"
	case m.connectCapabilitiesForm():
		return "Model details"
	case len(s.fields) > 0 && m.wizardStepKey() != "acp_command":
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
	if s.pending {
		return nil, nil
	}

	if s.blocked {
		return []string{muted.Render(truncateTailDisplay(m.wizardResultHint(), width))}, nil
	}
	if m.slashArgLoadPending {
		lines := []string{}
		if m.slashArgLoadLabel != slashArgLoadLabel(m.slashArgCommand) {
			lines = append(lines, muted.Render(truncateTailDisplay(m.slashArgLoadLabel, width)))
		}
		if code := m.slashArgLoadAuthCode; code != "" {
			lines = append(lines, m.theme.TitleStyle().Render("Code: "+code))
		}
		if url := m.slashArgLoadAuthURL; url != "" {
			lines = append(lines, wrapBTWContentLines([]string{url}, width)...)
		}
		return lines[:min(len(lines), budget)], nil
	}
	count, index := len(m.slashArgCandidates), m.slashArgIndex
	form := len(s.fields) > 0
	if form {
		count, index = len(s.fields), min(s.field, len(s.fields)-1)
	} else if m.wizardStepKey() == "model" || m.wizardStepKey() == "endpoint" {
		count++
	}
	if count == 0 {
		text := "No matches"
		if m.slashArgQuery == "" {
			text = "Nothing configured"
		}
		if m.slashArgRequestPending {
			text = "Loading…"
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
			lines = append(lines, m.renderWizardField(s.fields[i], i == s.field, width))
			continue
		}
		label, detail := "+ Custom model", ""
		if m.wizardStepKey() == "endpoint" {
			label = "+ Custom endpoint"
		}
		selected := i == m.slashArgIndex
		if i < len(m.slashArgCandidates) {
			candidate := m.slashArgCandidates[i]
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
