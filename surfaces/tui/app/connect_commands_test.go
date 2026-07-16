package tuiapp

import (
	"context"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/protocol/acp/control"
)

type acpConnectControlStub struct {
	control.Service
	req          controlagents.ConnectRequest
	disconnected string
}

type modelConnectControlStub struct {
	control.Service
	agents []control.AgentCandidate
	status control.AgentStatusSnapshot
}

func (*modelConnectControlStub) Connect(context.Context, control.ConnectConfig) (control.StatusSnapshot, error) {
	return control.StatusSnapshot{ModelStatus: control.StatusModel{Display: "openai/gpt-5.6"}}, nil
}

func (s *modelConnectControlStub) AgentStatus(context.Context) (control.AgentStatusSnapshot, error) {
	return s.status, nil
}

func (s *modelConnectControlStub) ListAgents(context.Context, int) ([]control.AgentCandidate, error) {
	if len(s.agents) > 0 {
		return slices.Clone(s.agents), nil
	}
	return []control.AgentCandidate{{Name: "sol", Description: "GPT 5.6 Sol"}}, nil
}

func (s *acpConnectControlStub) DiscoverACPConnection(_ context.Context, _ controlagents.ConnectRequest) (controlagents.DiscoverySnapshot, error) {
	return controlagents.DiscoverySnapshot{}, nil
}

func (s *acpConnectControlStub) ConnectACP(_ context.Context, req controlagents.ConnectRequest) (controlagents.ConnectResult, error) {
	s.req = req
	return controlagents.ConnectResult{Agents: []controlagents.Agent{{ID: "opus"}}}, nil
}

func (s *acpConnectControlStub) DisconnectCandidates(context.Context) ([]controlagents.DisconnectCandidate, error) {
	return []controlagents.DisconnectCandidate{{AgentID: "opus", ConnectionID: "claude", LastOnConnection: true}}, nil
}

func (s *acpConnectControlStub) DisconnectACP(_ context.Context, agentID string) (controlagents.DisconnectResult, error) {
	s.disconnected = agentID
	return controlagents.DisconnectResult{
		Agent: controlagents.Agent{ID: agentID}, ConnectionID: "claude", ConnectionRemoved: true,
	}, nil
}

func TestSlashConnectMapsACPWizardSelectionToConnector(t *testing.T) {
	service := &acpConnectControlStub{}
	payload := buildACPConnectWizardPayload(map[string]string{
		"acp_agent": "claude", "acp_launcher": "global", "acp_model": "opus",
		"acp_config": formatACPConfigSelections([]string{"reasoning_effort=max", "instructions=short, exact=a=b"}),
	})
	result := slashConnectWithContext(context.Background(), service, service, nil, "acp "+payload)
	if result.Err != nil {
		t.Fatalf("slashConnectWithContext() error = %v", result.Err)
	}
	if !result.SuppressTurnDivider {
		t.Fatalf("slashConnectWithContext() = %#v, want local connect result", result)
	}
	if service.req.AdapterID != "claude" || service.req.Launcher != controlagents.LauncherChoiceGlobal {
		t.Fatalf("ConnectACP request = %#v", service.req)
	}
	if service.req.ModelID != "opus" {
		t.Fatalf("ConnectACP model ID = %q", service.req.ModelID)
	}
	if service.req.ConfigValues["reasoning_effort"] != "max" {
		t.Fatalf("ConnectACP config values = %#v", service.req.ConfigValues)
	}
	if service.req.ConfigValues["instructions"] != "short, exact=a=b" {
		t.Fatalf("ConnectACP punctuation-bearing config values = %#v", service.req.ConfigValues)
	}
}

func TestSlashConnectDisconnectsOnlyAfterWizardConfirmation(t *testing.T) {
	service := &acpConnectControlStub{}

	result := slashConnectWithContext(context.Background(), service, service, nil, "disconnect opus")
	if result.Err != nil || !result.SuppressTurnDivider {
		t.Fatalf("unconfirmed result = %#v", result)
	}
	if service.disconnected != "" {
		t.Fatalf("unconfirmed disconnect called for %q", service.disconnected)
	}

	result = slashConnectWithContext(context.Background(), service, service, nil, "disconnect opus confirmed")
	if result.Err != nil || !result.SuppressTurnDivider {
		t.Fatalf("confirmed result = %#v", result)
	}
	if service.disconnected != "opus" {
		t.Fatalf("disconnect called for %q, want opus", service.disconnected)
	}
}

func TestSlashConnectModelRefreshesAgentSlashCommands(t *testing.T) {
	service := &modelConnectControlStub{}
	var commands SetCommandsMsg
	result := slashConnectWithContext(context.Background(), service, nil, func(msg tea.Msg) {
		if update, ok := msg.(SetCommandsMsg); ok {
			commands = update
		}
	}, "openai gpt-5.6")
	if result.Err != nil {
		t.Fatalf("slashConnectWithContext() error = %v", result.Err)
	}
	if !slices.Contains(commands.Commands, "sol") {
		t.Fatalf("refreshed commands = %#v, want sol", commands.Commands)
	}
	if commands.Details["sol"] != "GPT 5.6 Sol" {
		t.Fatalf("refreshed command details = %#v", commands.Details)
	}
}

func TestAgentSlashCommandsKeepRosterGlobalAndRunsSessionScoped(t *testing.T) {
	t.Parallel()

	service := &modelConnectControlStub{
		agents: []control.AgentCandidate{
			{Name: "codex", Description: "Codex ACP Agent"},
			{Name: "claude", Description: "Claude ACP Agent"},
		},
		status: control.AgentStatusSnapshot{Participants: []control.AgentParticipantSnapshot{{
			ID: "participant-1", Label: "@lina", AgentName: "codex", Kind: "acp", Role: "sidecar",
		}}},
	}

	before := appendAgentSlashCommandsWithContext(context.Background(), service, DefaultCommands())
	for _, command := range []string{"codex", "claude", "codex(lina)"} {
		if !slices.Contains(before, command) {
			t.Fatalf("commands before /new = %#v, want %q", before, command)
		}
	}
	details := registeredAgentCommandDetailsWithContext(context.Background(), service)
	if details["codex(lina)"] != "Continue /codex as lina" {
		t.Fatalf("run command details = %#v", details)
	}

	service.status.Participants = nil
	after := appendAgentSlashCommandsWithContext(context.Background(), service, DefaultCommands())
	for _, command := range []string{"codex", "claude"} {
		if !slices.Contains(after, command) {
			t.Fatalf("commands after /new = %#v, want global %q", after, command)
		}
	}
	if slices.Contains(after, "codex(lina)") {
		t.Fatalf("commands after /new = %#v, want prior Session run removed", after)
	}
}
