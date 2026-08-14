package tuiapp

import (
	"context"
	"os"
	"os/exec"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	controlagents "github.com/caelis-labs/caelis/control/agents"
)

type acpAuthSelectionRequestMsg struct {
	seq      uint64
	request  controlagents.AuthenticationSelectionRequest
	response chan PromptResponse
}

type acpAuthSelectionCancelMsg struct {
	seq      uint64
	response chan PromptResponse
}

type acpTerminalAuthRequestMsg struct {
	seq      uint64
	ctx      context.Context
	request  controlagents.TerminalAuthenticationRequest
	response chan error
}

type acpTerminalAuthFinishedMsg struct {
	seq      uint64
	response chan error
	err      error
}

func (m *Model) handleACPAuthSelectionRequest(msg acpAuthSelectionRequestMsg) {
	if msg.response == nil {
		return
	}
	if m == nil || msg.seq != m.slashArgLoadSeq || !m.slashArgLoadPending {
		msg.response <- PromptResponse{Err: context.Canceled}
		return
	}
	choices := make([]PromptChoice, 0, len(msg.request.Methods))
	for _, raw := range msg.request.Methods {
		method := controlagents.NormalizeAuthenticationMethod(raw)
		if method.ID == "" {
			continue
		}
		label := method.Name
		if label == "" {
			label = method.ID
		}
		detail := strings.TrimSpace(method.Description)
		if detail == "" {
			detail = string(method.Type)
		}
		choices = append(choices, PromptChoice{Label: label, Value: method.ID, Detail: detail})
	}
	m.slashArgLoadAuthPrompt = msg.response
	m.enqueuePrompt(PromptRequestMsg{
		Title:    acpAuthenticationAgentName(msg.request.AgentID) + " sign-in",
		Prompt:   "Choose an authentication method",
		Choices:  choices,
		Response: msg.response,
	})
	m.ensureViewportLayout()
}

func (m *Model) handleACPAuthSelectionCancel(msg acpAuthSelectionCancelMsg) {
	m.handleModelAuthInputCancel(modelAuthInputCancelMsg(msg))
}

func (m *Model) handleACPTerminalAuthRequest(msg acpTerminalAuthRequestMsg) tea.Cmd {
	if msg.response == nil {
		return nil
	}
	if m == nil || msg.seq != m.slashArgLoadSeq || !m.slashArgLoadPending {
		msg.response <- context.Canceled
		return nil
	}
	request := msg.request
	m.slashArgLoadLabel = "Complete " + acpAuthenticationAgentName(request.AgentID) + " sign-in in the terminal"
	command := terminalAuthenticationCommand(msg.ctx, request)
	return tea.ExecProcess(command, func(err error) tea.Msg {
		return acpTerminalAuthFinishedMsg{
			seq:      msg.seq,
			response: msg.response,
			err:      err,
		}
	})
}

func (m *Model) handleACPTerminalAuthFinished(msg acpTerminalAuthFinishedMsg) {
	if msg.response == nil {
		return
	}
	msg.response <- msg.err
	if m == nil || msg.seq != m.slashArgLoadSeq || !m.slashArgLoadPending {
		return
	}
	if msg.err == nil {
		m.slashArgLoadLabel = "Terminal sign-in complete; reconnecting to the ACP Agent"
	}
}

func terminalAuthenticationCommand(ctx context.Context, request controlagents.TerminalAuthenticationRequest) *exec.Cmd {
	if ctx == nil {
		ctx = context.Background()
	}
	command := exec.CommandContext(ctx, strings.TrimSpace(request.Command), request.Args...)
	command.Dir = strings.TrimSpace(request.WorkDir)
	command.Env = mergedAuthenticationEnvironment(os.Environ(), request.Env)
	return command
}

func mergedAuthenticationEnvironment(base []string, overrides map[string]string) []string {
	values := make(map[string]string, len(base)+len(overrides))
	for _, item := range base {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			continue
		}
		values[key] = value
	}
	for key, value := range overrides {
		if key == "" {
			continue
		}
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		value := values[key]
		out = append(out, key+"="+value)
	}
	return out
}

func acpAuthenticationAgentName(agentID string) string {
	name := acpSetupAdapterDisplayName(agentID)
	if name == "" {
		return "ACP Agent"
	}
	return name
}
