package tuiapp

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// ---------------------------------------------------------------------------
// Wizard — declarative multi-step inline command framework
//
// A WizardDef describes a sequence of named steps that the user walks through
// when invoking a slash command (e.g. /connect, /model).  Each step collects
// one value — either by selecting from a completion list or by free-form text
// input — and stores it in a string map keyed by [WizardStepDef.Key].
//
// The wizard engine lives entirely inside the TUI model. The CLI layer only
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
}

// ---------------------------------------------------------------------------
// Runtime state
// ---------------------------------------------------------------------------

// wizardRuntime holds the mutable state of an active wizard session.
type wizardRuntime struct {
	def       *WizardDef
	stepIndex int
	state     map[string]string
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
func (m *Model) startWizard(def *WizardDef) {
	m.startWizardWithQuery(def, "")
}

func (m *Model) startWizardWithQuery(def *WizardDef, initialQuery string) {
	m.clearMention()
	m.clearSkill()
	m.clearResume()
	m.clearSlashCompletion()

	m.wizard = &wizardRuntime{
		def:       def,
		stepIndex: -1, // will be advanced below
		state:     make(map[string]string),
	}

	// Open the first non-skipped step.
	m.advanceWizardStepWithQuery("", initialQuery)
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
	}

	// Advance to next non-skipped step.
	if !m.advanceWizardCursor() {
		return m.wizardSubmit()
	}

	// Open the new step.
	m.slashArgActive = true
	m.slashArgCommand = w.completionCommand()
	m.slashArgQuery = strings.TrimSpace(initialQuery)
	m.slashArgIndex = 0
	m.slashArgCandidates = nil
	m.setInputText(strings.TrimSpace(initialQuery))
	m.syncTextareaFromInput()
	m.updateSlashArgCandidates()
	return nil
}

// wizardSubmit builds the exec line and submits it.
func (m *Model) wizardSubmit() tea.Cmd {
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
	m.wizard = nil
	m.slashArgActive = false
	m.slashArgCommand = ""
	m.slashArgQuery = ""
	m.slashArgCandidates = nil
	m.slashArgIndex = 0
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

	// Determine the entered value.
	value := strings.TrimSpace(m.slashArgQuery)
	var candidate *SlashArgCandidate
	if len(m.slashArgCandidates) > 0 && m.slashArgIndex >= 0 && m.slashArgIndex < len(m.slashArgCandidates) {
		c := m.slashArgCandidates[m.slashArgIndex]
		selectedValue := strings.TrimSpace(c.Value)
		if value == "" || strings.EqualFold(value, selectedValue) || strings.EqualFold(value, strings.TrimSpace(c.Display)) {
			value = selectedValue
			candidate = &c
		}
	}

	// Validate.
	if value == "" {
		return true, nil // ignore empty enter
	}
	if step.Validate != nil {
		if err := step.Validate(value); err != nil {
			return true, nil // validation failed — stay
		}
	}

	cmd := m.advanceWizardStep(value, candidate)
	return true, cmd
}

// wizardQueryAtCursor extracts the query text after the wizard command prefix.
func wizardQueryAtCursor(command string, input []rune, cursor int) (string, bool) {
	_ = command
	if len(input) == 0 {
		return "", true
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(input) {
		cursor = len(input)
	}
	raw := string(input[:cursor])
	return strings.TrimSpace(raw), true
}

func wizardVisibleInputAtCursor(command string, input []rune, cursor int) (string, int, bool) {
	_ = command
	if len(input) == 0 {
		return "", 0, true
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(input) {
		cursor = len(input)
	}
	rawFull := string(input)
	return rawFull, len([]rune(string(input[:cursor]))), true
}

// ValidateInt returns a validator that accepts valid integer strings.
func ValidateInt(value string) error {
	_, err := strconv.Atoi(strings.TrimSpace(value))
	return err
}
