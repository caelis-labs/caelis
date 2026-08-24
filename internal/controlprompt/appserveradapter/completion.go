package appserveradapter

import (
	"context"
	"errors"
	"strings"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
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
	request.SessionID = a.clientSessionID()
	candidates, err := a.completionClient.CompleteSkill(ctx, request)
	if err != nil && request.SessionID != "" {
		request.SessionID = ""
		return a.completionClient.CompleteSkill(ctx, request)
	}
	return candidates, err
}

func (a *SessionClientAdapter) CompleteResume(ctx context.Context, query string, limit int) ([]controlprompt.ResumeCandidate, error) {
	request, err := a.completionRequest(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	return a.completionClient.CompleteResume(ctx, request)
}

func (a *SessionClientAdapter) CompleteSlashArg(ctx context.Context, command, query string, limit int) ([]controlprompt.SlashArgCandidate, error) {
	trimmedCommand := strings.TrimSpace(command)
	if raw, ok := strings.CutPrefix(trimmedCommand, "connect-acp-model:"); ok {
		state, err := controlagents.DecodeConnectState(raw)
		if err != nil {
			return nil, err
		}
		snapshot, err := a.DiscoverACPConnection(ctx, state.ConnectRequest(a.WorkspaceDir()))
		if err != nil {
			return nil, err
		}
		return projectConnectACPModels(snapshot, query, limit), nil
	}
	request, err := a.completionRequest(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	request.Command = trimmedCommand
	request.SessionID = a.clientSessionID()
	candidates, err := a.completionClient.CompleteSlashArg(ctx, request)
	if err != nil && request.SessionID != "" {
		request.SessionID = ""
		return a.completionClient.CompleteSlashArg(ctx, request)
	}
	return candidates, err
}

func (a *SessionClientAdapter) ResolveSkill(ctx context.Context, name string) (controlprompt.SkillResolveResult, error) {
	request, err := a.completionRequest(ctx, "", 0)
	if err != nil {
		return controlprompt.SkillResolveResult{}, err
	}
	request.Name = strings.TrimSpace(name)
	request.SessionID = a.clientSessionID()
	result, err := a.completionClient.ResolveSkill(ctx, request)
	if err != nil && request.SessionID != "" {
		request.SessionID = ""
		return a.completionClient.ResolveSkill(ctx, request)
	}
	return result, err
}

func (a *SessionClientAdapter) completionRequest(ctx context.Context, query string, limit int) (appserver.CompletionRequest, error) {
	if a == nil || a.completionClient == nil {
		return appserver.CompletionRequest{}, errors.New("app/gatewayapp/controladapter: completion client is unavailable")
	}
	return appserver.CompletionRequest{
		WorkspaceKey: a.workspaceKey,
		CWD:          a.WorkspaceDir(),
		Surface:      a.surface,
		Query:        query,
		Limit:        limit,
	}, nil
}
