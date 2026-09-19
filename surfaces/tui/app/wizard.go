package tuiapp

import (
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// ---------------------------------------------------------------------------
// Wizard — declarative multi-step command flows
//
// A WizardDef describes a sequence of named steps that the user walks through
// when invoking a slash command (e.g. /connect or /disconnect). Each step collects
// one value — either by selecting from a completion list or by free-form text
// input — and stores it in a string map keyed by [WizardStepDef.Key].
//
// Configuration commands present steps in overlays; other registered wizards
// use the composer. The engine lives inside the TUI model. The CLI layer only
// provides the definitions (through [Config.Wizards]) and the completion
// candidates (through [Config.SlashArgComplete]).
// ---------------------------------------------------------------------------

// WizardStepDef describes one step in a wizard flow.
type WizardStepDef struct {
	// Key is the storage key for this step's value in the state map.
	Key string

	// HintLabel is the text shown in the hint bar, e.g. "/connect provider".
	HintLabel string

	// FreeformHint is shown when no candidates are available, e.g.
	// "/connect model: type model name and press enter".
	// When empty a default "<HintLabel>: ↑/↓ select │ enter: apply │ tab: fill"
	// hint is used if candidates exist, or nothing otherwise.
	FreeformHint string

	// HideInput masks the typed text in the input bar (e.g. for API keys).
	HideInput bool

	// NoCompletion suppresses candidate listing for this step. The user must
	// type a value and press enter. If HideInput is true, NoCompletion is
	// implicitly true.
	NoCompletion bool

	// RequireCandidate accepts only an explicit completion candidate selection.
	// It is useful for enumerated steps such as provider selection, while later
	// steps can still allow custom base URLs or model names.
	RequireCandidate bool

	// MultiSelect renders eligible completion candidates as checkboxes. Space
	// and mouse clicks toggle the highlighted candidate before enter confirms
	// the selection; tab keeps its ordinary completion or selection move. Enter
	// still keeps the one-candidate fast path, and free-form input remains a
	// single value.
	MultiSelect bool

	// MultiSelectCandidate optionally decides whether a completion candidate may
	// be accumulated. Nil accepts every candidate.
	MultiSelectCandidate func(candidate SlashArgCandidate) bool

	// CompletionCommand returns the command string passed to
	// Config.SlashArgComplete for this step. It receives the accumulated
	// state from previous steps. If it returns "", no completion is requested.
	CompletionCommand func(state map[string]string) string

	// ShouldSkip returns true to skip this step. If nil, the step is never
	// skipped. It receives the accumulated state from previous steps.
	ShouldSkip func(state map[string]string) bool

	// Validate checks the entered value. Return a non-nil error to reject the
	// value and stay on the current step. If nil, any non-empty string is
	// accepted.
	Validate func(value string) error
}

// WizardDef describes a complete multi-step wizard flow bound to a slash
// command.
type WizardDef struct {
	// Command is the slash command that triggers this wizard (e.g. "connect").
	Command string

	// Steps is the ordered list of wizard steps.
	Steps []WizardStepDef

	// DisplayLine is shown in the input history instead of the full exec
	// line. When empty the exec line itself is displayed.
	DisplayLine string

	// BuildExecLine constructs the final command line from the accumulated
	// state map. It is called after the last step is confirmed.
	BuildExecLine func(state map[string]string) string

	// OnStepConfirm is called after a step value is accepted, before
	// advancing. It may mutate the state map (e.g. to set flags like
	// "_noauth"). The candidate pointer is non-nil only when the user picked
	// from the completion list (as opposed to typing free-form).
	// stepKey is the Key of the just-confirmed step.
	OnStepConfirm func(stepKey string, value string, candidate *SlashArgCandidate, state map[string]string)

	// Branch optionally selects a new explicit wizard flow after a step is
	// confirmed. The new flow inherits the accumulated state and starts at its
	// first eligible step.
	Branch func(stepKey string, value string, candidate *SlashArgCandidate, state map[string]string) *WizardDef
}

// ---------------------------------------------------------------------------
// Runtime state
// ---------------------------------------------------------------------------

// wizardRuntime holds the mutable state of an active wizard session.
type wizardRuntime struct {
	def             *WizardDef
	stepIndex       int
	state           map[string]string
	multiSelections map[string][]string
}

// currentStep returns the current step definition, or nil if out of range.
func (w *wizardRuntime) currentStep() *WizardStepDef {
	if w == nil || w.stepIndex < 0 || w.stepIndex >= len(w.def.Steps) {
		return nil
	}
	return &w.def.Steps[w.stepIndex]
}

// completionCommand returns the command string for this step.
// For no-completion / hidden-input steps the string is still useful
// for state introspection (e.g. tests); the caller decides whether
// to actually request candidates.
func (w *wizardRuntime) completionCommand() string {
	step := w.currentStep()
	if step == nil || step.CompletionCommand == nil {
		return ""
	}
	return step.CompletionCommand(w.state)
}

// hideInput indicates whether the current step should mask input.
func (w *wizardRuntime) hideInput() bool {
	step := w.currentStep()
	return step != nil && step.HideInput
}

// noCompletion returns true if the current step suppresses candidate listing.
func (w *wizardRuntime) noCompletion() bool {
	step := w.currentStep()
	if step == nil {
		return true
	}
	return step.NoCompletion || step.HideInput
}

// ---------------------------------------------------------------------------
// Model integration
// ---------------------------------------------------------------------------

// findWizard looks up a registered wizard definition by command name.
func (m *Model) findWizard(command string) *WizardDef {
	cmd := strings.ToLower(strings.TrimSpace(command))
	for i := range m.cfg.Wizards {
		if strings.ToLower(strings.TrimSpace(m.cfg.Wizards[i].Command)) == cmd {
			return &m.cfg.Wizards[i]
		}
	}
	return nil
}

// startWizard initialises a new wizard session and opens the first eligible
// step. It clears any existing slash-arg / wizard state.
func (m *Model) startWizard(def *WizardDef) tea.Cmd {
	return m.startWizardWithQuery(def, "")
}

func (m *Model) startWizardWithQuery(def *WizardDef, initialQuery string) tea.Cmd {
	m.clearWizard()
	m.clearMention()
	m.clearSlashCompletion()
	if def.Command == "connect" || def.Command == "disconnect" || def.Command == "plugin" {
		m.wizardOverlay = &wizardOverlayState{drafts: make(map[string]wizardStepDraft)}
		if fields := strings.Fields(m.textarea.Value()); len(fields) > 0 && fields[0] == "/"+def.Command {
			m.setInputText("")
			m.syncTextareaFromInput()
		}
	}

	m.wizard = &wizardRuntime{
		def:             def,
		stepIndex:       -1, // will be advanced below
		state:           make(map[string]string),
		multiSelections: make(map[string][]string),
	}

	// Open the first non-skipped step.
	return m.advanceWizardStepWithQuery("", initialQuery)
}

func (m *Model) advanceWizardCursor() bool {
	w := m.wizard
	if w == nil {
		return false
	}
	for {
		w.stepIndex++
		if w.stepIndex >= len(w.def.Steps) {
			return false
		}
		step := &w.def.Steps[w.stepIndex]
		if step.ShouldSkip != nil && step.ShouldSkip(w.state) {
			continue
		}
		return true
	}
}

// advanceWizardStep stores the given value for the current step (if any),
// invokes OnStepConfirm, and moves to the next non-skipped step. When all
// steps are exhausted it builds the exec line and submits it.
//
// The candidate pointer is non-nil when the value came from a list selection.
func (m *Model) advanceWizardStep(value string, candidateOpt ...*SlashArgCandidate) tea.Cmd {
	return m.advanceWizardStepWithQuery(value, "", candidateOpt...)
}

func (m *Model) advanceWizardStepWithQuery(value string, initialQuery string, candidateOpt ...*SlashArgCandidate) tea.Cmd {
	w := m.wizard
	if w == nil {
		return nil
	}

	if m.wizardOverlay != nil {
		m.rememberWizardStep()
	}

	// Store current step value.
	if step := w.currentStep(); step != nil {
		w.state[step.Key] = value
		var cand *SlashArgCandidate
		if len(candidateOpt) > 0 {
			cand = candidateOpt[0]
		}
		if w.def.OnStepConfirm != nil {
			w.def.OnStepConfirm(step.Key, value, cand, w.state)
		}
		if w.def.Branch != nil {
			if next := w.def.Branch(step.Key, value, cand, w.state); next != nil {
				w.def = next
				w.stepIndex = -1
			}
		}
	}

	// Advance to next non-skipped step.
	if !m.advanceWizardCursor() {
		return m.wizardSubmit()
	}

	// Open the new step. Cancel any ordinary completion still serving the
	// previous step before changing the target identity.
	m.cancelSlashArgRequest()
	m.slashArgActive = true
	m.slashArgCommand = w.completionCommand()
	m.slashArgQuery = strings.TrimSpace(initialQuery)
	m.slashArgIndex = 0
	m.slashArgCandidates = nil
	m.slashArgCandidateCommand = ""
	m.slashArgCompletionSettled = false
	if m.wizardOverlay != nil {
		m.openWizardStep(strings.TrimSpace(initialQuery))
	} else {
		m.setInputText(strings.TrimSpace(initialQuery))
		m.syncTextareaFromInput()
	}
	return m.beginSlashArgLoad()
}

// wizardSubmit builds the exec line and submits it.
func (m *Model) wizardSubmit() tea.Cmd {
	if m.wizardOverlay != nil {
		return m.submitWizardOverlay()
	}
	w := m.wizard
	if w == nil || w.def.BuildExecLine == nil {
		m.clearWizard()
		return nil
	}
	execLine := w.def.BuildExecLine(w.state)
	displayLine := w.def.DisplayLine
	if displayLine == "" {
		displayLine = execLine
	}
	m.clearWizard()
	_, cmd := m.submitLineWithDisplay(execLine, displayLine)
	return cmd
}

// clearWizard resets all wizard and slash-arg state.
func (m *Model) clearWizard() {
	if s := m.wizardOverlay; s != nil && s.bot != nil && s.bot.cancel != nil {
		s.bot.cancel()
	}
	m.wizardOverlay = nil
	m.cancelSlashArgRequest()
	m.slashArgLoadSeq++
	m.cancelSlashArgLoad()
	m.slashArgLoadPending = false
	m.slashArgLoadCommand = ""
	m.slashArgLoadLabel = ""
	m.slashArgLoadStartedAt = time.Time{}
	m.slashArgLoadAuthURL = ""
	m.slashArgLoadAuthCode = ""
	m.slashArgLoadAuthPrompt = nil
	m.slashArgLoaded = false
	m.slashArgLoadedCommand = ""
	m.slashArgLoadedCandidates = nil
	if !m.turnRunning() {
		m.stopRunningAnimation()
	}
	m.wizard = nil
	m.slashArgActive = false
	m.slashArgCommand = ""
	m.slashArgQuery = ""
	m.slashArgCandidates = nil
	m.slashArgCandidateCommand = ""
	m.slashArgCompletionSettled = false
	m.slashArgIndex = 0
	m.modelPicker = nil
}

// isWizardActive returns true when a multi-step wizard is in progress.
func (m *Model) isWizardActive() bool {
	return m.wizard != nil
}

// wizardHintText returns the hint text for the current wizard step.
func (m *Model) wizardHintText() string {
	w := m.wizard
	if w == nil {
		return ""
	}
	step := w.currentStep()
	if step == nil {
		return ""
	}
	if len(m.slashArgCandidates) == 0 {
		if step.FreeformHint != "" {
			return step.FreeformHint
		}
		if step.HideInput || step.NoCompletion {
			label := step.HintLabel
			if label == "" {
				label = "/" + w.def.Command + " " + step.Key
			}
			return label + ": type and press enter"
		}
		return ""
	}
	label := step.HintLabel
	if label == "" {
		label = "/" + w.def.Command + " " + step.Key
	}
	if step.MultiSelect {
		return label + "  ↑/↓ select · space toggle · enter confirm"
	}
	return m.overlayHintText(label)
}

// handleWizardEnter processes the enter key when a wizard is active.
// Returns (handled bool, cmd tea.Cmd).
func (m *Model) handleWizardEnter() (bool, tea.Cmd) {
	w := m.wizard
	if w == nil {
		return false, nil
	}
	step := w.currentStep()
	if step == nil {
		return false, nil
	}

	// A multi-select step with accumulated candidates confirms them when enter is
	// pressed on an empty query. This must run before ordinary candidate matching,
	// where an empty query otherwise selects the highlighted next candidate.
	value := strings.TrimSpace(m.slashArgQuery)
	selectedValues := wizardMultiSelectValues(w, step)
	if step.MultiSelect && len(selectedValues) > 0 && (value == "" || m.wizardOverlay != nil) {
		formatted := strings.Join(selectedValues, ",")
		complete := SlashArgCandidate{
			Value:                 formatted,
			ModelMetadataComplete: true,
			ModelImageInputKnown:  true,
		}
		return true, m.advanceWizardStep(formatted, &complete)
	}

	// Determine the entered value.
	var candidate *SlashArgCandidate
	if len(m.slashArgCandidates) > 0 && m.slashArgIndex >= 0 && m.slashArgIndex < len(m.slashArgCandidates) {
		c := m.slashArgCandidates[m.slashArgIndex]
		if slashArgCandidateMatchesQuery(value, c) {
			value = strings.TrimSpace(c.Value)
			candidate = &c
		}
	}

	// Validate.
	if value == "" {
		return true, nil // ignore empty enter
	}
	if step.RequireCandidate && candidate == nil {
		return true, nil
	}
	if step.Validate != nil {
		if err := step.Validate(value); err != nil {
			return true, nil // validation failed — stay
		}
	}
	if step.MultiSelect && candidate != nil {
		selectedValues = appendUniqueWizardValue(selectedValues, value)
		formatted := strings.Join(selectedValues, ",")
		complete := SlashArgCandidate{
			Value:                 formatted,
			ModelMetadataComplete: candidate.ModelMetadataComplete,
			ModelImageInputKnown:  candidate.ModelImageInputKnown,
		}
		return true, m.advanceWizardStep(formatted, &complete)
	}

	cmd := m.advanceWizardStep(value, candidate)
	return true, cmd
}

func wizardMultiSelectValues(w *wizardRuntime, step *WizardStepDef) []string {
	if w == nil || step == nil || !step.MultiSelect {
		return nil
	}
	return append([]string(nil), w.multiSelections[step.Key]...)
}

func appendUniqueWizardValue(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if strings.EqualFold(strings.TrimSpace(existing), value) {
			return values
		}
	}
	return append(values, value)
}

func (m *Model) toggleWizardMultiSelectCandidate(candidate SlashArgCandidate) (bool, tea.Cmd) {
	if m == nil || m.wizard == nil {
		return false, nil
	}
	step := m.wizard.currentStep()
	if step == nil || !step.MultiSelect || !wizardCandidateSupportsMultiSelect(step, candidate) {
		return false, nil
	}
	selectedIndex := m.slashArgIndex
	values := wizardMultiSelectValues(m.wizard, step)
	if wizardMultiSelectContains(values, candidate.Value) {
		values = removeWizardMultiSelectValue(values, candidate.Value)
	} else {
		values = appendUniqueWizardValue(values, candidate.Value)
	}
	if m.wizard.multiSelections == nil {
		m.wizard.multiSelections = make(map[string][]string)
	}
	m.wizard.multiSelections[step.Key] = append([]string(nil), values...)
	if len(values) == 0 {
		delete(m.wizard.state, step.Key)
	} else {
		m.wizard.state[step.Key] = strings.Join(values, ",")
	}
	if m.wizardOverlay != nil {
		// Checkbox changes do not change the available catalog. Keep the search
		// and row stable without repeating discovery or authentication.
		m.slashArgCommand = m.wizard.completionCommand()
		m.slashArgCandidateCommand = m.slashArgCommand
		if m.slashArgLoaded {
			m.slashArgLoadedCommand = m.slashArgCommand
		}
		return true, nil
	}
	m.slashArgQuery = ""
	m.setInputText("")
	m.syncTextareaFromInput()
	command := m.wizard.completionCommand()
	if isAsyncSlashArgCommand(command) && m.slashArgLoaded && sameAsyncSlashArgCatalog(m.slashArgLoadedCommand, command) {
		m.applySlashArgCandidates(command, "", m.slashArgLoadedCandidates, nil)
		m.slashArgIndex = normalizeFilteredSelection(selectedIndex, "", "", len(m.slashArgCandidates))
		return true, nil
	}
	cmd := m.requestCurrentSlashArgCompletion()
	// The selected values are part of the wizard command payload, so refreshing
	// an otherwise unchanged catalog may reset the ordinary completion target.
	// Keep the user's row while that refresh is pending; the result application
	// will clamp it if the catalog became shorter.
	m.slashArgIndex = selectedIndex
	return true, cmd
}

func wizardMultiSelectContains(values []string, value string) bool {
	value = strings.TrimSpace(value)
	for _, existing := range values {
		if strings.EqualFold(strings.TrimSpace(existing), value) {
			return true
		}
	}
	return false
}

func removeWizardMultiSelectValue(values []string, value string) []string {
	value = strings.TrimSpace(value)
	out := make([]string, 0, len(values))
	for _, existing := range values {
		if strings.EqualFold(strings.TrimSpace(existing), value) {
			continue
		}
		out = append(out, existing)
	}
	return out
}

func wizardCandidateSupportsMultiSelect(step *WizardStepDef, candidate SlashArgCandidate) bool {
	if step == nil || !step.MultiSelect {
		return false
	}
	if step.MultiSelectCandidate != nil {
		return step.MultiSelectCandidate(candidate)
	}
	return true
}

// wizardQueryAtCursor returns the inline wizard input before the cursor.
func wizardQueryAtCursor(input []rune, cursor int) string {
	return strings.TrimSpace(string(input[:clampInt(cursor, 0, len(input))]))
}

// ValidateInt accepts valid integer strings.
func ValidateInt(value string) error {
	_, err := strconv.Atoi(strings.TrimSpace(value))
	return err
}
