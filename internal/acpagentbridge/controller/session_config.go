package controller

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/sessionconfig"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/subagent"
)

func (m *Manager) participantSessionConfig(ctx context.Context, parent session.Session, cfg subagent.AgentConfig, frozen placement.Placement) (subagent.AgentConfig, error) {
	if frozen.Kind == placement.KindModel {
		if cfg.PinnedModel == nil || m.placementResolver == nil || m.sessionPreparer == nil {
			return subagent.AgentConfig{}, fmt.Errorf("internal/acpagentbridge/controller: provider participant preparation is unavailable")
		}
		// Revalidate the recorded placement on reattachment as well as first
		// startup. A handle's current binding must not replace its durable model.
		resolved, err := m.placementResolver(ctx, tasksubagent.SpawnContext{
			SessionRef: parent.SessionRef, Session: parent, CWD: parent.CWD,
		}, delegation.TargetRequest{Target: delegation.Target{Selector: cfg.Name, Placement: frozen}})
		if err != nil {
			return subagent.AgentConfig{}, err
		}
		cfg.PinnedModel = resolved.PinnedModel
		cfg.SessionOptions = resolved.SessionOptions
		return cfg, nil
	}
	cfg.SessionOptions = agents.SessionOptions{
		ModelID: strings.TrimSpace(frozen.Model), ConfigValues: maps.Clone(frozen.SessionConfigValues),
		ReasoningEffortConfigID: frozen.ReasoningEffortConfigID,
	}
	return cfg, nil
}

// configureSession lets Control consume process-local provider configuration
// before checking the remaining defaults against the ACP peer's advertised IDs.
// Both new and resumed participants use the same preparation as spawned Tasks.
func (m *Manager) configureSession(ctx context.Context, acpClient sessionconfig.Client, sessionID string, state sessionconfig.State, cfg subagent.AgentConfig) (sessionconfig.State, error) {
	options := cfg.SessionOptions
	if m.sessionPreparer != nil {
		var err error
		options, err = m.sessionPreparer(ctx, sessionID, cfg)
		if err != nil {
			return sessionconfig.State{}, fmt.Errorf("internal/acpagentbridge/controller: prepare Session: %w", err)
		}
	}
	return sessionconfig.Apply(ctx, acpClient, sessionID, state, options)
}
