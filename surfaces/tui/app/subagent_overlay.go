package tuiapp

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

type subagentOverlayPage int

const (
	subagentPageMain subagentOverlayPage = iota
	subagentPageBinding
	subagentPageClassifier
)

type subagentOverlayRow struct {
	key         string
	label       string
	detail      string
	search      string
	efforts     []string
	effortIndex int
	handle      agentbinding.Handle
	binding     agentbinding.Binding
	reset       bool
}

type subagentOverlayGeometry struct {
	x       int
	y       int
	width   int
	height  int
	rows    []int
	efforts []pickerEffortHit
}

type subagentOverlayNav struct {
	index int
	key   string
	query string
}

type subagentList struct {
	index       int
	rows        []subagentOverlayRow
	query       string
	windowStart int
}

type subagentOverlayState struct {
	subagentList
	page           subagentOverlayPage
	status         agentbinding.Status
	loading        bool
	pending        bool
	reloadRequired bool
	err            string
	request        uint64
	geometry       subagentOverlayGeometry
	pressedKey     string
	pressedDelta   int
	parent         subagentOverlayNav
	bindingParent  subagentOverlayNav
	notice         string

	bindingHandle           agentbinding.Handle
	selectedEffortByProfile map[string]string
	stagedBinding           agentbinding.Binding
	hasClassifiers          bool
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
	service := m.cfg.ControlService
	if service == nil {
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

func (m *Model) runSubagentMutation(bindings []agentbinding.Binding) tea.Cmd {
	state := m.subagentOverlay
	service, ok := m.cfg.ControlService.(agentbinding.DraftService)
	if !ok || state.status.ConfigurationRevision == nil {
		state.err = "This Host cannot save Team drafts. Update the Host and reopen /team."
		return nil
	}
	revision := *state.status.ConfigurationRevision
	state.pending = true
	state.err = ""
	m.subagentRequestSeq++
	request := m.subagentRequestSeq
	state.request = request
	ctx := contextOrBackground(m.cfg.Context)
	return func() tea.Msg {
		status, err := service.ApplyAgentBindingDraft(ctx, revision, bindings)
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
		var receipt *appserver.CommandReceiptError
		if errors.As(msg.err, &receipt) && receipt.Receipt.Outcome == appserver.OutcomeConflicted {
			state.reloadRequired = true
			state.err = "Team changed on the Host. Press Enter to reload."
		}
		return nil
	}
	state.status = msg.status
	state.err = ""
	if wasPending && state.page != subagentPageMain {
		m.returnToSubagentMain()
	} else {
		m.refreshSubagentRows(m.currentSubagentRow().key)
	}
	if wasPending {
		state.notice = "Configuration saved."
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

func (m *Model) allSubagentRows() []subagentOverlayRow {
	state := m.subagentOverlay
	if state == nil {
		return nil
	}
	switch state.page {
	case subagentPageBinding:
		return m.subagentBindingRows(state.bindingHandle)
	case subagentPageClassifier:
		return m.subagentBindingRows(agentbinding.HandleGuardianScreen)
	default:
		return m.subagentMainRows()
	}
}

func (m *Model) subagentMainRows() []subagentOverlayRow {
	state := m.subagentOverlay
	var rows []subagentOverlayRow
	for _, item := range state.status.Handles {
		def := item.Definition
		if !def.Configurable || def.Custom || def.Handle == agentbinding.HandleGuardianScreen || agentbinding.IsRetired(def.Handle) {
			continue
		}
		row := subagentOverlayRow{
			key:     "handle:" + string(def.Handle),
			label:   string(def.Handle),
			detail:  subagentBindingDetail(item),
			binding: item.Binding,
			handle:  def.Handle,
			search:  def.Description,
		}
		if def.Class == agentbinding.HandleClassSystem {
			row.label = firstNonEmpty(strings.TrimSpace(def.Name), string(def.Handle))
		}
		rows = append(rows, row)
	}
	return rows
}

func (m *Model) subagentBindingRows(handle agentbinding.Handle) []subagentOverlayRow {
	state := m.subagentOverlay
	current := state.currentBinding(handle)
	var rows []subagentOverlayRow
	if handle == agentbinding.HandleGuardianScreen {
		rows = append(rows, subagentOverlayRow{
			key: "binding:reset", label: "Disabled", handle: handle, reset: true,
			binding: agentbinding.Binding{Handle: handle},
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
		effort := state.subagentBindingEffort(current, profile, efforts)
		effortIndex := indexOfString(efforts, effort)
		binding := agentbinding.Binding{Handle: handle, ProfileID: profile.ID, Effort: effort}
		if modelprofile.NormalizeID(current.ProfileID) == modelprofile.NormalizeID(profile.ID) {
			binding.Speed = current.Speed
		}
		rows = append(rows, subagentOverlayRow{
			key:   "binding:" + profile.ID,
			label: subagentProfileDisplayName(profile),
			detail: subagentTargetDetail(
				profile,
				nameCounts[subagentProfileNameKey(profile)] > 1,
			),
			efforts: efforts, effortIndex: effortIndex,
			handle: handle, binding: binding,
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

func (s *subagentOverlayState) subagentBindingEffort(seed agentbinding.Binding, profile modelprofile.ModelProfile, efforts []string) string {
	if s.selectedEffortByProfile == nil {
		s.selectedEffortByProfile = make(map[string]string)
	}
	profileID := modelprofile.NormalizeID(profile.ID)
	if effort := strings.TrimSpace(s.selectedEffortByProfile[profileID]); indexOfString(efforts, effort) >= 0 {
		return effort
	}
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

func subagentBindingDetail(item agentbinding.HandleStatus) string {
	if agentbinding.IsBound(item) {
		return firstNonEmpty(subagentProfileDisplayName(item.Profile), strings.TrimSpace(item.Binding.ProfileID))
	}
	if item.Definition.Class == agentbinding.HandleClassSystem {
		return "Session default"
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
	return pickerProviderFromConfigID(profile.Backend.Provider.ModelConfigID)
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

func (s *subagentOverlayState) currentBinding(handle agentbinding.Handle) agentbinding.Binding {
	for _, item := range s.status.Handles {
		if item.Definition.Handle == handle {
			return item.Binding
		}
	}
	return agentbinding.Binding{}
}

func (s *subagentOverlayState) boundClassifierName() string {
	for _, item := range s.status.Handles {
		if item.Definition.Handle == agentbinding.HandleGuardianScreen && agentbinding.IsBound(item) {
			if name := subagentProfileDisplayName(item.Profile); name != "" {
				return name
			}
			id := modelprofile.NormalizeID(item.Binding.ProfileID)
			for _, target := range s.status.Targets {
				if modelprofile.NormalizeID(target.ID) == id {
					if name := subagentProfileDisplayName(target); name != "" {
						return name
					}
				}
			}
			return strings.TrimSpace(item.Binding.ProfileID)
		}
	}
	return ""
}
