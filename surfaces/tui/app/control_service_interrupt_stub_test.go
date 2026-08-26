package tuiapp

import (
	"context"
	"sync"

	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

// interruptBridgeStub is the smallest ControlServices implementation that can
// drive ConfigFromControlService plus one live Turn feed.
type interruptBridgeStub struct {
	turn controlprompt.Turn

	mu             sync.Mutex
	submitted      chan struct{}
	interruptCalls int
	interruptErr   error
	waitForCancel  bool
}

func (s *interruptBridgeStub) markSubmitted() {
	s.mu.Lock()
	submitted := s.submitted
	s.mu.Unlock()
	if submitted == nil {
		return
	}
	select {
	case submitted <- struct{}{}:
	default:
	}
}

func (s *interruptBridgeStub) Status(context.Context) (controlstatus.StatusSnapshot, error) {
	return controlstatus.StatusSnapshot{}, nil
}
func (*interruptBridgeStub) WorkspaceDir() string         { return "" }
func (*interruptBridgeStub) CanSubmitRunningPrompt() bool { return true }
func (s *interruptBridgeStub) Submit(ctx context.Context, _ controlprompt.Submission) (controlprompt.Turn, error) {
	s.markSubmitted()
	s.mu.Lock()
	waitForCancel := s.waitForCancel
	s.mu.Unlock()
	if waitForCancel {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.turn, nil
}
func (s *interruptBridgeStub) Interrupt(context.Context) error {
	s.mu.Lock()
	s.interruptCalls++
	err := s.interruptErr
	s.mu.Unlock()
	return err
}
func (*interruptBridgeStub) ResetSession(context.Context) error { return nil }
func (*interruptBridgeStub) ResumeSession(context.Context, string) (controlprompt.SessionSnapshot, error) {
	return controlprompt.SessionSnapshot{}, nil
}
func (*interruptBridgeStub) ListSessions(context.Context, int) ([]controlprompt.ResumeCandidate, error) {
	return nil, nil
}
func (*interruptBridgeStub) Compact(context.Context) (bool, error) { return true, nil }
func (*interruptBridgeStub) CycleSessionMode(context.Context) (controlstatus.StatusSnapshot, error) {
	return controlstatus.StatusSnapshot{}, nil
}
func (*interruptBridgeStub) SetSessionMode(context.Context, string) (controlstatus.StatusSnapshot, error) {
	return controlstatus.StatusSnapshot{}, nil
}
func (*interruptBridgeStub) Connect(context.Context, controlprompt.ConnectConfig) (controlstatus.StatusSnapshot, error) {
	return controlstatus.StatusSnapshot{}, nil
}
func (*interruptBridgeStub) UseModel(context.Context, string, ...string) (controlstatus.StatusSnapshot, error) {
	return controlstatus.StatusSnapshot{}, nil
}
func (*interruptBridgeStub) DeleteModel(context.Context, string) error { return nil }
func (*interruptBridgeStub) SetSandboxBackend(context.Context, string) (controlstatus.StatusSnapshot, error) {
	return controlstatus.StatusSnapshot{}, nil
}
func (*interruptBridgeStub) PrepareSandbox(context.Context) (controlstatus.StatusSnapshot, error) {
	return controlstatus.StatusSnapshot{}, nil
}
func (*interruptBridgeStub) RepairSandbox(context.Context) (controlstatus.StatusSnapshot, error) {
	return controlstatus.StatusSnapshot{}, nil
}
func (*interruptBridgeStub) ListAgents(context.Context, int) ([]controlprompt.AgentCandidate, error) {
	return nil, nil
}
func (*interruptBridgeStub) AgentStatus(context.Context) (controlprompt.AgentStatusSnapshot, error) {
	return controlprompt.AgentStatusSnapshot{}, nil
}
func (*interruptBridgeStub) StartAgentRun(context.Context, string, string, []controlprompt.Attachment) (controlprompt.Turn, error) {
	return nil, nil
}
func (*interruptBridgeStub) ContinueAgentRun(context.Context, string, string, []controlprompt.Attachment) (controlprompt.Turn, error) {
	return nil, nil
}
func (*interruptBridgeStub) StartReview(context.Context, string, []controlprompt.Attachment) (controlprompt.Turn, error) {
	return nil, nil
}
func (*interruptBridgeStub) CompleteFile(context.Context, string, int) ([]controlprompt.CompletionCandidate, error) {
	return nil, nil
}
func (*interruptBridgeStub) CompleteSkill(context.Context, string, int) ([]controlprompt.CompletionCandidate, error) {
	return nil, nil
}
func (*interruptBridgeStub) CompleteResume(context.Context, string, int) ([]controlprompt.ResumeCandidate, error) {
	return nil, nil
}
func (*interruptBridgeStub) CompleteSlashArg(context.Context, string, string, int) ([]controlprompt.SlashArgCandidate, error) {
	return nil, nil
}
func (*interruptBridgeStub) ListPlugins(context.Context) ([]controlprompt.PluginSnapshot, error) {
	return nil, nil
}
func (*interruptBridgeStub) AddMarketplace(context.Context, string) (controlprompt.MarketplaceSnapshot, error) {
	return controlprompt.MarketplaceSnapshot{}, nil
}
func (*interruptBridgeStub) ListMarketplaces(context.Context) ([]controlprompt.MarketplaceSnapshot, error) {
	return nil, nil
}
func (*interruptBridgeStub) UpdateMarketplace(context.Context, string) (controlprompt.MarketplaceSnapshot, error) {
	return controlprompt.MarketplaceSnapshot{}, nil
}
func (*interruptBridgeStub) RemoveMarketplace(context.Context, string) error { return nil }
func (*interruptBridgeStub) AddPluginPath(context.Context, string) (controlprompt.PluginSnapshot, error) {
	return controlprompt.PluginSnapshot{}, nil
}
func (*interruptBridgeStub) InstallPlugin(context.Context, string) (controlprompt.PluginSnapshot, error) {
	return controlprompt.PluginSnapshot{}, nil
}
func (*interruptBridgeStub) EnablePlugin(context.Context, string) (controlprompt.PluginSnapshot, error) {
	return controlprompt.PluginSnapshot{}, nil
}
func (*interruptBridgeStub) DisablePlugin(context.Context, string) (controlprompt.PluginSnapshot, error) {
	return controlprompt.PluginSnapshot{}, nil
}
func (*interruptBridgeStub) RemovePlugin(context.Context, string) error { return nil }
func (*interruptBridgeStub) InspectPlugin(context.Context, string) (controlprompt.PluginSnapshot, error) {
	return controlprompt.PluginSnapshot{}, nil
}
func (*interruptBridgeStub) AgentBindingStatus(context.Context) (agentbinding.Status, error) {
	return agentbinding.Status{}, nil
}
func (*interruptBridgeStub) BindAgentBinding(context.Context, agentbinding.Binding) (agentbinding.Status, error) {
	return agentbinding.Status{}, nil
}
func (*interruptBridgeStub) ResetAgentBinding(context.Context, agentbinding.Handle) (agentbinding.Status, error) {
	return agentbinding.Status{}, nil
}
func (*interruptBridgeStub) DiscoverACPConnection(context.Context, controlagents.ConnectRequest) (controlagents.DiscoverySnapshot, error) {
	return controlagents.DiscoverySnapshot{}, nil
}
func (*interruptBridgeStub) ConnectACP(context.Context, controlagents.ConnectRequest) (controlagents.ConnectResult, error) {
	return controlagents.ConnectResult{}, nil
}
func (*interruptBridgeStub) DisconnectCandidates(context.Context) ([]controlagents.DisconnectCandidate, error) {
	return nil, nil
}
func (*interruptBridgeStub) DisconnectACP(context.Context, string) (controlagents.DisconnectResult, error) {
	return controlagents.DisconnectResult{}, nil
}

var _ ControlServices = (*interruptBridgeStub)(nil)
