package tuiapp

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

type subagentOverlayPage int

const (
	subagentPageMain subagentOverlayPage = iota
	subagentPageBinding
	subagentPageSets
	subagentPageNewRole
	subagentPageSaveSet
	subagentPageConfirm
)

type subagentOverlayAction string

const (
	subagentActionNoop         subagentOverlayAction = "noop"
	subagentActionOpenSets     subagentOverlayAction = "open-sets"
	subagentActionOpenBinding  subagentOverlayAction = "open-binding"
	subagentActionNewRole      subagentOverlayAction = "new-role"
	subagentActionSaveSet      subagentOverlayAction = "save-set"
	subagentActionApplySet     subagentOverlayAction = "apply-set"
	subagentActionFieldHandle  subagentOverlayAction = "field-handle"
	subagentActionFieldDesc    subagentOverlayAction = "field-description"
	subagentActionFieldBind    subagentOverlayAction = "field-binding"
	subagentActionFieldSetName subagentOverlayAction = "field-set-name"
	subagentActionCreateRole   subagentOverlayAction = "create-role"
	subagentActionCommitSet    subagentOverlayAction = "commit-set"
	subagentActionCancel       subagentOverlayAction = "cancel"
	subagentActionConfirm      subagentOverlayAction = "confirm"
)

type subagentOverlayRow struct {
	action  subagentOverlayAction
	key     string
	section string
	label   string
	detail  string
	handle  agentbinding.Handle
	binding agentbinding.Binding
	reset   bool
	enabled bool
	custom  bool
}

type subagentOverlayGeometry struct {
	x      int
	y      int
	closeX int
	closeY int
	width  int
	height int
	rows   []int
}

type subagentOverlayNav struct {
	page  subagentOverlayPage
	index int
}

type subagentOverlayState struct {
	page       subagentOverlayPage
	status     agentbinding.Status
	loading    bool
	pending    bool
	err        string
	request    uint64
	index      int
	rows       []subagentOverlayRow
	geometry   subagentOverlayGeometry
	pressedKey string

	bindingHandle agentbinding.Handle
	creatingRole  bool

	roleHandle      string
	roleDescription string
	roleBinding     agentbinding.Binding
	setName         string

	confirmLabel  string
	confirmHandle agentbinding.Handle
	confirmSet    string

	afterMutation *subagentOverlayNav
}

type subagentOverlayResultMsg struct {
	request uint64
	status  agentbinding.Status
	err     error
}

func (m *Model) openSubagentOverlay() tea.Cmd {
	if m == nil || m.turnRunning() {
		return nil
	}
	service, ok := m.cfg.ControlService.(agentbinding.ConfigurationService)
	if !ok {
		return m.showHint("subagent configuration is unavailable", hintOptions{
			priority:       HintPriorityHigh,
			clearOnMessage: true,
			clearAfter:     systemHintDuration,
		})
	}
	m.clearInputOverlays()
	m.showPalette = false
	m.dismissWelcomeCard()
	m.subagentRequestSeq++
	request := m.subagentRequestSeq
	ctx := contextOrBackground(m.cfg.Context)
	m.subagentOverlay = &subagentOverlayState{
		page: subagentPageMain, loading: true, request: request,
	}
	return func() tea.Msg {
		status, err := service.AgentBindingStatus(ctx)
		return subagentOverlayResultMsg{request: request, status: status, err: err}
	}
}

func (m *Model) runSubagentMutation(
	action func(context.Context, agentbinding.ConfigurationService) (agentbinding.Status, error),
) tea.Cmd {
	if m == nil || m.subagentOverlay == nil || action == nil {
		return nil
	}
	service, ok := m.cfg.ControlService.(agentbinding.ConfigurationService)
	if !ok {
		m.subagentOverlay.err = "subagent configuration is unavailable"
		return nil
	}
	m.subagentOverlay.pending = true
	m.subagentOverlay.err = ""
	m.subagentRequestSeq++
	request := m.subagentRequestSeq
	m.subagentOverlay.request = request
	ctx := contextOrBackground(m.cfg.Context)
	return func() tea.Msg {
		status, err := action(ctx, service)
		return subagentOverlayResultMsg{request: request, status: status, err: err}
	}
}

func (m *Model) handleSubagentOverlayResult(msg subagentOverlayResultMsg) tea.Cmd {
	if m == nil || m.subagentOverlay == nil || m.subagentOverlay.request != msg.request {
		return nil
	}
	state := m.subagentOverlay
	state.loading = false
	state.pending = false
	if msg.err != nil {
		state.err = strings.TrimSpace(msg.err.Error())
		state.afterMutation = nil
		return nil
	}
	state.status = msg.status
	state.err = ""
	if state.afterMutation != nil {
		state.page = state.afterMutation.page
		state.index = state.afterMutation.index
		state.afterMutation = nil
	}
	return m.refreshAgentSlashCommandsCmd()
}

func (m *Model) refreshAgentSlashCommandsCmd() tea.Cmd {
	if m == nil || m.cfg.ControlService == nil {
		return nil
	}
	service := m.cfg.ControlService
	return func() tea.Msg {
		return SetCommandsMsg{
			Commands: appendAgentSlashCommandsWithContext(context.Background(), service, DefaultCommands()),
			Details:  profileCommandDetailsWithContext(context.Background(), service),
		}
	}
}

func (m *Model) renderSubagentOverlay() string {
	if m == nil || m.subagentOverlay == nil {
		return ""
	}
	state := m.subagentOverlay
	width := minInt(maxInt(48, m.fixedRowWidth()-4), 120)
	if m.width > 0 {
		width = minInt(width, maxInt(20, m.width-4))
	}
	innerWidth := maxInt(16, width-m.overlayBorderChromeWidth())
	body := make([]string, 0, 24)
	body = append(body, m.renderSubagentTitle(innerWidth))
	body = append(body, m.theme.MutedTextStyle().Render(strings.Repeat("─", innerWidth)))

	state.rows = m.subagentRows()
	state.index = clampInt(state.index, 0, maxInt(0, len(state.rows)-1))
	rowLines, rowOffsets := m.renderSubagentRows(state.rows, innerWidth, len(body))
	body = append(body, rowLines...)
	if state.loading {
		body = append(body, "", m.theme.HelpHintTextStyle().Render("Loading Agent bindings…"))
	}
	if state.pending {
		body = append(body, "", m.theme.HelpHintTextStyle().Render("Saving configuration…"))
	}
	if state.err != "" {
		body = append(body, "", m.theme.ErrorStyle().Render(truncateTailDisplay(state.err, innerWidth)))
	}
	body = append(body, "")
	body = append(body, m.theme.HelpHintTextStyle().Render(truncateTailDisplay(m.subagentFooter(), innerWidth)))

	frame := tuikit.RenderResponsiveOverlayFrame(m.theme, tuikit.ResponsiveOverlayFrameModel{
		Body:      body,
		Width:     width,
		UseBorder: m.overlayUsesBorder(),
	})
	renderedWidth := lipgloss.Width(frame)
	renderedHeight := len(strings.Split(frame, "\n"))
	startX := maxInt(0, (m.width-renderedWidth)/2)
	startY := maxInt(0, (m.height-renderedHeight)/2)
	borderInset := 0
	contentInset := 0
	if m.overlayUsesBorder() {
		borderInset = 1
		contentInset = 2
	}
	screenRows := make([]int, len(rowOffsets))
	for i, offset := range rowOffsets {
		screenRows[i] = -1
		if offset >= 0 {
			screenRows[i] = startY + borderInset + offset
		}
	}
	state.geometry = subagentOverlayGeometry{
		x: startX, y: startY,
		closeX: startX + contentInset + maxInt(0, innerWidth-1),
		closeY: startY + borderInset,
		width:  renderedWidth, height: renderedHeight, rows: screenRows,
	}
	return frame
}

func (m *Model) renderSubagentTitle(width int) string {
	title := m.theme.TitleStyle().Render("◆ Subagents")
	close := m.theme.HelpHintTextStyle().Render("×")
	gap := maxInt(1, width-displayColumns(title)-displayColumns(close))
	return title + strings.Repeat(" ", gap) + close
}

func (m *Model) renderSubagentRows(rows []subagentOverlayRow, width int, bodyOffset int) ([]string, []int) {
	if len(rows) == 0 {
		return []string{"", m.theme.HelpHintTextStyle().Render("No configurable Agents.")}, nil
	}
	maxVisible := maxInt(2, m.height-16)
	start := 0
	if len(rows) > maxVisible {
		start = m.subagentOverlay.index - maxVisible/2
		start = clampInt(start, 0, len(rows)-maxVisible)
	}
	end := minInt(len(rows), start+maxVisible)
	lines := make([]string, 0, end-start+6)
	offsets := make([]int, len(rows))
	for i := range offsets {
		offsets[i] = -1
	}
	lastSection := ""
	if start > 0 {
		lines = append(lines, m.theme.HelpHintTextStyle().Render("  ↑ more"))
	}
	for i := start; i < end; i++ {
		row := rows[i]
		if row.section != "" && row.section != lastSection {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, m.theme.KeyLabelStyle().Render(row.section))
			lastSection = row.section
		}
		offsets[i] = bodyOffset + len(lines)
		lines = append(lines, m.renderSubagentRow(row, i == m.subagentOverlay.index, width))
	}
	if end < len(rows) {
		lines = append(lines, m.theme.HelpHintTextStyle().Render("  ↓ more"))
	}
	return lines, offsets
}

func (m *Model) renderSubagentRow(row subagentOverlayRow, selected bool, width int) string {
	marker := "  "
	if selected {
		marker = "▎ "
	}
	labelWidth := minInt(24, maxInt(14, width/3))
	if m.subagentOverlay != nil && m.subagentOverlay.page == subagentPageBinding {
		labelWidth = minInt(52, maxInt(24, width*3/5))
	}
	labelWidth = minInt(labelWidth, maxInt(1, width-displayColumns(marker)-3))
	label := truncateTailDisplay(row.label, labelWidth)
	detailWidth := maxInt(1, width-displayColumns(marker)-labelWidth-2)
	detail := truncateTailDisplay(row.detail, detailWidth)
	labelSegment := marker + padRightDisplay(label, labelWidth) + "  "
	detailSegment := padRightDisplay(detail, detailWidth)
	switch {
	case selected && row.enabled:
		selection := m.theme.SelectionStyle()
		return selection.Bold(true).Render(labelSegment) + selection.Render(detailSegment)
	case selected:
		return m.theme.MutedTextStyle().Render(labelSegment + detailSegment)
	case !row.enabled:
		return m.theme.HelpHintTextStyle().Render(labelSegment + detailSegment)
	}
	labelStyle := m.theme.TextStyle()
	switch row.action {
	case subagentActionConfirm:
		labelStyle = m.theme.ErrorStyle().Bold(true)
	case subagentActionCancel:
		labelStyle = m.theme.SecondaryTextStyle().Bold(true)
	case subagentActionNewRole,
		subagentActionSaveSet,
		subagentActionApplySet,
		subagentActionCreateRole,
		subagentActionCommitSet:
		labelStyle = labelStyle.Bold(true)
	}
	return labelStyle.Render(labelSegment) + m.theme.HelpHintTextStyle().Render(detailSegment)
}

func (m *Model) subagentRows() []subagentOverlayRow {
	state := m.subagentOverlay
	if state == nil {
		return nil
	}
	switch state.page {
	case subagentPageBinding:
		return m.subagentBindingRows()
	case subagentPageSets:
		return m.subagentSetRows()
	case subagentPageNewRole:
		return m.subagentNewRoleRows()
	case subagentPageSaveSet:
		return m.subagentSaveSetRows()
	case subagentPageConfirm:
		return []subagentOverlayRow{
			{action: subagentActionConfirm, key: "confirm", section: "Confirm", label: "Delete", detail: state.confirmLabel, enabled: !state.pending},
			{action: subagentActionCancel, key: "cancel", label: "Cancel", detail: "Keep the current configuration", enabled: true},
		}
	default:
		return m.subagentMainRows()
	}
}

func (m *Model) subagentMainRows() []subagentOverlayRow {
	state := m.subagentOverlay
	activeSet := "Custom"
	for _, set := range state.status.Sets {
		if set.Active {
			activeSet = set.Name
			break
		}
	}
	rows := []subagentOverlayRow{{
		action: subagentActionOpenSets, key: "sets", section: "Configuration",
		label: "Binding set", detail: activeSet + "  ›", enabled: true,
	}}
	for _, item := range state.status.Handles {
		detail := subagentBindingDetail(item)
		if item.Definition.Custom {
			detail += "  · custom"
		}
		row := subagentOverlayRow{
			action:  subagentActionOpenBinding,
			key:     "handle:" + string(item.Definition.Handle),
			section: "Delegation Profiles",
			label:   string(item.Definition.Handle),
			detail:  detail,
			handle:  item.Definition.Handle,
			enabled: item.Definition.Configurable,
			custom:  item.Definition.Custom,
		}
		if item.Definition.Class == agentbinding.HandleClassSystem {
			row.section = "System Agents"
		}
		if !item.Definition.Configurable {
			row.action = subagentActionNoop
		}
		rows = append(rows, row)
	}
	rows = append(rows,
		subagentOverlayRow{action: subagentActionNewRole, key: "new", section: "Custom roles", label: "+ new", detail: "Handle + description + binding", enabled: true},
		subagentOverlayRow{action: subagentActionSaveSet, key: "save", section: "Snapshots", label: "Save binding set…", detail: "Snapshot all active bindings", enabled: true},
	)
	return rows
}

func (m *Model) subagentBindingRows() []subagentOverlayRow {
	state := m.subagentOverlay
	handle := state.bindingHandle
	resetDetail := "Remove the explicit binding"
	if agentbinding.IsSystem(handle) {
		resetDetail = "Use the provider-backed default"
	}
	var rows []subagentOverlayRow
	if !state.creatingRole {
		rows = append(rows, subagentOverlayRow{
			action: subagentActionOpenBinding, key: "binding:reset", section: "Choose binding",
			label: "Default", detail: resetDetail, handle: handle, reset: true, enabled: true,
		})
	}
	nameCounts := subagentProfileNameCounts(handle, state.status.Targets)
	for _, profile := range state.status.Targets {
		if !agentbinding.SupportsProfile(handle, profile) {
			continue
		}
		for _, choice := range profile.Effort.Choices {
			effort := strings.TrimSpace(choice.Canonical)
			if effort == "" {
				continue
			}
			binding := agentbinding.Binding{Handle: handle, ProfileID: profile.ID, Effort: effort}
			section := ""
			if len(rows) == 0 {
				section = "Choose binding"
			}
			rows = append(rows, subagentOverlayRow{
				action:  subagentActionOpenBinding,
				key:     "binding:" + profile.ID + ":" + effort,
				section: section,
				label:   subagentProfileDisplayName(profile),
				detail: subagentTargetDetail(
					profile,
					effort,
					nameCounts[subagentProfileNameKey(profile)] > 1,
				),
				handle: handle, binding: binding, enabled: true,
			})
		}
	}
	return rows
}

func (m *Model) subagentSetRows() []subagentOverlayRow {
	rows := []subagentOverlayRow{{
		action: subagentActionSaveSet, key: "save", section: "Binding sets",
		label: "+ save current", detail: "Create or replace a snapshot", enabled: true,
	}}
	for _, set := range m.subagentOverlay.status.Sets {
		detail := fmt.Sprintf("%d bindings", len(set.Bindings))
		if set.Active {
			detail += "  active"
		} else if !set.Available {
			detail = "unavailable"
		}
		rows = append(rows, subagentOverlayRow{
			action: subagentActionApplySet, key: "set:" + set.Name,
			label: set.Name, detail: detail, enabled: set.Available && !set.Active,
		})
	}
	return rows
}

func (m *Model) subagentNewRoleRows() []subagentOverlayRow {
	state := m.subagentOverlay
	binding := "Choose a ModelProfile and effort  ›"
	if state.roleBinding.ProfileID != "" {
		binding = subagentBindingDisplay(state.roleBinding, state.status.Targets)
	}
	return []subagentOverlayRow{
		{action: subagentActionFieldHandle, key: "field:handle", section: "New custom role", label: "Handle", detail: subagentFieldValue(state.roleHandle, "lowercase handle"), enabled: true},
		{action: subagentActionFieldDesc, key: "field:description", label: "Description", detail: subagentFieldValue(state.roleDescription, "what this Agent is good at"), enabled: true},
		{action: subagentActionFieldBind, key: "field:binding", label: "Binding", detail: binding, enabled: true},
		{action: subagentActionCreateRole, key: "create", section: "Actions", label: "Create role", detail: "Persist role and initial binding", enabled: !state.pending},
		{action: subagentActionCancel, key: "cancel", label: "Cancel", detail: "Discard this draft", enabled: true},
	}
}

func (m *Model) subagentSaveSetRows() []subagentOverlayRow {
	return []subagentOverlayRow{
		{action: subagentActionFieldSetName, key: "field:set-name", section: "Save binding set", label: "Name", detail: subagentFieldValue(m.subagentOverlay.setName, "lowercase snapshot name"), enabled: true},
		{action: subagentActionCommitSet, key: "save", section: "Actions", label: "Save snapshot", detail: "Replace a snapshot with the same name", enabled: !m.subagentOverlay.pending},
		{action: subagentActionCancel, key: "cancel", label: "Cancel", detail: "Return without saving", enabled: true},
	}
}

func subagentBindingDetail(item agentbinding.HandleStatus) string {
	if !item.Definition.Configurable {
		return "Current Session controller and effort"
	}
	if agentbinding.IsBound(item) {
		return subagentBindingDisplay(item.Binding, []modelprofile.ModelProfile{item.Profile})
	}
	if item.Definition.Class == agentbinding.HandleClassSystem {
		return "Provider-backed default"
	}
	return "Unbound"
}

func subagentProfileDisplayName(profile modelprofile.ModelProfile) string {
	name := strings.TrimSpace(profile.DisplayName)
	if name != "" {
		return name
	}
	return strings.TrimSpace(profile.ID)
}

func subagentProfileNameKey(profile modelprofile.ModelProfile) string {
	return strings.ToLower(subagentProfileDisplayName(profile))
}

func subagentProfileNameCounts(
	handle agentbinding.Handle,
	profiles []modelprofile.ModelProfile,
) map[string]int {
	counts := make(map[string]int, len(profiles))
	for _, profile := range profiles {
		if agentbinding.SupportsProfile(handle, profile) {
			counts[subagentProfileNameKey(profile)]++
		}
	}
	return counts
}

func subagentTargetDetail(profile modelprofile.ModelProfile, effort string, duplicateName bool) string {
	detail := "[" + effort + "]"
	switch profile.Kind() {
	case modelprofile.BackendACP:
		detail += "  · ACP"
		if duplicateName {
			detail += " · " + strings.TrimSpace(profile.Backend.ACP.AgentID)
		}
	case modelprofile.BackendProvider:
		if duplicateName {
			if source := subagentProviderSource(profile); source != "" {
				detail += "  · " + source
			}
		}
	}
	return detail
}

func subagentProviderSource(profile modelprofile.ModelProfile) string {
	if profile.Backend.Provider == nil {
		return ""
	}
	source, _, _ := strings.Cut(strings.TrimSpace(profile.Backend.Provider.ModelConfigID), "/")
	return source
}

func subagentBindingDisplay(binding agentbinding.Binding, profiles []modelprofile.ModelProfile) string {
	name := ""
	for _, profile := range profiles {
		if modelprofile.NormalizeID(profile.ID) == modelprofile.NormalizeID(binding.ProfileID) {
			name = subagentProfileDisplayName(profile)
			break
		}
	}
	if name == "" {
		name = strings.TrimSpace(binding.ProfileID)
	}
	return name + " [" + strings.TrimSpace(binding.Effort) + "]"
}

func subagentFieldValue(value, placeholder string) string {
	if strings.TrimSpace(value) == "" {
		return "‹" + placeholder + "›"
	}
	return value
}

func (m *Model) subagentFooter() string {
	if m.subagentOverlay == nil {
		return ""
	}
	switch m.subagentOverlay.page {
	case subagentPageMain:
		return "↑/↓/j/k navigate · Enter edit · p sets · n new · s save · d delete · Esc close"
	case subagentPageNewRole:
		return "↑/↓ or Tab fields · type to edit · Enter choose/save · Esc back"
	case subagentPageSaveSet:
		return "↑/↓ or Tab fields · type name · Enter save · Esc back"
	case subagentPageSets:
		return "↑/↓/j/k navigate · Enter apply · s save · d delete · Esc back"
	default:
		return "↑/↓/j/k navigate · Enter choose · Esc back"
	}
}

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
