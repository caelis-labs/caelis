package tuiapp

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/internal/controlprompt"
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
	action        subagentOverlayAction
	key           string
	section       string
	label         string
	detail        string
	current       bool
	search        string
	nameConflict  bool
	efforts       []string
	fastSupported bool
	fastMode      bool
	effortIndex   int
	handle        agentbinding.Handle
	binding       agentbinding.Binding
	reset         bool
	enabled       bool
	custom        bool
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
	key   string
	query string
	depth int
}

type subagentOverlayState struct {
	page        subagentOverlayPage
	status      agentbinding.Status
	loading     bool
	pending     bool
	err         string
	request     uint64
	index       int
	rows        []subagentOverlayRow
	geometry    subagentOverlayGeometry
	pressedKey  string
	query       string
	windowStart int
	parents     []subagentOverlayNav
	notice      string

	bindingHandle           agentbinding.Handle
	selectedSpeedByProfile  map[string]string
	fastFocus               bool
	selectedEffortByProfile map[string]string
	creatingRole            bool

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
		return m.showHint("participant and system-agent configuration is unavailable", hintOptions{
			priority:       HintPriorityHigh,
			clearOnMessage: true,
			clearAfter:     systemHintDuration,
		})
	}
	m.clearInputOverlays()
	m.showPalette = false
	m.subagentRosterPressed = false
	m.dismissWelcomeCard()
	m.syncViewportContent()
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
		m.subagentOverlay.err = "participant and system-agent configuration is unavailable"
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
	wasPending := state.pending
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
		nav := *state.afterMutation
		state.afterMutation = nil
		m.restoreSubagentPage(nav)
	}
	if wasPending {
		state.notice = "Configuration saved."
	}
	m.refreshSubagentRows("")
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

func (m *Model) allSubagentRows() []subagentOverlayRow {
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
			section: "Participant profiles",
			label:   string(item.Definition.Handle),
			detail:  detail,
			handle:  item.Definition.Handle,
			enabled: item.Definition.Configurable,
			custom:  item.Definition.Custom,
			search:  item.Definition.Description,
		}
		if spec, ok := controlprompt.Lookup(string(row.handle)); row.custom && ok {
			row.nameConflict = true
			row.detail = "Name conflicts with /" + spec.Name + "; rename role"
		}
		if item.Definition.Class == agentbinding.HandleClassSystem {
			row.section = "System Agents"
			row.label = firstNonEmpty(strings.TrimSpace(item.Definition.Name), string(item.Definition.Handle))
		}
		if !item.Definition.Configurable {
			row.action = subagentActionNoop
		}
		rows = append(rows, row)
	}
	rows = append(rows,
		subagentOverlayRow{action: subagentActionNewRole, key: "new", section: "Custom roles", label: "+ new", detail: "Choose a name, purpose and model", enabled: true},
		subagentOverlayRow{action: subagentActionSaveSet, key: "save", section: "Snapshots", label: "Save binding set…", detail: "Snapshot all active bindings", enabled: true},
	)
	return rows
}

func (m *Model) subagentBindingRows() []subagentOverlayRow {
	state := m.subagentOverlay
	handle := state.bindingHandle
	resetDetail := "Remove the explicit binding"
	switch handle {
	case agentbinding.HandleGuardian:
		resetDetail = "Use the provider-backed default"
	case agentbinding.HandleReviewer:
		resetDetail = "Use the Main Agent default"
	case agentbinding.HandleSteward:
		resetDetail = "Use static zero-token Memory"
	}
	var rows []subagentOverlayRow
	if !state.creatingRole {
		rows = append(rows, subagentOverlayRow{
			action: subagentActionOpenBinding, key: "binding:reset",
			label: "Default", detail: resetDetail, handle: handle, reset: true, enabled: true,
			current: state.currentBinding().ProfileID == "",
		})
	}
	nameCounts := subagentProfileNameCounts(handle, state.status.Targets)
	for _, profile := range state.status.Targets {
		if !agentbinding.SupportsProfile(handle, profile) {
			continue
		}
		efforts := subagentProfileEfforts(profile)
		if len(efforts) == 0 {
			continue
		}
		effort := state.subagentBindingEffort(profile, efforts)
		effortIndex := indexOfString(efforts, effort)
		binding := agentbinding.Binding{Handle: handle, ProfileID: profile.ID, Effort: effort}
		if state.currentBinding().ProfileID == profile.ID {
			binding.Speed = state.currentBinding().Speed
		}
		if speed, ok := state.selectedSpeedByProfile[profile.ID]; ok {
			binding.Speed = speed
		}
		effectiveSpeed := firstNonEmpty(binding.Speed, profile.Speed.DefaultSpeed)
		rows = append(rows, subagentOverlayRow{
			action: subagentActionOpenBinding,
			key:    "binding:" + profile.ID,
			label:  subagentProfileDisplayName(profile),
			detail: subagentTargetDetail(
				profile,
				nameCounts[subagentProfileNameKey(profile)] > 1,
			),
			efforts: efforts, effortIndex: effortIndex, fastSupported: profile.SupportsFast(), fastMode: effectiveSpeed == "fast",
			handle: handle, binding: binding, enabled: true,
			current: modelprofile.NormalizeID(state.currentBinding().ProfileID) == modelprofile.NormalizeID(profile.ID),
		})
	}
	return rows
}

func subagentProfileEfforts(profile modelprofile.ModelProfile) []string {
	efforts := make([]string, 0, len(profile.Effort.Choices))
	for _, choice := range profile.Effort.Choices {
		if effort := strings.TrimSpace(choice.Canonical); effort != "" {
			efforts = append(efforts, effort)
		}
	}
	return efforts
}

func (s *subagentOverlayState) subagentBindingEffort(profile modelprofile.ModelProfile, efforts []string) string {
	if s.selectedEffortByProfile == nil {
		s.selectedEffortByProfile = make(map[string]string)
	}
	profileID := modelprofile.NormalizeID(profile.ID)
	if effort := strings.TrimSpace(s.selectedEffortByProfile[profileID]); indexOfString(efforts, effort) >= 0 {
		return effort
	}
	seed := s.currentBinding()
	effort := strings.TrimSpace(profile.Effort.DefaultEffort)
	if modelprofile.NormalizeID(seed.ProfileID) == profileID && indexOfString(efforts, strings.TrimSpace(seed.Effort)) >= 0 {
		effort = strings.TrimSpace(seed.Effort)
	}
	if indexOfString(efforts, effort) < 0 {
		effort = efforts[0]
	}
	s.selectedEffortByProfile[profileID] = effort
	return effort
}

func indexOfString(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
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
			label: set.Name, detail: detail, current: set.Active, enabled: set.Available && !set.Active,
		})
	}
	return rows
}

func (m *Model) subagentNewRoleRows() []subagentOverlayRow {
	state := m.subagentOverlay
	binding := "Choose a model and effort  ›"
	if state.roleBinding.ProfileID != "" {
		binding = subagentBindingDisplay(state.roleBinding, state.status.Targets)
	}
	return []subagentOverlayRow{
		{action: subagentActionFieldHandle, key: "field:handle", section: "New custom role", label: "Handle", detail: subagentFieldValue(state.roleHandle, "lowercase handle"), enabled: true},
		{action: subagentActionFieldDesc, key: "field:description", label: "Description", detail: subagentFieldValue(state.roleDescription, "what this Agent is good at"), enabled: true},
		{action: subagentActionFieldBind, key: "field:binding", label: "Binding", detail: binding, enabled: true},
		{action: subagentActionCreateRole, key: "create", section: "Actions", label: "Create role", detail: "Save role and model binding", enabled: !state.pending},
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
		switch item.Definition.Handle {
		case agentbinding.HandleGuardian:
			return "Provider-backed default"
		case agentbinding.HandleSteward:
			return "Static (zero-token)"
		default:
			return "Main Agent default"
		}
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

func subagentTargetDetail(profile modelprofile.ModelProfile, duplicateName bool) string {
	detail := ""
	switch profile.Kind() {
	case modelprofile.BackendACP:
		detail = "ACP"
		if duplicateName {
			detail += " · " + strings.TrimSpace(profile.Backend.ACP.AgentID)
		}
	case modelprofile.BackendProvider:
		if duplicateName {
			if source := subagentProviderSource(profile); source != "" {
				detail = source
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
	speed := ""
	for _, profile := range profiles {
		if modelprofile.NormalizeID(profile.ID) == modelprofile.NormalizeID(binding.ProfileID) {
			name = subagentProfileDisplayName(profile)
			if profile.SupportsFast() {
				speed = " · Fast off"
				if firstNonEmpty(binding.Speed, profile.Speed.DefaultSpeed) == "fast" {
					speed = " · Fast on"
				}
			}
			break
		}
	}
	if name == "" {
		name = strings.TrimSpace(binding.ProfileID)
	}
	return name + " [" + strings.TrimSpace(binding.Effort) + "]" + speed
}

func subagentFieldValue(value, placeholder string) string {
	if strings.TrimSpace(value) == "" {
		return "‹" + placeholder + "›"
	}
	return value
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

func (s *subagentOverlayState) currentBinding() agentbinding.Binding {
	if s.creatingRole {
		return s.roleBinding
	}
	for _, item := range s.status.Handles {
		if item.Definition.Handle == s.bindingHandle {
			return item.Binding
		}
	}
	return agentbinding.Binding{}
}
