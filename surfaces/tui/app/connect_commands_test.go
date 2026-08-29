package tuiapp

import (
	"context"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

type acpConnectControlStub struct {
	ControlServices
	req          controlagents.ConnectRequest
	disconnected string
}

type modelConnectControlStub struct {
	ControlServices
	agents        []controlprompt.AgentCandidate
	status        controlprompt.AgentStatusSnapshot
	bindingStatus agentbinding.Status
	connected     controlprompt.ConnectConfig
	deleted       string
}

func (s *modelConnectControlStub) Connect(_ context.Context, config controlprompt.ConnectConfig) (controlstatus.StatusSnapshot, error) {
	s.connected = config
	return controlstatus.StatusSnapshot{ModelStatus: controlstatus.StatusModel{Display: "openai/gpt-5.6"}}, nil
}

func (s *modelConnectControlStub) DeleteModel(_ context.Context, model string) error {
	s.deleted = model
	return nil
}

func (*modelConnectControlStub) Status(context.Context) (controlstatus.StatusSnapshot, error) {
	return controlstatus.StatusSnapshot{}, nil
}

func (s *modelConnectControlStub) AgentStatus(context.Context) (controlprompt.AgentStatusSnapshot, error) {
	return s.status, nil
}

func (s *modelConnectControlStub) ListAgents(context.Context, int) ([]controlprompt.AgentCandidate, error) {
	if len(s.agents) > 0 {
		return slices.Clone(s.agents), nil
	}
	return []controlprompt.AgentCandidate{{Name: "sol", Description: "GPT 5.6 Sol"}}, nil
}

func (s *modelConnectControlStub) AgentBindingStatus(context.Context) (agentbinding.Status, error) {
	return s.bindingStatus, nil
}

func (s *modelConnectControlStub) BindAgentBinding(_ context.Context, _ agentbinding.Binding) (agentbinding.Status, error) {
	return s.bindingStatus, nil
}

func (s *modelConnectControlStub) ResetAgentBinding(_ context.Context, _ agentbinding.Handle) (agentbinding.Status, error) {
	return s.bindingStatus, nil
}

func (*modelConnectControlStub) DiscoverACPConnection(context.Context, controlagents.ConnectRequest) (controlagents.DiscoverySnapshot, error) {
	return controlagents.DiscoverySnapshot{}, nil
}

func (*modelConnectControlStub) ConnectACP(context.Context, controlagents.ConnectRequest) (controlagents.ConnectResult, error) {
	return controlagents.ConnectResult{}, nil
}

func (*modelConnectControlStub) DisconnectCandidates(context.Context) ([]controlagents.DisconnectCandidate, error) {
	return nil, nil
}

func (*modelConnectControlStub) DisconnectACP(context.Context, string) (controlagents.DisconnectResult, error) {
	return controlagents.DisconnectResult{}, nil
}

func (s *acpConnectControlStub) DiscoverACPConnection(_ context.Context, _ controlagents.ConnectRequest) (controlagents.DiscoverySnapshot, error) {
	return controlagents.DiscoverySnapshot{}, nil
}

func (s *acpConnectControlStub) ConnectACP(_ context.Context, req controlagents.ConnectRequest) (controlagents.ConnectResult, error) {
	s.req = req
	return controlagents.ConnectResult{Profiles: []controlagents.ConnectedProfile{{ID: "acp:claude:opus"}}}, nil
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
		"acp_agent": "custom", "acp_launcher": "command", "acp_command": "claude-agent-acp", "acp_model": "opus",
	})
	result := slashConnectWithContext(context.Background(), service, service, nil, "acp "+payload)
	if result.Err != nil {
		t.Fatalf("slashConnectWithContext() error = %v", result.Err)
	}
	if !result.SuppressTurnDivider {
		t.Fatalf("slashConnectWithContext() = %#v, want local connect result", result)
	}
	if service.req.AdapterID != "custom" || service.req.Launcher != controlagents.LauncherChoiceCommand || service.req.CommandLine != "claude-agent-acp" {
		t.Fatalf("ConnectACP request = %#v", service.req)
	}
	if service.req.ModelID != "opus" {
		t.Fatalf("ConnectACP model ID = %q", service.req.ModelID)
	}
	if len(service.req.ConfigValues) != 0 {
		t.Fatalf("ConnectACP config values = %#v, want Agent defaults", service.req.ConfigValues)
	}
}

func TestConnectIsHandledAsLocalAppConfiguration(t *testing.T) {
	service := &modelConnectControlStub{}
	result, handled := executeTUIPrivateSlashCommandWithContext(
		context.Background(), service, nil, "connect", "codex gpt-5.6-sol",
	)
	if !handled {
		t.Fatal("executeTUIPrivateSlashCommandWithContext(connect) was not handled as local App configuration")
	}
	if result.completion.Err != nil {
		t.Fatalf("execute connect error = %v", result.completion.Err)
	}
	if service.connected.Provider != "codex" || service.connected.Model != "gpt-5.6-sol" {
		t.Fatalf("Connect() config = %#v, want local App model connection", service.connected)
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

func TestSlashDisconnectSeparatesProviderAndACP(t *testing.T) {
	provider := &modelConnectControlStub{}
	result := slashDisconnectWithContext(context.Background(), provider, provider, nil, "provider ollama/qwen3")
	if result.Err != nil || !result.SuppressTurnDivider || provider.deleted != "ollama/qwen3" {
		t.Fatalf("provider disconnect = %#v, deleted %q", result, provider.deleted)
	}

	acp := &acpConnectControlStub{}
	result = slashDisconnectWithContext(context.Background(), acp, acp, nil, "acp codex")
	if result.Err != nil || !result.SuppressTurnDivider || acp.disconnected != "codex" {
		t.Fatalf("ACP disconnect = %#v, disconnected %q", result, acp.disconnected)
	}
}

func TestSlashConnectModelKeepsUnboundProfilesHiddenWithoutExposingAgentSlash(t *testing.T) {
	service := &modelConnectControlStub{}
	var commands SetCommandsMsg
	var notice SlashNoticeMsg
	result := slashConnectWithContext(context.Background(), service, nil, func(msg tea.Msg) {
		switch update := msg.(type) {
		case SetCommandsMsg:
			commands = update
		case SlashNoticeMsg:
			notice = update
		}
	}, "codex gpt-5.6-sol")
	if result.Err != nil {
		t.Fatalf("slashConnectWithContext() error = %v", result.Err)
	}
	for _, profile := range []string{"breeze", "orbit", "zenith"} {
		if slices.Contains(commands.Commands, profile) {
			t.Fatalf("refreshed commands = %#v, should hide unbound %s", commands.Commands, profile)
		}
	}
	if slices.Contains(commands.Commands, "sol") {
		t.Fatalf("refreshed commands = %#v, should hide model Agent ID sol", commands.Commands)
	}
	if !strings.Contains(notice.Text, "Connected openai-codex/gpt-5.6-sol") {
		t.Fatalf("connect notice = %#v, want canonical connected model", notice)
	}
	if notice.Placement != SlashNoticeFeedback {
		t.Fatalf("connect notice placement = %v, want feedback under the command", notice.Placement)
	}
}

func TestAgentSlashCommandsHideRosterAndKeepProfileRunsSessionScoped(t *testing.T) {
	t.Parallel()

	service := &modelConnectControlStub{
		agents: []controlprompt.AgentCandidate{
			{Name: "codex", Description: "Codex ACP Agent"},
			{Name: "claude", Description: "Claude ACP Agent"},
		},
		status: controlprompt.AgentStatusSnapshot{Participants: []controlprompt.AgentParticipantSnapshot{{
			ID: "participant-1", Label: "@lina", AgentName: "codex", Kind: "acp", Role: "sidecar", Source: "slash_profile_breeze",
		}}},
		bindingStatus: subagentTestStatus(),
	}

	before := appendAgentSlashCommandsWithContext(context.Background(), service, DefaultCommands())
	for _, command := range []string{"orbit", "breeze(lina)"} {
		if !slices.Contains(before, command) {
			t.Fatalf("commands before /new = %#v, want %q", before, command)
		}
	}
	for _, hidden := range []string{"breeze", "zenith"} {
		if slices.Contains(before, hidden) {
			t.Fatalf("commands before /new = %#v, should hide unbound profile %q", before, hidden)
		}
	}
	for _, hidden := range []string{"codex", "claude", "codex(lina)"} {
		if slices.Contains(before, hidden) {
			t.Fatalf("commands before /new = %#v, should hide raw Agent %q", before, hidden)
		}
	}
	details := profileCommandDetailsWithContext(context.Background(), service)
	if details["breeze(lina)"] != "Continue /breeze as lina" {
		t.Fatalf("run command details = %#v", details)
	}

	service.status.Participants = nil
	after := appendAgentSlashCommandsWithContext(context.Background(), service, DefaultCommands())
	if !slices.Contains(after, "orbit") {
		t.Fatalf("commands after /new = %#v, want bound Orbit", after)
	}
	for _, hidden := range []string{"breeze", "zenith"} {
		if slices.Contains(after, hidden) {
			t.Fatalf("commands after /new = %#v, should hide unbound profile %q", after, hidden)
		}
	}
	if slices.Contains(after, "breeze(lina)") {
		t.Fatalf("commands after /new = %#v, want prior Session run removed", after)
	}
}

func TestACPControllerHidesCompactFromTUICommands(t *testing.T) {
	t.Parallel()

	service := &modelConnectControlStub{status: controlprompt.AgentStatusSnapshot{ControllerKind: "acp"}}
	commands := appendAgentSlashCommandsWithContext(context.Background(), service, DefaultCommands())
	if slices.Contains(commands, "compact") {
		t.Fatalf("commands = %#v, should hide /compact for external ACP controller", commands)
	}
	if !slices.Contains(commands, "status") || !slices.Contains(commands, "model") {
		t.Fatalf("commands = %#v, want status and model retained", commands)
	}
}
