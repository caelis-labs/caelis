package tuiapp

import (
	"net/url"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

func (m *Model) initConnectFields() {
	s := m.wizardOverlay
	if s == nil || len(s.fields) > 0 || m.wizard == nil {
		return
	}
	state := m.wizard.state
	add := func(key, label, placeholder string, secret bool) {
		s.fields = append(s.fields, wizardField{key: key, label: label, value: state[key], placeholder: placeholder, secret: secret})
	}
	switch m.wizardStepKey() {
	case "baseurl":
		add("baseurl", "Endpoint", "https://…/v1", false)
		if state["_noauth"] != "true" {
			add("apikey", "API key", "Paste key", true)
		}
	case "apikey":
		add("apikey", "API key", "Paste key", true)
	case "acp_command":
		add("acp_command", "Command", "Executable and arguments", false)
	case "image_input", "context_window_tokens", "max_output_tokens", "reasoning_levels":
		if state["_known_image_input"] != "true" {
			s.fields = append(s.fields, wizardField{key: "image_input", label: "Images", value: firstNonEmpty(state["image_input"], "false"), choice: true})
		}
		if state["_known_model"] != "true" {
			add("context_window_tokens", "Context", "tokens", false)
			add("max_output_tokens", "Max output", "tokens", false)
			add("reasoning_levels", "Reasoning", "low,medium,high · blank if unsupported", false)
		}
	}
	if len(s.fields) > 0 {
		s.field = 0
		s.cursor = len([]rune(s.fields[0].value))
	}
}

func (m *Model) connectCapabilitiesForm() bool {
	for _, f := range m.wizardOverlay.fields {
		if f.key == "image_input" || f.key == "context_window_tokens" {
			return true
		}
	}
	return false
}

func (m *Model) connectEndpointCandidate(value string) *SlashArgCandidate {
	for _, c := range m.slashArgCandidates {
		if c.Value == strings.TrimSpace(value) {
			return &c
		}
	}
	return nil
}

func (m *Model) fillConnectEndpointDefault() {
	s := m.wizardOverlay
	if m.wizardStepKey() != "baseurl" || len(s.fields) == 0 || s.fields[0].value != "" {
		return
	}
	if len(m.slashArgCandidates) > 0 {
		s.fields[0].value = m.slashArgCandidates[0].Value
		s.cursor = len([]rune(s.fields[0].value))
	}
}

func (m *Model) connectKeyOptional() bool {
	if m.wizard.state["_noauth"] == "true" || m.wizard.state["_reuseauth"] == "true" {
		return true
	}
	for _, field := range m.wizardOverlay.fields {
		if field.key == "baseurl" || field.key == "endpoint" {
			candidate := m.connectEndpointCandidate(field.value)
			return candidate != nil && candidate.NoAuth || modelconfig.EndpointUsesNoAuth(m.wizard.state["provider"], field.value)
		}
	}
	return false
}

func (m *Model) openConnectCustomModel() {
	s := m.wizardOverlay
	if s == nil || m.wizardStepKey() != "model" || m.slashArgLoadPending {
		return
	}
	m.openConnectCustomForm([]wizardField{
		{key: "model", label: "Model ID", value: m.slashArgQuery},
		{key: "image_input", label: "Images", value: "false", choice: true},
		{key: "context_window_tokens", label: "Context", placeholder: "tokens"},
		{key: "max_output_tokens", label: "Max output", placeholder: "tokens"},
		{key: "reasoning_levels", label: "Reasoning", placeholder: "low,medium,high · blank if unsupported"},
	})
}

func (m *Model) openConnectCustomEndpoint() {
	value := ""
	if strings.HasPrefix(m.slashArgQuery, "http://") || strings.HasPrefix(m.slashArgQuery, "https://") {
		value = m.slashArgQuery
	}
	fields := []wizardField{{key: "endpoint", label: "Endpoint", value: value, placeholder: "https://…/v1"}}
	if m.wizard.state["_noauth"] != "true" {
		fields = append(fields, wizardField{key: "apikey", label: "API key", placeholder: "Paste key", secret: true})
	}
	m.openConnectCustomForm(fields)
}

func (m *Model) openConnectCustomForm(fields []wizardField) {
	m.rememberWizardStep()
	s := m.wizardOverlay
	s.fields = fields
	s.field, s.cursor, s.window, s.err = 0, len([]rune(fields[0].value)), 0, ""
	if draft, ok := s.drafts[m.wizardDraftKey()]; ok {
		s.fields = append([]wizardField(nil), draft.fields...)
		s.field, s.cursor = draft.field, draft.cursor
	}
}

func (m *Model) submitConnectForm() tea.Cmd {
	s := m.wizardOverlay
	if len(s.fields) == 0 {
		return nil
	}
	values := make(map[string]string, len(s.fields))
	for _, field := range s.fields {
		values[field.key] = strings.TrimSpace(field.value)
	}
	for i, field := range s.fields {
		value, issue := values[field.key], ""
		switch field.key {
		case "baseurl", "endpoint":
			parsed, err := url.Parse(value)
			if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				issue = "Enter an http:// or https:// endpoint."
			}
		case "apikey":
			if value == "" && !m.connectKeyOptional() {
				issue = "Enter an API key."
			}
		case "model":
			if value == "" || strings.ContainsAny(value, " \t\n,") {
				issue = "Enter one model ID."
			}
		case "acp_command":
			if value == "" {
				issue = "Enter an executable and arguments."
			}
		case "context_window_tokens", "max_output_tokens":
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				issue = "Enter a positive token limit."
			} else if field.key == "max_output_tokens" {
				context, _ := strconv.Atoi(values["context_window_tokens"])
				if n > context && context > 0 {
					issue = "Max output exceeds context."
				}
			}
		case "reasoning_levels":
			if strings.ContainsAny(value, " \t\n") {
				issue = "Separate levels with commas."
			}
			if value == "" {
				values[field.key] = "-"
			}
		}
		if issue != "" {
			s.field, s.cursor, s.err = i, len([]rune(field.value)), issue
			return nil
		}
	}
	m.rememberWizardStep()
	s.formAdvancing = true
	defer func() { s.formAdvancing = false }()
	// Reuse the wizard's step-confirm hooks and skip rules as one form commit.
	// No intermediate catalog or authentication work is dispatched.
	for m.wizard != nil {
		step := m.wizard.currentStep()
		if step == nil {
			return m.wizardSubmit()
		}
		value, exists := values[step.Key]
		if !exists {
			break
		}
		var candidate *SlashArgCandidate
		if step.Key == "baseurl" || step.Key == "endpoint" {
			candidate = m.connectEndpointCandidate(value)
		}
		m.wizard.state[step.Key] = value
		if m.wizard.def.OnStepConfirm != nil {
			m.wizard.def.OnStepConfirm(step.Key, value, candidate, m.wizard.state)
		}
		if (step.Key == "baseurl" || step.Key == "endpoint") && values["apikey"] != "" {
			// An explicitly entered key takes precedence over saved credentials.
			delete(m.wizard.state, "_reuseauth")
		}
		if !m.advanceWizardCursor() {
			return m.wizardSubmit()
		}
	}
	m.cancelSlashArgRequest()
	m.slashArgCommand = m.wizard.completionCommand()
	m.slashArgQuery, m.slashArgIndex = "", 0
	m.slashArgCandidates, m.slashArgCandidateCommand = nil, ""
	m.slashArgCompletionSettled = false
	s.formAdvancing = false
	m.openWizardStep("")
	return m.beginSlashArgLoad()
}
