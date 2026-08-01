package local

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	controladapter "github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
	controlclient "github.com/caelis-labs/caelis/control/client"
)

// CompletionService implements Session-addressed completion and skill
// discovery inside the AppServer boundary.
type CompletionService struct {
	host *gatewayapp.Stack
}

func NewCompletionService(host *gatewayapp.Stack) (*CompletionService, error) {
	if host == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: host Stack is required")
	}
	return &CompletionService{host: host}, nil
}

func (s *CompletionService) CompleteFile(ctx context.Context, principal controlclient.Principal, req controlclient.CompletionRequest) ([]controlclient.CompletionCandidate, error) {
	driver, closeDriver, err := s.runtimeAdapter(ctx, principal, req)
	if err != nil {
		return nil, err
	}
	defer closeDriver()
	return driver.CompleteFile(ctx, req.Query, req.Limit)
}

func (s *CompletionService) CompleteSkill(ctx context.Context, principal controlclient.Principal, req controlclient.CompletionRequest) ([]controlclient.CompletionCandidate, error) {
	driver, closeDriver, err := s.runtimeAdapter(ctx, principal, req)
	if err != nil {
		return nil, err
	}
	defer closeDriver()
	return driver.CompleteSkill(ctx, req.Query, req.Limit)
}

func (s *CompletionService) CompleteResume(ctx context.Context, principal controlclient.Principal, req controlclient.CompletionRequest) ([]controlclient.ResumeCandidate, error) {
	driver, closeDriver, err := s.runtimeAdapter(ctx, principal, req)
	if err != nil {
		return nil, err
	}
	defer closeDriver()
	return driver.CompleteResume(ctx, req.Query, req.Limit)
}

func (s *CompletionService) CompleteSlashArg(ctx context.Context, principal controlclient.Principal, req controlclient.CompletionRequest) ([]controlclient.SlashArgCandidate, error) {
	driver, closeDriver, err := s.runtimeAdapter(ctx, principal, req)
	if err != nil {
		return nil, err
	}
	defer closeDriver()
	return driver.CompleteSlashArg(ctx, req.Command, req.Query, req.Limit)
}

func (s *CompletionService) ResolveSkill(ctx context.Context, principal controlclient.Principal, req controlclient.CompletionRequest) (controlclient.SkillResolveResult, error) {
	driver, closeDriver, err := s.runtimeAdapter(ctx, principal, req)
	if err != nil {
		return controlclient.SkillResolveResult{}, err
	}
	defer closeDriver()
	return driver.ResolveSkill(ctx, req.Name)
}

func (s *CompletionService) runtimeAdapter(ctx context.Context, principal controlclient.Principal, req controlclient.CompletionRequest) (controladapter.CompletionAssembler, func(), error) {
	if s == nil || s.host == nil {
		return nil, nil, errors.New("app/gatewayapp/controladapter/local: completion service is unavailable")
	}
	lease, err := s.host.AcquireControlRuntime(ctx, principal, controlclient.ActionSessionInspect, req.SessionID, false)
	if err != nil {
		return nil, nil, err
	}
	driver, err := controladapter.NewCompletionAssemblerForSession(ctx, runtimeStack(lease.Runtime()), lease.Session(), strings.TrimSpace(req.Surface), "")
	if err != nil {
		_ = lease.Close(ctx)
		return nil, nil, err
	}
	return driver, func() { _ = lease.Close(context.Background()) }, nil
}

var _ controlclient.CompletionService = (*CompletionService)(nil)
