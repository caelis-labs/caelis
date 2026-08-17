package local

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	controladapter "github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// CompletionService implements Host completion with optional Session-bound
// runtime projections inside the AppServer boundary.
type CompletionService struct {
	hostDeps            *controladapter.CompletionAssemblyDeps
	acquireRuntime      acquireControlRuntimeFunc
	resolveWorkspace    resolveWorkspaceAddressFunc
	currentSkillCatalog func(context.Context, session.WorkspaceRef) (skill.Catalog, error)
	listSessions        func(context.Context, appserver.Principal, appserver.ListSessionsRequest) (session.SessionList, error)
}

type completionServiceDeps struct {
	hostDeps            *controladapter.CompletionAssemblyDeps
	acquireRuntime      acquireControlRuntimeFunc
	resolveWorkspace    resolveWorkspaceAddressFunc
	currentSkillCatalog func(context.Context, session.WorkspaceRef) (skill.Catalog, error)
	listSessions        func(context.Context, appserver.Principal, appserver.ListSessionsRequest) (session.SessionList, error)
}

func newCompletionService(deps completionServiceDeps) (*CompletionService, error) {
	if deps.hostDeps == nil || deps.acquireRuntime == nil || deps.resolveWorkspace == nil ||
		deps.currentSkillCatalog == nil || deps.listSessions == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: completion service dependencies are required")
	}
	return &CompletionService{
		hostDeps: deps.hostDeps, acquireRuntime: deps.acquireRuntime, resolveWorkspace: deps.resolveWorkspace,
		currentSkillCatalog: deps.currentSkillCatalog, listSessions: deps.listSessions,
	}, nil
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
	if s == nil || s.hostDeps == nil {
		return nil, nil, errors.New("app/gatewayapp/controladapter/local: completion service is unavailable")
	}
	if err := authorizeHostCapability(principal); err != nil {
		return nil, nil, err
	}
	deps := s.controlRuntimeDeps(principal)
	workspace, err := s.workspaceAddress(req.WorkspaceKey, req.CWD)
	if err != nil {
		return nil, nil, err
	}
	deps.Session.Workspace = workspace
	return controladapter.NewCompletionAssemblerForHost(
		*deps,
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
	if s == nil || s.hostDeps == nil {
		return nil, nil, errors.New("app/gatewayapp/controladapter/local: completion service is unavailable")
	}
	if err := authorizeHostCapability(principal); err != nil {
		return nil, nil, err
	}
	workspace, err := s.workspaceAddress(req.WorkspaceKey, req.CWD)
	if err != nil {
		return nil, nil, err
	}
	catalog, err := s.currentSkillCatalog(ctx, workspace)
	if err != nil {
		return nil, nil, err
	}
	deps := s.controlRuntimeDeps(principal)
	deps.Session.Workspace = workspace
	deps.Skill.SnapshotFn = func() skill.Catalog { return catalog }
	driver := controladapter.NewCompletionAssemblerForHost(
		*deps,
		strings.TrimSpace(req.Surface),
		"",
	)
	return driver, func() {}, nil
}

func (s *CompletionService) runtimeAdapter(ctx context.Context, principal appserver.Principal, req appserver.CompletionRequest) (controladapter.CompletionAssembler, func(), error) {
	if s == nil || s.hostDeps == nil {
		return nil, nil, errors.New("app/gatewayapp/controladapter/local: completion service is unavailable")
	}
	if err := authorizeHostCapability(principal); err != nil {
		return nil, nil, err
	}
	deps := s.controlRuntimeDeps(principal)
	if strings.TrimSpace(req.SessionID) == "" {
		workspace, err := s.workspaceAddress(req.WorkspaceKey, req.CWD)
		if err != nil {
			return nil, nil, err
		}
		deps.Session.Workspace = workspace
		return controladapter.NewCompletionAssemblerForHost(
			*deps,
			strings.TrimSpace(req.Surface),
			"",
		), func() {}, nil
	}
	lease, err := s.acquireRuntime(ctx, principal, appserver.ActionSessionInspect, req.SessionID, false)
	if err != nil {
		return nil, nil, err
	}
	assemblyDeps := completionAssemblyDepsFromLease(lease)
	if assemblyDeps == nil {
		_ = lease.Close(ctx)
		return nil, nil, errors.New("app/gatewayapp/controladapter/local: completion Runtime projection is unavailable")
	}
	deps = assemblyDeps
	s.bindPrincipalSessionList(deps, principal)
	driver, err := controladapter.NewCompletionAssemblerForSession(ctx, *deps, lease.Session(), strings.TrimSpace(req.Surface), "")
	if err != nil {
		_ = lease.Close(ctx)
		return nil, nil, err
	}
	return driver, func() { _ = lease.Close(context.Background()) }, nil
}

func (s *CompletionService) workspaceAddress(key, cwd string) (session.WorkspaceRef, error) {
	if s == nil || s.resolveWorkspace == nil {
		return session.WorkspaceRef{}, errors.New("app/gatewayapp/controladapter/local: completion service is unavailable")
	}
	return s.resolveWorkspace(session.WorkspaceRef{
		Key: strings.TrimSpace(key),
		CWD: strings.TrimSpace(cwd),
	})
}

func (s *CompletionService) controlRuntimeDeps(principal appserver.Principal) *controladapter.CompletionAssemblyDeps {
	if s == nil || s.hostDeps == nil {
		return nil
	}
	deps := *s.hostDeps
	s.bindPrincipalSessionList(&deps, principal)
	return &deps
}

func (s *CompletionService) bindPrincipalSessionList(deps *controladapter.CompletionAssemblyDeps, principal appserver.Principal) {
	if deps == nil {
		return
	}
	deps.Session.ListSessionsFn = func(ctx context.Context, req kernel.ListSessionsRequest) (session.SessionList, error) {
		return s.listSessions(ctx, principal, appserver.ListSessionsRequest{
			WorkspaceKey: req.WorkspaceKey,
			CWD:          req.CWD,
			Cursor:       req.Cursor,
			Limit:        req.Limit,
		})
	}
}

var _ appserver.CompletionService = (*CompletionService)(nil)
