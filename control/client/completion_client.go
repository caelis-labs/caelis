package controlclient

import (
	"context"
	"errors"
	"strings"
)

// CompletionRequest addresses Host/workspace completion and skill discovery.
// SessionID is optional context for controller-specific completion only. When
// no Session is selected, WorkspaceKey and CWD identify the calling client's
// workspace rather than the persistent Host's startup workspace.
// Command is only the already-parsed slash command name; AppServer does not
// accept or interpret raw slash input.
type CompletionRequest struct {
	SessionID    string `json:"session_id,omitempty"`
	WorkspaceKey string `json:"workspace_key,omitempty"`
	CWD          string `json:"cwd,omitempty"`
	Surface      string `json:"surface,omitempty"`
	Query        string `json:"query,omitempty"`
	Command      string `json:"command,omitempty"`
	Name         string `json:"name,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

// CompletionService is the principal-aware AppServer discovery capability.
type CompletionService interface {
	CompleteFile(context.Context, Principal, CompletionRequest) ([]CompletionCandidate, error)
	CompleteSkill(context.Context, Principal, CompletionRequest) ([]CompletionCandidate, error)
	CompleteResume(context.Context, Principal, CompletionRequest) ([]ResumeCandidate, error)
	CompleteSlashArg(context.Context, Principal, CompletionRequest) ([]SlashArgCandidate, error)
	ResolveSkill(context.Context, Principal, CompletionRequest) (SkillResolveResult, error)
}

// CompletionClient is the transport-neutral presentation client.
type CompletionClient interface {
	CompleteFile(context.Context, CompletionRequest) ([]CompletionCandidate, error)
	CompleteSkill(context.Context, CompletionRequest) ([]CompletionCandidate, error)
	CompleteResume(context.Context, CompletionRequest) ([]ResumeCandidate, error)
	CompleteSlashArg(context.Context, CompletionRequest) ([]SlashArgCandidate, error)
	ResolveSkill(context.Context, CompletionRequest) (SkillResolveResult, error)
}

type boundCompletionClient struct {
	service   CompletionService
	principal Principal
}

func BindCompletionClient(service CompletionService, principal Principal) (CompletionClient, error) {
	if service == nil {
		return nil, errors.New("controlclient: completion service is required")
	}
	principal.ID = strings.TrimSpace(principal.ID)
	if principal.ID == "" {
		return nil, errors.New("controlclient: principal ID is required")
	}
	principal.Roles = append([]string(nil), principal.Roles...)
	return &boundCompletionClient{service: service, principal: principal}, nil
}

func (c *boundCompletionClient) boundPrincipal() Principal {
	principal := c.principal
	principal.Roles = append([]string(nil), principal.Roles...)
	return principal
}

func (c *boundCompletionClient) CompleteFile(ctx context.Context, req CompletionRequest) ([]CompletionCandidate, error) {
	return c.service.CompleteFile(ctx, c.boundPrincipal(), req)
}

func (c *boundCompletionClient) CompleteSkill(ctx context.Context, req CompletionRequest) ([]CompletionCandidate, error) {
	return c.service.CompleteSkill(ctx, c.boundPrincipal(), req)
}

func (c *boundCompletionClient) CompleteResume(ctx context.Context, req CompletionRequest) ([]ResumeCandidate, error) {
	return c.service.CompleteResume(ctx, c.boundPrincipal(), req)
}

func (c *boundCompletionClient) CompleteSlashArg(ctx context.Context, req CompletionRequest) ([]SlashArgCandidate, error) {
	return c.service.CompleteSlashArg(ctx, c.boundPrincipal(), req)
}

func (c *boundCompletionClient) ResolveSkill(ctx context.Context, req CompletionRequest) (SkillResolveResult, error) {
	return c.service.ResolveSkill(ctx, c.boundPrincipal(), req)
}

var _ CompletionClient = (*boundCompletionClient)(nil)
