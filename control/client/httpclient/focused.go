package httpclient

import (
	"context"
	"fmt"
	"net/http"

	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/control/client/wirev1"
	controlstatus "github.com/caelis-labs/caelis/control/status"
)

func (c *Client) PresentationCapabilities(ctx context.Context) (controlclient.PresentationCapabilities, error) {
	return doFocusedJSON[controlclient.PresentationCapabilities](ctx, c, http.MethodGet, "/presentation/capabilities", nil)
}

func (c *Client) PresentationSnapshot(ctx context.Context, req controlclient.PresentationRequest) (controlclient.PresentationSnapshot, error) {
	path, err := focusedSessionPath(req.SessionID, "/presentation")
	if err != nil {
		return controlclient.PresentationSnapshot{}, err
	}
	return doFocusedJSON[controlclient.PresentationSnapshot](ctx, c, http.MethodGet, path, nil)
}

func (c *Client) SetPresentationMode(ctx context.Context, req controlclient.SetPresentationModeRequest) error {
	_, err := doFocusedSessionJSON[struct{}](ctx, c, req.SessionID, "/presentation/mode", req)
	return err
}

func (c *Client) SetPresentationConfig(ctx context.Context, req controlclient.SetPresentationConfigRequest) ([]controlclient.PresentationConfigOption, error) {
	return doFocusedSessionJSON[[]controlclient.PresentationConfigOption](ctx, c, req.SessionID, "/presentation/config", req)
}

func (c *Client) SetPresentationModel(ctx context.Context, req controlclient.SetPresentationModelRequest) error {
	_, err := doFocusedSessionJSON[struct{}](ctx, c, req.SessionID, "/presentation/model", req)
	return err
}

func (c *Client) TerminalOutput(ctx context.Context, req controlclient.TerminalRequest) (controlclient.TerminalOutput, error) {
	return doFocusedSessionJSON[controlclient.TerminalOutput](ctx, c, req.SessionID, "/terminals/output", req)
}

func (c *Client) WaitTerminal(ctx context.Context, req controlclient.TerminalRequest) (controlclient.TerminalExitStatus, error) {
	return doFocusedSessionJSON[controlclient.TerminalExitStatus](ctx, c, req.SessionID, "/terminals/wait", req)
}

func (c *Client) KillTerminal(ctx context.Context, req controlclient.TerminalRequest) error {
	_, err := doFocusedSessionJSON[struct{}](ctx, c, req.SessionID, "/terminals/kill", req)
	return err
}

func (c *Client) ReleaseTerminal(ctx context.Context, req controlclient.TerminalRequest) error {
	_, err := doFocusedSessionJSON[struct{}](ctx, c, req.SessionID, "/terminals/release", req)
	return err
}

func (c *Client) Handles(ctx context.Context, sessionID string) ([]string, error) {
	path, err := focusedSessionPath(sessionID, "/participants/handles")
	if err != nil {
		return nil, err
	}
	return doFocusedJSON[[]string](ctx, c, http.MethodGet, path, nil)
}

func (c *Client) StartParticipant(ctx context.Context, req controlclient.StartParticipantRequest) (controlclient.CommandResult, error) {
	path, err := focusedSessionPath(req.SessionID, "/participants/start")
	if err != nil {
		return controlclient.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, path, req.WriteBase, req)
}

func (c *Client) PromptParticipant(ctx context.Context, req controlclient.PromptParticipantRequest) (controlclient.CommandResult, error) {
	path, err := focusedSessionPath(req.SessionID, "/participants/prompt")
	if err != nil {
		return controlclient.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, path, req.WriteBase, req)
}

func (c *Client) CancelParticipant(ctx context.Context, req controlclient.CancelParticipantRequest) (controlclient.CommandResult, error) {
	path, err := focusedSessionPath(req.SessionID, "/participants/cancel")
	if err != nil {
		return controlclient.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, path, req.WriteBase, req)
}

func (c *Client) ConfigureSessionMode(ctx context.Context, req controlclient.SessionModeRequest) (controlstatus.StatusSnapshot, error) {
	return doFocusedSessionJSON[controlstatus.StatusSnapshot](ctx, c, req.SessionID, "/configuration/session-mode", req)
}
func (c *Client) ConnectModel(ctx context.Context, req controlclient.ConnectModelRequest) (controlstatus.StatusSnapshot, error) {
	return doFocusedSessionJSON[controlstatus.StatusSnapshot](ctx, c, req.SessionID, "/configuration/connect-model", req)
}
func (c *Client) UseModel(ctx context.Context, req controlclient.UseModelRequest) (controlstatus.StatusSnapshot, error) {
	return doFocusedSessionJSON[controlstatus.StatusSnapshot](ctx, c, req.SessionID, "/configuration/use-model", req)
}
func (c *Client) DeleteModel(ctx context.Context, req controlclient.DeleteModelRequest) error {
	_, err := doFocusedSessionJSON[struct{}](ctx, c, req.SessionID, "/configuration/delete-model", req)
	return err
}
func (c *Client) SetSandboxBackend(ctx context.Context, req controlclient.SandboxRequest) (controlstatus.StatusSnapshot, error) {
	return doFocusedSessionJSON[controlstatus.StatusSnapshot](ctx, c, req.SessionID, "/configuration/sandbox-backend", req)
}
func (c *Client) PrepareSandbox(ctx context.Context, req controlclient.SandboxRequest) (controlstatus.StatusSnapshot, error) {
	return doFocusedSessionJSON[controlstatus.StatusSnapshot](ctx, c, req.SessionID, "/configuration/sandbox-prepare", req)
}
func (c *Client) RepairSandbox(ctx context.Context, req controlclient.SandboxRequest) (controlstatus.StatusSnapshot, error) {
	return doFocusedSessionJSON[controlstatus.StatusSnapshot](ctx, c, req.SessionID, "/configuration/sandbox-repair", req)
}
func (c *Client) RefreshSandbox(ctx context.Context, req controlclient.SandboxRequest) error {
	_, err := doFocusedSessionJSON[struct{}](ctx, c, req.SessionID, "/configuration/sandbox-refresh", req)
	return err
}

func (c *Client) ListAgents(ctx context.Context, req controlclient.AgentRequest) ([]controlclient.AgentCandidate, error) {
	return doFocusedSessionJSON[[]controlclient.AgentCandidate](ctx, c, req.SessionID, "/agents/list", req)
}
func (c *Client) AgentStatus(ctx context.Context, req controlclient.AgentRequest) (controlclient.AgentStatusSnapshot, error) {
	return doFocusedSessionJSON[controlclient.AgentStatusSnapshot](ctx, c, req.SessionID, "/agents/status", req)
}
func (c *Client) HandoffAgent(ctx context.Context, req controlclient.HandoffAgentRequest) (controlclient.AgentStatusSnapshot, error) {
	return doFocusedSessionJSON[controlclient.AgentStatusSnapshot](ctx, c, req.SessionID, "/agents/handoff", req)
}
func (c *Client) DiscoverACPConnection(ctx context.Context, req controlclient.ConnectACPRequest) (controlagents.DiscoverySnapshot, error) {
	return doFocusedSessionJSON[controlagents.DiscoverySnapshot](ctx, c, req.SessionID, "/agents/discover-acp", req)
}
func (c *Client) ConnectACP(ctx context.Context, req controlclient.ConnectACPRequest) (controlagents.ConnectResult, error) {
	return doFocusedSessionJSON[controlagents.ConnectResult](ctx, c, req.SessionID, "/agents/connect-acp", req)
}
func (c *Client) DisconnectCandidates(ctx context.Context, req controlclient.DisconnectACPRequest) ([]controlagents.DisconnectCandidate, error) {
	return doFocusedSessionJSON[[]controlagents.DisconnectCandidate](ctx, c, req.SessionID, "/agents/disconnect-candidates", req)
}
func (c *Client) DisconnectACP(ctx context.Context, req controlclient.DisconnectACPRequest) (controlagents.DisconnectResult, error) {
	return doFocusedSessionJSON[controlagents.DisconnectResult](ctx, c, req.SessionID, "/agents/disconnect-acp", req)
}
func (c *Client) AgentBindingStatus(ctx context.Context, req controlclient.AgentRequest) (agentbinding.Status, error) {
	return doFocusedSessionJSON[agentbinding.Status](ctx, c, req.SessionID, "/agents/binding-status", req)
}
func (c *Client) BindAgentBinding(ctx context.Context, req controlclient.AgentBindingRequest) (agentbinding.Status, error) {
	return doFocusedSessionJSON[agentbinding.Status](ctx, c, req.SessionID, "/agents/bind", req)
}
func (c *Client) ResetAgentBinding(ctx context.Context, req controlclient.AgentBindingRequest) (agentbinding.Status, error) {
	return doFocusedSessionJSON[agentbinding.Status](ctx, c, req.SessionID, "/agents/reset-binding", req)
}
func (c *Client) CreateAgentRole(ctx context.Context, req controlclient.AgentBindingRequest) (agentbinding.Status, error) {
	return doFocusedSessionJSON[agentbinding.Status](ctx, c, req.SessionID, "/agents/create-role", req)
}
func (c *Client) DeleteAgentRole(ctx context.Context, req controlclient.AgentBindingRequest) (agentbinding.Status, error) {
	return doFocusedSessionJSON[agentbinding.Status](ctx, c, req.SessionID, "/agents/delete-role", req)
}
func (c *Client) SaveAgentBindingSet(ctx context.Context, req controlclient.AgentBindingRequest) (agentbinding.Status, error) {
	return doFocusedSessionJSON[agentbinding.Status](ctx, c, req.SessionID, "/agents/save-binding-set", req)
}
func (c *Client) ApplyAgentBindingSet(ctx context.Context, req controlclient.AgentBindingRequest) (agentbinding.Status, error) {
	return doFocusedSessionJSON[agentbinding.Status](ctx, c, req.SessionID, "/agents/apply-binding-set", req)
}
func (c *Client) DeleteAgentBindingSet(ctx context.Context, req controlclient.AgentBindingRequest) (agentbinding.Status, error) {
	return doFocusedSessionJSON[agentbinding.Status](ctx, c, req.SessionID, "/agents/delete-binding-set", req)
}

func (c *Client) CompleteFile(ctx context.Context, req controlclient.CompletionRequest) ([]controlclient.CompletionCandidate, error) {
	return doFocusedSessionJSON[[]controlclient.CompletionCandidate](ctx, c, req.SessionID, "/completion/files", req)
}
func (c *Client) CompleteSkill(ctx context.Context, req controlclient.CompletionRequest) ([]controlclient.CompletionCandidate, error) {
	return doFocusedSessionJSON[[]controlclient.CompletionCandidate](ctx, c, req.SessionID, "/completion/skills", req)
}
func (c *Client) CompleteResume(ctx context.Context, req controlclient.CompletionRequest) ([]controlclient.ResumeCandidate, error) {
	return doFocusedSessionJSON[[]controlclient.ResumeCandidate](ctx, c, req.SessionID, "/completion/sessions", req)
}
func (c *Client) CompleteSlashArg(ctx context.Context, req controlclient.CompletionRequest) ([]controlclient.SlashArgCandidate, error) {
	return doFocusedSessionJSON[[]controlclient.SlashArgCandidate](ctx, c, req.SessionID, "/completion/slash-arguments", req)
}
func (c *Client) ResolveSkill(ctx context.Context, req controlclient.CompletionRequest) (controlclient.SkillResolveResult, error) {
	return doFocusedSessionJSON[controlclient.SkillResolveResult](ctx, c, req.SessionID, "/completion/resolve-skill", req)
}

func (c *Client) ListPlugins(ctx context.Context, req controlclient.PluginRequest) ([]controlclient.PluginSnapshot, error) {
	return doFocusedSessionJSON[[]controlclient.PluginSnapshot](ctx, c, req.SessionID, "/plugins/list", req)
}
func (c *Client) AddMarketplace(ctx context.Context, req controlclient.PluginRequest) (controlclient.MarketplaceSnapshot, error) {
	return doFocusedSessionJSON[controlclient.MarketplaceSnapshot](ctx, c, req.SessionID, "/plugins/add-marketplace", req)
}
func (c *Client) ListMarketplaces(ctx context.Context, req controlclient.PluginRequest) ([]controlclient.MarketplaceSnapshot, error) {
	return doFocusedSessionJSON[[]controlclient.MarketplaceSnapshot](ctx, c, req.SessionID, "/plugins/list-marketplaces", req)
}
func (c *Client) UpdateMarketplace(ctx context.Context, req controlclient.PluginRequest) (controlclient.MarketplaceSnapshot, error) {
	return doFocusedSessionJSON[controlclient.MarketplaceSnapshot](ctx, c, req.SessionID, "/plugins/update-marketplace", req)
}
func (c *Client) RemoveMarketplace(ctx context.Context, req controlclient.PluginRequest) error {
	_, err := doFocusedSessionJSON[struct{}](ctx, c, req.SessionID, "/plugins/remove-marketplace", req)
	return err
}
func (c *Client) AddPluginPath(ctx context.Context, req controlclient.PluginRequest) (controlclient.PluginSnapshot, error) {
	return doFocusedSessionJSON[controlclient.PluginSnapshot](ctx, c, req.SessionID, "/plugins/add-path", req)
}
func (c *Client) InstallPlugin(ctx context.Context, req controlclient.PluginRequest) (controlclient.PluginSnapshot, error) {
	return doFocusedSessionJSON[controlclient.PluginSnapshot](ctx, c, req.SessionID, "/plugins/install", req)
}
func (c *Client) EnablePlugin(ctx context.Context, req controlclient.PluginRequest) (controlclient.PluginSnapshot, error) {
	return doFocusedSessionJSON[controlclient.PluginSnapshot](ctx, c, req.SessionID, "/plugins/enable", req)
}
func (c *Client) DisablePlugin(ctx context.Context, req controlclient.PluginRequest) (controlclient.PluginSnapshot, error) {
	return doFocusedSessionJSON[controlclient.PluginSnapshot](ctx, c, req.SessionID, "/plugins/disable", req)
}
func (c *Client) RemovePlugin(ctx context.Context, req controlclient.PluginRequest) error {
	_, err := doFocusedSessionJSON[struct{}](ctx, c, req.SessionID, "/plugins/remove", req)
	return err
}
func (c *Client) InspectPlugin(ctx context.Context, req controlclient.PluginRequest) (controlclient.PluginSnapshot, error) {
	return doFocusedSessionJSON[controlclient.PluginSnapshot](ctx, c, req.SessionID, "/plugins/inspect", req)
}

func doFocusedSessionJSON[T any](ctx context.Context, client *Client, sessionID, suffix string, body any) (T, error) {
	path, err := focusedSessionPath(sessionID, suffix)
	if err != nil {
		var zero T
		return zero, err
	}
	return doFocusedJSON[T](ctx, client, http.MethodPost, path, body)
}

func focusedSessionPath(sessionID, suffix string) (string, error) {
	id, err := remotePathID("session", sessionID)
	if err != nil {
		return "", err
	}
	return "/sessions/" + id + suffix, nil
}

func doFocusedJSON[T any](ctx context.Context, client *Client, method, path string, body any) (T, error) {
	var result T
	response, err := client.do(ctx, method, path, nil, body, nil)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	raw, err := readRemoteResponse(response)
	if err != nil {
		return result, err
	}
	if err := wirev1.Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("control http client: decode focused response: %w", err)
	}
	return result, nil
}

var _ controlclient.ParticipantClient = (*Client)(nil)
var _ controlclient.ConfigurationClient = (*Client)(nil)
var _ controlclient.AgentClient = (*Client)(nil)
var _ controlclient.CompletionClient = (*Client)(nil)
var _ controlclient.PluginClient = (*Client)(nil)
var _ controlclient.PresentationClient = (*Client)(nil)
var _ controlclient.TerminalClient = (*Client)(nil)
