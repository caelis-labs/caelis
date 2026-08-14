package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/control/client/wirev1"
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

func (c *Client) DeliverAgentMessage(ctx context.Context, req controlclient.AgentMessageRequest) (controlclient.AgentMessageResult, error) {
	return doFocusedRequiredSessionJSON[controlclient.AgentMessageResult](ctx, c, req.SessionID, "/agent-messages", req)
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

func (c *Client) ConfigureSessionMode(ctx context.Context, req controlclient.SessionModeRequest) (controlclient.CommandResult, error) {
	path, err := focusedSessionPath(req.SessionID, "/configuration/session-mode")
	if err != nil {
		return controlclient.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, path, req.WriteBase, req)
}
func (c *Client) UseSessionModel(ctx context.Context, req controlclient.SessionModelRequest) (controlclient.CommandResult, error) {
	path, err := focusedSessionPath(req.SessionID, "/configuration/model")
	if err != nil {
		return controlclient.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, path, req.WriteBase, req)
}
func (c *Client) ConfigureSessionControllerMode(ctx context.Context, req controlclient.SessionControllerModeRequest) (controlclient.CommandResult, error) {
	path, err := focusedSessionPath(req.SessionID, "/configuration/controller-mode")
	if err != nil {
		return controlclient.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, path, req.WriteBase, req)
}
func (c *Client) ConfigureSessionPresentationMode(ctx context.Context, req controlclient.SessionPresentationModeRequest) (controlclient.CommandResult, error) {
	path, err := focusedSessionPath(req.SessionID, "/configuration/presentation-mode")
	if err != nil {
		return controlclient.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, path, req.WriteBase, req)
}
func (c *Client) ConfigureSessionPresentation(ctx context.Context, req controlclient.SessionPresentationConfigRequest) (controlclient.CommandResult, error) {
	path, err := focusedSessionPath(req.SessionID, "/configuration/presentation-config")
	if err != nil {
		return controlclient.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, path, req.WriteBase, req)
}
func (c *Client) ConnectModel(ctx context.Context, req controlclient.ConnectModelRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/configuration/connect-model", req.WriteBase, req)
}
func (c *Client) UseModel(ctx context.Context, req controlclient.UseModelRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/configuration/use-model", req.WriteBase, req)
}
func (c *Client) DeleteModel(ctx context.Context, req controlclient.DeleteModelRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/configuration/delete-model", req.WriteBase, req)
}
func (c *Client) SetSandboxBackend(ctx context.Context, req controlclient.SandboxRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/configuration/sandbox-backend", req.WriteBase, req)
}
func (c *Client) PrepareSandbox(ctx context.Context, req controlclient.SandboxRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/configuration/sandbox-prepare", req.WriteBase, req)
}
func (c *Client) RepairSandbox(ctx context.Context, req controlclient.SandboxRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/configuration/sandbox-repair", req.WriteBase, req)
}
func (c *Client) ResetSandbox(ctx context.Context, req controlclient.SandboxRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/configuration/sandbox-reset", req.WriteBase, req)
}
func (c *Client) RefreshSandbox(ctx context.Context, req controlclient.SandboxRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/configuration/sandbox-refresh", req.WriteBase, req)
}

func (c *Client) ListAgents(ctx context.Context, req controlclient.AgentRequest) ([]controlclient.AgentCandidate, error) {
	return doFocusedSessionJSON[[]controlclient.AgentCandidate](ctx, c, req.SessionID, "/agents/list", req)
}
func (c *Client) AgentStatus(ctx context.Context, req controlclient.AgentRequest) (controlclient.AgentStatusSnapshot, error) {
	return doFocusedSessionJSON[controlclient.AgentStatusSnapshot](ctx, c, req.SessionID, "/agents/status", req)
}
func (c *Client) HandoffAgent(ctx context.Context, req controlclient.HandoffAgentRequest) (controlclient.CommandResult, error) {
	path, err := focusedSessionPath(req.SessionID, "/agents/handoff")
	if err != nil {
		return controlclient.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, path, req.WriteBase, req)
}
func (c *Client) PrepareACP(ctx context.Context, req controlclient.PrepareACPRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/prepare-acp", req.WriteBase, req)
}
func (c *Client) PrepareACPAuthentication(ctx context.Context, req controlclient.PrepareACPAuthenticationRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/prepare-acp-auth", req.WriteBase, req)
}
func (c *Client) ACPPreparation(ctx context.Context, req controlclient.ACPPreparationRequest) (controlagents.ACPPreparation, error) {
	ref := strings.TrimSpace(req.Ref)
	if ref == "" {
		return controlagents.ACPPreparation{}, errors.New("control http client: ACP preparation ref is required")
	}
	return doFocusedJSON[controlagents.ACPPreparation](ctx, c, http.MethodGet, "/agents/acp-preparations/"+url.PathEscape(ref), nil)
}
func (c *Client) ConnectACP(ctx context.Context, req controlclient.ConnectACPRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/connect-acp", req.WriteBase, req)
}
func (c *Client) DisconnectCandidates(ctx context.Context, req controlclient.AgentRequest) (controlclient.DisconnectCandidatesSnapshot, error) {
	if strings.TrimSpace(req.SessionID) != "" {
		return controlclient.DisconnectCandidatesSnapshot{}, errors.New("control http client: ACP disconnect candidates are Host-scoped")
	}
	return doFocusedJSON[controlclient.DisconnectCandidatesSnapshot](ctx, c, http.MethodPost, "/agents/disconnect-candidates", req)
}
func (c *Client) DisconnectACP(ctx context.Context, req controlclient.DisconnectACPRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/disconnect-acp", req.WriteBase, req)
}
func (c *Client) AgentBindingStatus(ctx context.Context, req controlclient.AgentRequest) (agentbinding.Status, error) {
	if strings.TrimSpace(req.SessionID) != "" {
		return agentbinding.Status{}, errors.New("control http client: Agent binding status is Host-scoped")
	}
	return doFocusedJSON[agentbinding.Status](ctx, c, http.MethodPost, "/agents/binding-status", req)
}
func (c *Client) BindAgentBinding(ctx context.Context, req controlclient.BindAgentBindingRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/bind", req.WriteBase, req)
}
func (c *Client) ResetAgentBinding(ctx context.Context, req controlclient.ResetAgentBindingRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/reset-binding", req.WriteBase, req)
}
func (c *Client) CreateAgentRole(ctx context.Context, req controlclient.CreateAgentRoleRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/create-role", req.WriteBase, req)
}
func (c *Client) DeleteAgentRole(ctx context.Context, req controlclient.DeleteAgentRoleRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/delete-role", req.WriteBase, req)
}
func (c *Client) SaveAgentBindingSet(ctx context.Context, req controlclient.AgentBindingSetRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/save-binding-set", req.WriteBase, req)
}
func (c *Client) ApplyAgentBindingSet(ctx context.Context, req controlclient.AgentBindingSetRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/apply-binding-set", req.WriteBase, req)
}
func (c *Client) DeleteAgentBindingSet(ctx context.Context, req controlclient.AgentBindingSetRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/delete-binding-set", req.WriteBase, req)
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
func (c *Client) AddMarketplace(ctx context.Context, req controlclient.AddMarketplaceRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/plugins/add-marketplace", req.WriteBase, req)
}
func (c *Client) ListMarketplaces(ctx context.Context, req controlclient.PluginRequest) ([]controlclient.MarketplaceSnapshot, error) {
	return doFocusedSessionJSON[[]controlclient.MarketplaceSnapshot](ctx, c, req.SessionID, "/plugins/list-marketplaces", req)
}
func (c *Client) UpdateMarketplace(ctx context.Context, req controlclient.UpdateMarketplaceRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/plugins/update-marketplace", req.WriteBase, req)
}
func (c *Client) RemoveMarketplace(ctx context.Context, req controlclient.RemoveMarketplaceRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/plugins/remove-marketplace", req.WriteBase, req)
}
func (c *Client) AddPluginPath(ctx context.Context, req controlclient.AddPluginPathRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/plugins/add-path", req.WriteBase, req)
}
func (c *Client) InstallPlugin(ctx context.Context, req controlclient.InstallPluginRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/plugins/install", req.WriteBase, req)
}
func (c *Client) EnablePlugin(ctx context.Context, req controlclient.EnablePluginRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/plugins/enable", req.WriteBase, req)
}
func (c *Client) DisablePlugin(ctx context.Context, req controlclient.DisablePluginRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/plugins/disable", req.WriteBase, req)
}
func (c *Client) RemovePlugin(ctx context.Context, req controlclient.RemovePluginRequest) (controlclient.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/plugins/remove", req.WriteBase, req)
}
func (c *Client) InspectPlugin(ctx context.Context, req controlclient.PluginRequest) (controlclient.PluginSnapshot, error) {
	return doFocusedSessionJSON[controlclient.PluginSnapshot](ctx, c, req.SessionID, "/plugins/inspect", req)
}

func doFocusedSessionJSON[T any](ctx context.Context, client *Client, sessionID, suffix string, body any) (T, error) {
	path, err := focusedOptionalSessionPath(sessionID, suffix)
	if err != nil {
		var zero T
		return zero, err
	}
	return doFocusedJSON[T](ctx, client, http.MethodPost, path, body)
}

func doFocusedRequiredSessionJSON[T any](ctx context.Context, client *Client, sessionID, suffix string, body any) (T, error) {
	path, err := focusedSessionPath(sessionID, suffix)
	if err != nil {
		var zero T
		return zero, err
	}
	return doFocusedJSON[T](ctx, client, http.MethodPost, path, body)
}

func focusedOptionalSessionPath(sessionID, suffix string) (string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return suffix, nil
	}
	return focusedSessionPath(sessionID, suffix)
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
var _ controlclient.AgentMessageClient = (*Client)(nil)
var _ controlclient.ConfigurationClient = (*Client)(nil)
var _ controlclient.AgentClient = (*Client)(nil)
var _ controlclient.CompletionClient = (*Client)(nil)
var _ controlclient.PluginClient = (*Client)(nil)
var _ controlclient.PresentationClient = (*Client)(nil)
var _ controlclient.TerminalClient = (*Client)(nil)
