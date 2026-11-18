package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

var errWizardSubmissionCanceled = errors.New("wizard submission canceled by user")

func wizardSubmissionCanceledByUser(ctx context.Context, err error) bool {
	return errors.Is(context.Cause(ctx), errWizardSubmissionCanceled) &&
		(errors.Is(err, context.Canceled) || errors.Is(err, errWizardSubmissionCanceled)) &&
		activeSubmissionErrorOutcome(err) == appserver.OutcomeUnknown
}

type wizardField struct {
	key, label, value, placeholder string
	secret, choice                 bool
}

// Configuration overlays share the wizard's commands and catalog requests. Its local history
// stores presentation drafts only; authentication and mutations stay in Control.
type wizardOverlayState struct {
	parents       []wizardStepDraft
	drafts        map[string]wizardStepDraft
	fields        []wizardField
	field         int
	cursor        int
	window        int
	err           string
	blocked       bool
	pending       bool
	submitID      uint64
	submitCancel  context.CancelCauseFunc
	formAdvancing bool
	geometry      wizardOverlayGeometry
	pressed       string
	hovered       string
	text          wizardTextSelection
}

type wizardStepDraft struct {
	wizard        wizardRuntime
	query         string
	index         int
	fields        []wizardField
	field         int
	cursor        int
	loadedCommand string
	candidates    []SlashArgCandidate
}

type wizardOverlayGeometry struct {
	x, y, width, height              int
	closeX, closeY                   int
	rows                             []int
	actionX, actionY                 int
	actionWidth                      int
	backX, backY, backWidth          int
	contentX, contentY, contentWidth int
}

func (m *Model) wizardStepKey() string {
	if m.wizard == nil || m.wizard.currentStep() == nil {
		return ""
	}
	return m.wizard.currentStep().Key
}

func (m *Model) wizardDraftKey() string {
	w := cloneWizardRuntime(*m.wizard)
	if m.wizardStepKey() == "model" {
		delete(w.state, "model")
	}
	key := strings.Join([]string{m.wizardStepKey(), w.completionCommand(), w.state["source"], w.state["provider"], w.state["baseurl"], w.state["acp_agent"], w.state["acp_launcher"]}, "\x00")
	for _, field := range m.wizardOverlay.fields {
		if field.key == "model" || field.key == "endpoint" {
			key += "\x00custom"
		}
	}
	return key
}

func cloneWizardRuntime(w wizardRuntime) wizardRuntime {
	w.state = maps.Clone(w.state)
	w.multiSelections = maps.Clone(w.multiSelections)
	for key, values := range w.multiSelections {
		w.multiSelections[key] = append([]string(nil), values...)
	}
	return w
}

func (m *Model) rememberWizardStep() {
	s := m.wizardOverlay
	if s == nil || s.formAdvancing || m.wizard.currentStep() == nil {
		return
	}
	draft := wizardStepDraft{
		wizard: cloneWizardRuntime(*m.wizard), query: m.slashArgQuery, index: m.slashArgIndex,
		fields: append([]wizardField(nil), s.fields...), field: s.field, cursor: s.cursor,
	}
	draft.candidates = append([]SlashArgCandidate(nil), m.slashArgCandidates...)
	if m.slashArgLoaded {
		draft.loadedCommand = m.slashArgLoadedCommand
		draft.candidates = append([]SlashArgCandidate(nil), m.slashArgLoadedCandidates...)
	}
	s.drafts[m.wizardDraftKey()] = draft
	s.parents = append(s.parents, draft)
}

func (m *Model) openWizardStep(query string, draft *wizardStepDraft) {
	s := m.wizardOverlay
	s.fields, s.field, s.cursor, s.window = nil, 0, 0, 0
	s.err, s.blocked = "", false
	s.hovered, s.pressed = "", ""
	s.text = wizardTextSelection{}
	m.cancelSelectionAutoScroll()
	s.submitID = 0
	if s.formAdvancing {
		return
	}
	if draft == nil {
		if saved, ok := s.drafts[m.wizardDraftKey()]; ok {
			draft = &saved
		}
	}
	if draft != nil {
		m.slashArgQuery, m.slashArgIndex = draft.query, draft.index
		m.wizard.multiSelections = cloneWizardRuntime(draft.wizard).multiSelections
		s.fields, s.field, s.cursor = append([]wizardField(nil), draft.fields...), draft.field, draft.cursor
		if draft.loadedCommand != "" {
			m.slashArgLoaded, m.slashArgLoadedCommand = true, draft.loadedCommand
			m.slashArgLoadedCandidates = append([]SlashArgCandidate(nil), draft.candidates...)
		}
	} else {
		m.slashArgQuery = query
		m.initWizardFields()
	}
}

func (m *Model) backWizardOverlay() tea.Cmd {
	s := m.wizardOverlay
	if m.isWizardAuthChoice() {
		return m.handlePromptKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	}
	if s == nil {
		return nil
	}
	if s.pending {
		if s.submitCancel != nil {
			s.submitCancel(errWizardSubmissionCanceled)
			m.clearWizard()
		}
		return nil
	}
	if s.blocked || len(s.parents) == 0 {
		m.clearWizard()
		return nil
	}
	// Save the current draft before restoring the parent. Its identity includes
	// the endpoint/credential payload, so changed upstream choices cannot reuse it.
	parents := s.parents
	m.rememberWizardStep()
	s.parents = parents[:len(parents)-1]
	draft := parents[len(parents)-1]
	m.cancelWizardCatalog()
	w := cloneWizardRuntime(draft.wizard)
	m.wizard = &w
	m.slashArgActive, m.slashArgCommand = true, w.completionCommand()
	m.openWizardStep(draft.query, &draft)
	return m.beginSlashArgLoad()
}

func (m *Model) cancelWizardCatalog() {
	m.cancelSlashArgRequest()
	m.slashArgLoadSeq++
	m.cancelSlashArgLoad()
	m.slashArgLoadPending = false
	m.slashArgLoadCommand, m.slashArgLoadLabel = "", ""
	m.slashArgLoadAuthURL, m.slashArgLoadAuthCode = "", ""
	m.slashArgLoadAuthPrompt = nil
	m.slashArgLoaded, m.slashArgCompletionSettled = false, false
	m.slashArgLoadedCommand, m.slashArgCandidateCommand = "", ""
	m.slashArgLoadedCandidates, m.slashArgCandidates = nil, nil
	if !m.turnRunning() {
		m.stopRunningAnimation()
	}
}

func (m *Model) wizardCatalogReady(err error) tea.Cmd {
	s := m.wizardOverlay
	if s == nil {
		return nil
	}
	s.hovered, s.pressed = "", ""
	s.text.selecting = false
	m.cancelSelectionAutoScroll()
	if err != nil {
		s.err = m.wizardErrorText(err)
		s.blocked = shouldStopACPSetupAfterLoadError(m.slashArgCommand, err)
		return nil
	}
	s.err = ""
	if m.wizardStepKey() == "acp_launcher" && m.slashArgQuery == "" && len(m.slashArgCandidates) == 1 {
		candidate := m.slashArgCandidates[0]
		// A single declared launcher needs no extra choice. Do not add that
		// automatic page to Back's history.
		s.formAdvancing = true
		cmd := m.advanceWizardStep(candidate.Value, &candidate)
		s.formAdvancing = false
		m.initWizardFields()
		return cmd
	}
	m.fillConnectEndpointDefault()
	return nil
}

func (m *Model) submitWizardOverlay() tea.Cmd {
	s := m.wizardOverlay
	if s == nil || s.pending || m.wizard == nil || m.wizard.def.BuildExecLine == nil {
		return nil
	}
	if m.sessionSwitchPending || m.sessionObservationRecovering || m.sessionHistoryFailed || m.turnRunning() {
		s.err = "Session unavailable"
		return nil
	}
	s.pending, s.err = true, ""
	m.cancelWizardCatalog()
	m.slashArgActive = false
	s.submitID = m.allocateSubmissionID()
	opts := submitLineOptions{preserveComposer: true, localID: s.submitID}
	if m.wizard.def.Command == "connect" && m.wizard.state["source"] != "acp" && m.cfg.executeLineCmd != nil {
		if provider, ok := modelconfig.LookupProvider(m.wizard.state["provider"]); ok && provider.AuthFlow != "" {
			// Own cancellation before the render yield. A closed wizard must
			// cancel even when the asynchronous request has yet to start.
			opts.requestContext, s.submitCancel = context.WithCancelCause(contextOrBackground(m.cfg.Context))
		}
	}
	_, cmd := m.submitLineWithDisplayAndAttachmentsOptions(m.wizard.def.BuildExecLine(m.wizard.state), "/"+m.wizard.def.Command, nil, opts)
	return cmd
}

func (m *Model) finishWizardSubmission(msg TaskResultMsg) {
	s := m.wizardOverlay
	if s == nil || !s.pending || msg.localID != s.submitID {
		return
	}
	s.pending = false
	if s.submitCancel != nil {
		s.submitCancel(nil)
		s.submitCancel = nil
	}
	if msg.Err == nil {
		m.clearWizard()
		return
	}
	s.err = m.wizardErrorText(msg.Err)
	outcome := activeSubmissionErrorOutcome(msg.Err)
	if (outcome == appserver.OutcomeRejected || outcome == appserver.OutcomeConflicted) && len(s.parents) > 0 {
		draft := s.parents[len(s.parents)-1]
		s.parents = s.parents[:len(s.parents)-1]
		w := cloneWizardRuntime(draft.wizard)
		m.wizard = &w
		s.fields = append([]wizardField(nil), draft.fields...)
		s.field, s.cursor = draft.field, draft.cursor
		m.slashArgActive, m.slashArgCommand = true, w.completionCommand()
		m.slashArgQuery, m.slashArgIndex = draft.query, draft.index
		m.slashArgLoaded, m.slashArgLoadedCommand = draft.loadedCommand != "", draft.loadedCommand
		m.slashArgLoadedCandidates = append([]SlashArgCandidate(nil), draft.candidates...)
		m.applySlashArgCandidates(m.slashArgCommand, draft.query, draft.candidates, nil)
		return
	}
	// Partial commits and unproven outcomes must never replay this mutation.
	s.fields, s.blocked = nil, true
	clear(s.drafts)
	s.parents = nil
	delete(m.wizard.state, "apikey")
}

func (m *Model) wizardErrorText(err error) string {
	detail := err.Error()
	var remote *httpclient.RemoteError
	if errors.As(err, &remote) && remote.Detail != "" {
		detail = remote.Detail
	}
	text := singleLineErrorText(detail)
	if command := m.slashArgCommand; command != "" {
		text = strings.ReplaceAll(text, command, "/"+m.wizard.def.Command)
	}
	if m.wizard != nil {
		if secret := m.wizard.state["apikey"]; secret != "" {
			text = strings.ReplaceAll(text, secret, "••••")
		}
	}
	for _, field := range m.wizardOverlay.fields {
		if field.secret && field.value != "" {
			text = strings.ReplaceAll(text, field.value, "••••")
		}
	}
	return text
}

func (m *Model) wizardAction() string {
	s := m.wizardOverlay
	if m.isWizardAuthChoice() {
		return "Continue"
	}
	switch {
	case s.pending:
		return "Saving…"
	case s.blocked:
		return "Close"
	case s.err != "" && len(s.fields) == 0:
		return "Retry"
	case m.slashArgLoadPending || m.slashArgRequestPending:
		return "Loading…"
	case m.wizard.def.Command == "disconnect" && m.wizardMultiSelectStep():
		count := len(m.wizard.multiSelections[m.wizardStepKey()])
		if count > 0 {
			return fmt.Sprintf("Disconnect %d", count)
		}
		return "Disconnect"
	case m.wizard.def.Command == "plugin":
		return m.pluginWizardAction()
	case m.wizardStepKey() == "model" && len(s.fields) == 0:
		count := len(m.wizard.multiSelections["model"])
		if count == 0 {
			return "Select a model"
		}
		if count > 1 {
			return fmt.Sprintf("Connect %d models", count)
		}
		if count == 1 {
			return "Connect 1 model"
		}
		if c, ok := m.currentSlashArgCandidate(); ok && c.ModelMetadataComplete && c.ModelImageInputKnown {
			return "Connect"
		}
	case m.wizardStepKey() == "acp_model":
		if count := len(m.wizard.multiSelections["acp_model"]); count > 1 {
			return fmt.Sprintf("Connect %d models", count)
		}
		return "Connect"
	case m.connectCapabilitiesForm():
		return "Connect"
	}
	return "Continue"
}

func (m *Model) wizardSearch(query string) tea.Cmd {
	m.wizardOverlay.err, m.wizardOverlay.window = "", 0
	m.wizardOverlay.submitID = 0
	m.slashArgQuery, m.slashArgIndex = truncateRunes(query, 160), 0
	return m.requestCurrentSlashArgCompletion()
}

func (m *Model) wizardMultiSelectStep() bool {
	return m.wizard != nil && m.wizard.currentStep() != nil && m.wizard.currentStep().MultiSelect
}

func (m *Model) initWizardFields() {
	if m.wizard.def.Command == "plugin" && m.wizardStepKey() == "target" && m.wizard.noCompletion() {
		m.wizardOverlay.fields = []wizardField{{key: "target", label: "Source", placeholder: "plugin@marketplace or directory"}}
		if m.wizard.state["action"] == "marketplace" {
			m.wizardOverlay.fields[0].placeholder = "URL or directory"
		}
		return
	}
	m.initConnectFields()
}
