package controladapter

import (
	"context"
	"errors"
	"strings"

	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func (a *SessionClientAdapter) CompleteFile(ctx context.Context, query string, limit int) ([]controlprompt.CompletionCandidate, error) {
	request, err := a.completionRequest(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	return a.completionClient.CompleteFile(ctx, request)
}

func (a *SessionClientAdapter) CompleteSkill(ctx context.Context, query string, limit int) ([]controlprompt.CompletionCandidate, error) {
	request, err := a.completionRequest(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	return a.completionClient.CompleteSkill(ctx, request)
}

func (a *SessionClientAdapter) CompleteResume(ctx context.Context, query string, limit int) ([]controlprompt.ResumeCandidate, error) {
	request, err := a.completionRequest(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	return a.completionClient.CompleteResume(ctx, request)
}

func (a *SessionClientAdapter) CompleteSlashArg(ctx context.Context, command, query string, limit int) ([]controlprompt.SlashArgCandidate, error) {
	request, err := a.completionRequest(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	request.Command = strings.TrimSpace(command)
	return a.completionClient.CompleteSlashArg(ctx, request)
}

func (a *SessionClientAdapter) ResolveSkill(ctx context.Context, name string) (controlprompt.SkillResolveResult, error) {
	request, err := a.completionRequest(ctx, "", 0)
	if err != nil {
		return controlprompt.SkillResolveResult{}, err
	}
	request.Name = strings.TrimSpace(name)
	return a.completionClient.ResolveSkill(ctx, request)
}

func (a *SessionClientAdapter) completionRequest(ctx context.Context, query string, limit int) (controlclient.CompletionRequest, error) {
	if a == nil || a.completionClient == nil {
		return controlclient.CompletionRequest{}, errors.New("app/gatewayapp/controladapter: completion client is unavailable")
	}
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return controlclient.CompletionRequest{}, err
	}
	return controlclient.CompletionRequest{
		SessionID: state.SessionID,
		Surface:   a.surface,
		Query:     query,
		Limit:     limit,
	}, nil
}
