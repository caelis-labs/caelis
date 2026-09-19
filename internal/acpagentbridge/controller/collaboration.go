package controller

import (
	"context"
	"encoding/json"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acputil"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/subagent"
)

// CollaborationResolver attaches Host-issued authority to one controller epoch.
// The Host owns membership and credentials; this bridge only carries the MCP
// declarations over the negotiated ACP Session setup channel.
type CollaborationResolver func(context.Context, session.SessionRef, session.ControllerBinding, subagent.AgentConfig) (subagent.AgentConfig, error)

type controllerPromptGrant interface {
	PreparePrompt(context.Context) (string, error)
	ForgetPrompt(context.Context) error
}

func (r *controllerRun) collaborationPrompt(ctx context.Context, prompt []json.RawMessage) ([]json.RawMessage, controllerPromptGrant, error) {
	r.mu.Lock()
	grant, _ := r.cfg.MCPGrant.(controllerPromptGrant)
	r.mu.Unlock()
	if grant == nil {
		return prompt, nil, nil
	}
	text, err := grant.PreparePrompt(ctx)
	if err != nil {
		return nil, nil, err
	}
	if text == "" {
		return prompt, nil, nil
	}
	result := append([]json.RawMessage(nil), prompt...)
	return append(result, acputil.BuildPromptParts(text, nil)...), grant, nil
}

func (r *controllerRun) promptWithCollaboration(ctx context.Context, prompt []json.RawMessage) (client.PromptResponse, error) {
	prompt, grant, err := r.collaborationPrompt(ctx, prompt)
	if err != nil {
		return client.PromptResponse{}, err
	}
	response, err := r.promptParts(ctx, prompt)
	if err != nil && grant != nil && client.SubmissionProvenNotStarted(err) {
		_ = grant.ForgetPrompt(context.WithoutCancel(ctx))
	}
	return response, err
}
