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
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
)

func (c *Client) PresentationCapabilities(ctx context.Context) (appserver.PresentationCapabilities, error) {
	return doFocusedJSON[appserver.PresentationCapabilities](ctx, c, http.MethodGet, "/presentation/capabilities", nil)
}

func (c *Client) PresentationSnapshot(ctx context.Context, req appserver.PresentationRequest) (appserver.PresentationSnapshot, error) {
	path, err := focusedSessionPath(req.SessionID, "/presentation")
	if err != nil {
		return appserver.PresentationSnapshot{}, err
	}
	return doFocusedJSON[appserver.PresentationSnapshot](ctx, c, http.MethodGet, path, nil)
}

func (c *Client) TerminalOutput(ctx context.Context, req appserver.TerminalRequest) (appserver.TerminalOutput, error) {
	return doFocusedSessionJSON[appserver.TerminalOutput](ctx, c, req.SessionID, "/terminals/output", req)
}

func (c *Client) WaitTerminal(ctx context.Context, req appserver.TerminalRequest) (appserver.TerminalExitStatus, error) {
	return doFocusedSessionJSON[appserver.TerminalExitStatus](ctx, c, req.SessionID, "/terminals/wait", req)
}

func (c *Client) KillTerminal(ctx context.Context, req appserver.TerminalRequest) error {
	_, err := doFocusedSessionJSON[struct{}](ctx, c, req.SessionID, "/terminals/kill", req)
	return err
}

func (c *Client) ReleaseTerminal(ctx context.Context, req appserver.TerminalRequest) error {
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

func (c *Client) StartParticipant(ctx context.Context, req appserver.StartParticipantRequest) (appserver.CommandResult, error) {
	path, err := focusedSessionPath(req.SessionID, "/participants/start")
	if err != nil {
		return appserver.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, path, req.WriteBase, req)
}

func (c *Client) PromptParticipant(ctx context.Context, req appserver.PromptParticipantRequest) (appserver.CommandResult, error) {
	path, err := focusedSessionPath(req.SessionID, "/participants/prompt")
	if err != nil {
		return appserver.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, path, req.WriteBase, req)
}

func (c *Client) CancelParticipant(ctx context.Context, req appserver.CancelParticipantRequest) (appserver.CommandResult, error) {
	path, err := focusedSessionPath(req.SessionID, "/participants/cancel")
	if err != nil {
		return appserver.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, path, req.WriteBase, req)
}

func (c *Client) ConfigureSessionMode(ctx context.Context, req appserver.SessionModeRequest) (appserver.CommandResult, error) {
	path, err := focusedSessionPath(req.SessionID, "/configuration/session-mode")
	if err != nil {
		return appserver.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, path, req.WriteBase, req)
}
func (c *Client) UseSessionModel(ctx context.Context, req appserver.SessionModelRequest) (appserver.CommandResult, error) {
	path, err := focusedSessionPath(req.SessionID, "/configuration/model")
	if err != nil {
		return appserver.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, path, req.WriteBase, req)
}
func (c *Client) ConfigureSessionControllerMode(ctx context.Context, req appserver.SessionControllerModeRequest) (appserver.CommandResult, error) {
	path, err := focusedSessionPath(req.SessionID, "/configuration/controller-mode")
	if err != nil {
		return appserver.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, path, req.WriteBase, req)
}
func (c *Client) ConfigureSessionPresentationMode(ctx context.Context, req appserver.SessionPresentationModeRequest) (appserver.CommandResult, error) {
	path, err := focusedSessionPath(req.SessionID, "/configuration/presentation-mode")
	if err != nil {
		return appserver.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, path, req.WriteBase, req)
}
func (c *Client) ConfigureSessionPresentation(ctx context.Context, req appserver.SessionPresentationConfigRequest) (appserver.CommandResult, error) {
	path, err := focusedSessionPath(req.SessionID, "/configuration/presentation-config")
	if err != nil {
		return appserver.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, path, req.WriteBase, req)
}
func (c *Client) ConnectModel(ctx context.Context, req appserver.ConnectModelRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/configuration/connect-model", req.WriteBase, req)
}
func (c *Client) UseModel(ctx context.Context, req appserver.UseModelRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/configuration/use-model", req.WriteBase, req)
}
func (c *Client) DeleteModel(ctx context.Context, req appserver.DeleteModelRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/configuration/delete-model", req.WriteBase, req)
}
func (c *Client) SetSandboxBackend(ctx context.Context, req appserver.SandboxRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/configuration/sandbox-backend", req.WriteBase, req)
}
func (c *Client) PrepareSandbox(ctx context.Context, req appserver.SandboxRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/configuration/sandbox-prepare", req.WriteBase, req)
}
func (c *Client) RepairSandbox(ctx context.Context, req appserver.SandboxRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/configuration/sandbox-repair", req.WriteBase, req)
}
func (c *Client) ResetSandbox(ctx context.Context, req appserver.SandboxRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/configuration/sandbox-reset", req.WriteBase, req)
}
func (c *Client) RefreshSandbox(ctx context.Context, req appserver.SandboxRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/configuration/sandbox-refresh", req.WriteBase, req)
}

func (c *Client) ListAgents(ctx context.Context, req appserver.AgentRequest) ([]appserver.AgentCandidate, error) {
	return doFocusedSessionJSON[[]appserver.AgentCandidate](ctx, c, req.SessionID, "/agents/list", req)
}
func (c *Client) AgentStatus(ctx context.Context, req appserver.AgentRequest) (appserver.AgentStatusSnapshot, error) {
	return doFocusedSessionJSON[appserver.AgentStatusSnapshot](ctx, c, req.SessionID, "/agents/status", req)
}
func (c *Client) HandoffAgent(ctx context.Context, req appserver.HandoffAgentRequest) (appserver.CommandResult, error) {
	path, err := focusedSessionPath(req.SessionID, "/agents/handoff")
	if err != nil {
		return appserver.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, path, req.WriteBase, req)
}
func (c *Client) PrepareACP(ctx context.Context, req appserver.PrepareACPRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/prepare-acp", req.WriteBase, req)
}
func (c *Client) PrepareACPAuthentication(ctx context.Context, req appserver.PrepareACPAuthenticationRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/prepare-acp-auth", req.WriteBase, req)
}
func (c *Client) ACPPreparation(ctx context.Context, req appserver.ACPPreparationRequest) (controlagents.ACPPreparation, error) {
	ref := strings.TrimSpace(req.Ref)
	if ref == "" {
		return controlagents.ACPPreparation{}, errors.New("control http client: ACP preparation ref is required")
	}
	return doFocusedJSON[controlagents.ACPPreparation](ctx, c, http.MethodGet, "/agents/acp-preparations/"+url.PathEscape(ref), nil)
}
func (c *Client) ConnectACP(ctx context.Context, req appserver.ConnectACPRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/connect-acp", req.WriteBase, req)
}
func (c *Client) DisconnectCandidates(ctx context.Context, req appserver.AgentRequest) (appserver.DisconnectCandidatesSnapshot, error) {
	if strings.TrimSpace(req.SessionID) != "" {
		return appserver.DisconnectCandidatesSnapshot{}, errors.New("control http client: ACP disconnect candidates are Host-scoped")
	}
	return doFocusedJSON[appserver.DisconnectCandidatesSnapshot](ctx, c, http.MethodPost, "/agents/disconnect-candidates", req)
}
func (c *Client) DisconnectACP(ctx context.Context, req appserver.DisconnectACPRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/disconnect-acp", req.WriteBase, req)
}
func (c *Client) AgentBindingStatus(ctx context.Context, req appserver.AgentRequest) (agentbinding.Status, error) {
	if strings.TrimSpace(req.SessionID) != "" {
		return agentbinding.Status{}, errors.New("control http client: Agent binding status is Host-scoped")
	}
	return doFocusedJSON[agentbinding.Status](ctx, c, http.MethodPost, "/agents/binding-status", req)
}
func (c *Client) BindAgentBinding(ctx context.Context, req appserver.BindAgentBindingRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/bind", req.WriteBase, req)
}
func (c *Client) ResetAgentBinding(ctx context.Context, req appserver.ResetAgentBindingRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/reset-binding", req.WriteBase, req)
}
func (c *Client) CreateAgentRole(ctx context.Context, req appserver.CreateAgentRoleRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/create-role", req.WriteBase, req)
}
func (c *Client) DeleteAgentRole(ctx context.Context, req appserver.DeleteAgentRoleRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/delete-role", req.WriteBase, req)
}
func (c *Client) SaveAgentBindingSet(ctx context.Context, req appserver.AgentBindingSetRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/save-binding-set", req.WriteBase, req)
}
func (c *Client) ApplyAgentBindingSet(ctx context.Context, req appserver.AgentBindingSetRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/apply-binding-set", req.WriteBase, req)
}
func (c *Client) DeleteAgentBindingSet(ctx context.Context, req appserver.AgentBindingSetRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/agents/delete-binding-set", req.WriteBase, req)
}

func (c *Client) CompleteFile(ctx context.Context, req appserver.CompletionRequest) ([]appserver.CompletionCandidate, error) {
	return doFocusedSessionJSON[[]appserver.CompletionCandidate](ctx, c, req.SessionID, "/completion/files", req)
}
func (c *Client) CompleteSkill(ctx context.Context, req appserver.CompletionRequest) ([]appserver.CompletionCandidate, error) {
	return doFocusedSessionJSON[[]appserver.CompletionCandidate](ctx, c, req.SessionID, "/completion/skills", req)
}
func (c *Client) CompleteResume(ctx context.Context, req appserver.CompletionRequest) ([]appserver.ResumeCandidate, error) {
	return doFocusedSessionJSON[[]appserver.ResumeCandidate](ctx, c, req.SessionID, "/completion/sessions", req)
}
func (c *Client) CompleteSlashArg(ctx context.Context, req appserver.CompletionRequest) ([]appserver.SlashArgCandidate, error) {
	return doFocusedSessionJSON[[]appserver.SlashArgCandidate](ctx, c, req.SessionID, "/completion/slash-arguments", req)
}
func (c *Client) ResolveSkill(ctx context.Context, req appserver.CompletionRequest) (appserver.SkillResolveResult, error) {
	return doFocusedSessionJSON[appserver.SkillResolveResult](ctx, c, req.SessionID, "/completion/resolve-skill", req)
}

func (c *Client) ListPlugins(ctx context.Context, req appserver.PluginRequest) ([]appserver.PluginSnapshot, error) {
	return doFocusedSessionJSON[[]appserver.PluginSnapshot](ctx, c, req.SessionID, "/plugins/list", req)
}
func (c *Client) AddMarketplace(ctx context.Context, req appserver.AddMarketplaceRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/plugins/add-marketplace", req.WriteBase, req)
}
func (c *Client) ListMarketplaces(ctx context.Context, req appserver.PluginRequest) ([]appserver.MarketplaceSnapshot, error) {
	return doFocusedSessionJSON[[]appserver.MarketplaceSnapshot](ctx, c, req.SessionID, "/plugins/list-marketplaces", req)
}
func (c *Client) UpdateMarketplace(ctx context.Context, req appserver.UpdateMarketplaceRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/plugins/update-marketplace", req.WriteBase, req)
}
func (c *Client) RemoveMarketplace(ctx context.Context, req appserver.RemoveMarketplaceRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/plugins/remove-marketplace", req.WriteBase, req)
}
func (c *Client) AddPluginPath(ctx context.Context, req appserver.AddPluginPathRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/plugins/add-path", req.WriteBase, req)
}
func (c *Client) InstallPlugin(ctx context.Context, req appserver.InstallPluginRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/plugins/install", req.WriteBase, req)
}
func (c *Client) EnablePlugin(ctx context.Context, req appserver.EnablePluginRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/plugins/enable", req.WriteBase, req)
}
func (c *Client) DisablePlugin(ctx context.Context, req appserver.DisablePluginRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/plugins/disable", req.WriteBase, req)
}
func (c *Client) RemovePlugin(ctx context.Context, req appserver.RemovePluginRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/plugins/remove", req.WriteBase, req)
}
func (c *Client) InspectPlugin(ctx context.Context, req appserver.PluginRequest) (appserver.PluginSnapshot, error) {
	return doFocusedSessionJSON[appserver.PluginSnapshot](ctx, c, req.SessionID, "/plugins/inspect", req)
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

var _ appserver.ParticipantClient = (*Client)(nil)
var _ appserver.ConfigurationClient = (*Client)(nil)
var _ appserver.AgentClient = (*Client)(nil)
var _ appserver.CompletionClient = (*Client)(nil)
var _ appserver.PluginClient = (*Client)(nil)
var _ appserver.PresentationClient = (*Client)(nil)
var _ appserver.TerminalClient = (*Client)(nil)
