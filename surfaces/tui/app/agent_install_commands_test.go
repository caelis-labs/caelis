package tuiapp

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestSlashAgentInstallUsesTUIPrivateToolProjection(t *testing.T) {
	svc := &agentInstallStubService{
		candidates: []control.SlashArgCandidate{{
			Value:  "claude",
			Detail: "npm install @agentclientprotocol/claude-agent-acp",
		}},
	}
	var messages []tea.Msg
	result := slashAgentPrivateWithContext(context.Background(), svc, func(msg tea.Msg) {
		messages = append(messages, msg)
	}, "install claude")

	if result.Err != nil || result.Interrupted {
		t.Fatalf("slashAgentPrivateWithContext() = %#v, want successful install", result)
	}
	if svc.addedAgent != "claude" || !svc.addedOptions.Install {
		t.Fatalf("added agent=%q opts=%#v, want install claude", svc.addedAgent, svc.addedOptions)
	}
	if !hasAgentInstallToolCall(messages, schema.ToolStatusInProgress) {
		t.Fatalf("messages = %#v, want RunCommand in-progress tool call", messages)
	}
	if !hasAgentInstallToolUpdate(messages, schema.ToolStatusCompleted) {
		t.Fatalf("messages = %#v, want completed tool update", messages)
	}
	if !hasLogChunkContaining(messages, "claude is ready") {
		t.Fatalf("messages = %#v, want ready notice", messages)
	}
}

func TestSlashAgentAddInstallUsesTUIPrivateInstallPath(t *testing.T) {
	svc := &agentInstallStubService{}
	result := slashAgentPrivateWithContext(context.Background(), svc, func(tea.Msg) {}, "add --install codex")
	if result.Err != nil {
		t.Fatalf("slashAgentPrivateWithContext(add --install) error = %v", result.Err)
	}
	if svc.addedAgent != "codex" || !svc.addedOptions.Install {
		t.Fatalf("added agent=%q opts=%#v, want install codex", svc.addedAgent, svc.addedOptions)
	}
}

type agentInstallStubService struct {
	control.Service
	candidates   []control.SlashArgCandidate
	addedAgent   string
	addedOptions control.AgentAddOptions
}

func (s *agentInstallStubService) AddAgentWithOptions(_ context.Context, agent string, opts control.AgentAddOptions) (control.AgentStatusSnapshot, error) {
	s.addedAgent = agent
	s.addedOptions = opts
	return control.AgentStatusSnapshot{}, nil
}

func (s *agentInstallStubService) CompleteSlashArg(context.Context, string, string, int) ([]control.SlashArgCandidate, error) {
	return append([]control.SlashArgCandidate(nil), s.candidates...), nil
}

func (s *agentInstallStubService) ListAgents(context.Context, int) ([]control.AgentCandidate, error) {
	return nil, nil
}

func (s *agentInstallStubService) AgentStatus(context.Context) (control.AgentStatusSnapshot, error) {
	return control.AgentStatusSnapshot{}, nil
}

func hasAgentInstallToolCall(messages []tea.Msg, status string) bool {
	for _, msg := range messages {
		env, ok := msg.(eventstream.Envelope)
		if !ok {
			continue
		}
		call, ok := env.Update.(schema.ToolCall)
		if ok && call.Kind == names.RunCommand && call.Status == status {
			return true
		}
	}
	return false
}

func hasAgentInstallToolUpdate(messages []tea.Msg, status string) bool {
	for _, msg := range messages {
		env, ok := msg.(eventstream.Envelope)
		if !ok {
			continue
		}
		update, ok := env.Update.(schema.ToolCallUpdate)
		if ok && update.Kind != nil && *update.Kind == names.RunCommand && update.Status != nil && *update.Status == status {
			return true
		}
	}
	return false
}

func hasLogChunkContaining(messages []tea.Msg, want string) bool {
	for _, msg := range messages {
		log, ok := msg.(LogChunkMsg)
		if ok && strings.Contains(log.Chunk, want) {
			return true
		}
	}
	return false
}
