package local

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	controladapter "github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// CompletionService implements Host completion with optional Session-bound
// runtime projections inside the AppServer boundary.
type CompletionService struct {
	host *gatewayapp.Stack
}

func NewCompletionService(host *gatewayapp.Stack) (*CompletionService, error) {
	if host == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: host Stack is required")
	}
	return &CompletionService{host: host}, nil
}

func (s *CompletionService) CompleteFile(ctx context.Context, principal appserver.Principal, req appserver.CompletionRequest) ([]appserver.CompletionCandidate, error) {
	driver, closeDriver, err := s.runtimeAdapter(ctx, principal, req)
	if err != nil {
		return nil, err
	}
	defer closeDriver()
	return driver.CompleteFile(ctx, req.Query, req.Limit)
}

func (s *CompletionService) CompleteSkill(ctx context.Context, principal appserver.Principal, req appserver.CompletionRequest) ([]appserver.CompletionCandidate, error) {
	driver, closeDriver, err := s.skillRuntimeAdapter(ctx, principal, req)
	if err != nil {
		return nil, err
	}
	defer closeDriver()
	return driver.CompleteSkill(ctx, req.Query, req.Limit)
}

func (s *CompletionService) CompleteResume(ctx context.Context, principal appserver.Principal, req appserver.CompletionRequest) ([]appserver.ResumeCandidate, error) {
	driver, closeDriver, err := s.runtimeAdapter(ctx, principal, req)
	if err != nil {
		return nil, err
	}
	defer closeDriver()
	return driver.CompleteResume(ctx, req.Query, req.Limit)
}

func (s *CompletionService) CompleteSlashArg(ctx context.Context, principal appserver.Principal, req appserver.CompletionRequest) ([]appserver.SlashArgCandidate, error) {
	driver, closeDriver, err := s.slashArgAdapter(ctx, principal, req)
	if err != nil {
		return nil, err
	}
	defer closeDriver()
	return driver.CompleteSlashArg(ctx, req.Command, req.Query, req.Limit)
}

// slashArgAdapter keeps App configuration discovery live. Slash configuration
// candidates are read from the Host even when the TUI has selected a Session;
// the eventual command decides whether its write belongs to that Session or
// the App default.
func (s *CompletionService) slashArgAdapter(
	ctx context.Context,
	principal appserver.Principal,
	req appserver.CompletionRequest,
) (controladapter.CompletionAssembler, func(), error) {
	if s == nil || s.host == nil {
		return nil, nil, errors.New("app/gatewayapp/controladapter/local: completion service is unavailable")
	}
	if err := authorizeHostCapability(principal); err != nil {
		return nil, nil, err
	}
	stack := s.runtimeStack(principal)
	workspace, err := s.workspaceAddress(req.WorkspaceKey, req.CWD)
	if err != nil {
		return nil, nil, err
	}
	stack.Session.Workspace = workspace
	return controladapter.NewCompletionAssemblerForStack(
		stack,
		strings.TrimSpace(req.Surface),
		"",
	), func() {}, nil
}

func (s *CompletionService) ResolveSkill(ctx context.Context, principal appserver.Principal, req appserver.CompletionRequest) (appserver.SkillResolveResult, error) {
	driver, closeDriver, err := s.skillRuntimeAdapter(ctx, principal, req)
	if err != nil {
		return appserver.SkillResolveResult{}, err
	}
	defer closeDriver()
	return driver.ResolveSkill(ctx, req.Name)
}

// skillRuntimeAdapter keeps an active Session on its fixed context catalog.
// With no selected Session it reads the current App skill catalog without
// assembling an execution Runtime, so completion never starts plugin MCP
// servers or other Session-scoped contributions.
func (s *CompletionService) skillRuntimeAdapter(
	ctx context.Context,
	principal appserver.Principal,
	req appserver.CompletionRequest,
) (controladapter.CompletionAssembler, func(), error) {
	if strings.TrimSpace(req.SessionID) != "" {
		return s.runtimeAdapter(ctx, principal, req)
	}
	if s == nil || s.host == nil {
		return nil, nil, errors.New("app/gatewayapp/controladapter/local: completion service is unavailable")
	}
	if err := authorizeHostCapability(principal); err != nil {
		return nil, nil, err
	}
	workspace, err := s.workspaceAddress(req.WorkspaceKey, req.CWD)
	if err != nil {
		return nil, nil, err
	}
	catalog, err := s.host.CurrentSkillCatalog(ctx, workspace)
	if err != nil {
		return nil, nil, err
	}
	stack := s.runtimeStack(principal)
	stack.Session.Workspace = workspace
	stack.Skill.SnapshotFn = func() skill.Catalog { return catalog }
	driver := controladapter.NewCompletionAssemblerForStack(
		stack,
		strings.TrimSpace(req.Surface),
		"",
	)
	return driver, func() {}, nil
}

func (s *CompletionService) runtimeAdapter(ctx context.Context, principal appserver.Principal, req appserver.CompletionRequest) (controladapter.CompletionAssembler, func(), error) {
	if s == nil || s.host == nil {
		return nil, nil, errors.New("app/gatewayapp/controladapter/local: completion service is unavailable")
	}
	if err := authorizeHostCapability(principal); err != nil {
		return nil, nil, err
	}
	stack := s.runtimeStack(principal)
	if strings.TrimSpace(req.SessionID) == "" {
		workspace, err := s.workspaceAddress(req.WorkspaceKey, req.CWD)
		if err != nil {
			return nil, nil, err
		}
		stack.Session.Workspace = workspace
		return controladapter.NewCompletionAssemblerForStack(
			stack,
			strings.TrimSpace(req.Surface),
			"",
		), func() {}, nil
	}
	lease, err := s.host.AcquireControlRuntime(ctx, principal, appserver.ActionSessionInspect, req.SessionID, false)
	if err != nil {
		return nil, nil, err
	}
	stack = runtimeStack(lease.Runtime())
	s.bindPrincipalSessionList(stack, principal)
	driver, err := controladapter.NewCompletionAssemblerForSession(ctx, stack, lease.Session(), strings.TrimSpace(req.Surface), "")
	if err != nil {
		_ = lease.Close(ctx)
		return nil, nil, err
	}
	return driver, func() { _ = lease.Close(context.Background()) }, nil
}

func (s *CompletionService) workspaceAddress(key, cwd string) (session.WorkspaceRef, error) {
	if s == nil || s.host == nil {
		return session.WorkspaceRef{}, errors.New("app/gatewayapp/controladapter/local: completion service is unavailable")
	}
	return s.host.ResolveWorkspaceAddress(session.WorkspaceRef{
		Key: strings.TrimSpace(key),
		CWD: strings.TrimSpace(cwd),
	})
}

func (s *CompletionService) runtimeStack(principal appserver.Principal) *controladapter.RuntimeStack {
	stack := runtimeStack(s.host)
	s.bindPrincipalSessionList(stack, principal)
	return stack
}

func (s *CompletionService) bindPrincipalSessionList(stack *controladapter.RuntimeStack, principal appserver.Principal) {
	if stack == nil {
		return
	}
	stack.Session.ListSessionsFn = func(ctx context.Context, req kernel.ListSessionsRequest) (session.SessionList, error) {
		return s.host.ControlClient().ListSessions(ctx, principal, appserver.ListSessionsRequest{
			WorkspaceKey: req.WorkspaceKey,
			CWD:          req.CWD,
			Cursor:       req.Cursor,
			Limit:        req.Limit,
		})
	}
}

var _ appserver.CompletionService = (*CompletionService)(nil)
