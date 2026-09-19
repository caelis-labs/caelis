package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	acp "github.com/caelis-labs/acp-go-sdk"
	sdkplacement "github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/control/placement"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/subagent"
	"github.com/google/uuid"
)

func (s *runtimeComposition) controllerCollaboration(ctx context.Context, ref session.SessionRef, binding session.ControllerBinding, cfg subagent.AgentConfig) (subagent.AgentConfig, error) {
	if cfg.BuiltinRuntime {
		return cfg, nil
	}
	service := s.authorities.collaboration
	process := s.runtimeProcessSnapshot()
	if service == nil || process.childControlURL == "" {
		// Embeddings without an HTTP control endpoint cannot offer MCP tools.
		return cfg, nil
	}
	command, err := os.Executable()
	if err != nil {
		return cfg, err
	}
	spawnTool, err := s.controllerSpawnTool(ctx, binding.Placement)
	if err != nil {
		return cfg, err
	}
	definitions, err := json.Marshal(append(collaboration.Definitions(true), spawnTool.Definition()))
	if err != nil {
		return cfg, err
	}
	grant := service.Prepare(collaboration.Identity{Session: ref.SessionID, Member: "parent"}, binding.EpochID)
	cfg.MCPGrant = grant
	cfg.MCPServers = []acp.McpServer{{Stdio: &acp.McpServerStdio{Name: "caelis-collaboration", Command: command, Args: []string{"collaboration", "mcp", "--stdio"}, Env: []acp.EnvVariable{
		{Name: "CAELIS_COLLABORATION_URL", Value: process.childControlURL}, {Name: "CAELIS_COLLABORATION_TOKEN", Value: grant.Token()}, {Name: "CAELIS_COLLABORATION_TOOLS", Value: string(definitions)},
	}}}}
	return cfg, nil
}

func (b *collaborationBackend) Start(ctx context.Context, identity collaboration.Identity, expected string, args json.RawMessage) (json.RawMessage, error) {
	b.router.mu.RLock()
	registry := b.router.runtimes
	b.router.mu.RUnlock()
	if registry == nil {
		return nil, errors.New("collaboration runtime unavailable")
	}
	rt, active, release, err := registry.acquireControlRuntime(ctx, identity.Session, true)
	if err != nil {
		return nil, err
	}
	if release != nil {
		defer func() { _ = release(context.WithoutCancel(ctx)) }()
	}
	if identity.Member != "parent" || active.Controller.Kind != session.ControllerKindACP || active.Controller.EpochID != expected || rt == nil || rt.instance == nil {
		return nil, errors.New("controller identity changed")
	}
	spawnTool, err := rt.instance.controllerSpawnTool(ctx, active.Controller.Placement)
	if err != nil {
		return nil, err
	}
	call := tool.Call{ID: "collaboration-" + uuid.NewString(), Name: spawn.ToolName, Input: args}
	result, err := rt.instance.engine.CallControllerSpawn(ctx, active.SessionRef, expected, spawnTool, call)
	if err != nil {
		return nil, err
	}
	if result.IsError {
		return nil, errors.New("StartThread was rejected by the current execution policy")
	}
	for _, part := range result.Content {
		if part.Text != nil && json.Valid([]byte(part.Text.Text)) {
			return json.RawMessage(part.Text.Text), nil
		}
		if part.JSON != nil {
			return append(json.RawMessage(nil), part.JSON.Value...), nil
		}
	}
	return nil, errors.New("StartThread returned no structured result")
}

// Discovery and execution use the same activation-scoped delegation catalog.
func (s *runtimeComposition) controllerSpawnTool(ctx context.Context, frozen sdkplacement.Placement) (spawn.Tool, error) {
	selected := placement.SessionContext{ProfileID: frozen.ProfileID, Effort: frozen.ReasoningEffort}
	snapshot, err := s.placementSnapshot(ctx)
	if err != nil {
		return spawn.Tool{}, err
	}
	if profile, ok := modelprofile.Lookup(snapshot.placement.Profiles, selected.ProfileID); ok {
		selected.Speed = profile.SelectedSpeed(frozen)
	}
	agents, targets, err := s.delegationSpawnConfiguration(selected)
	if err != nil {
		return spawn.Tool{}, err
	}
	return spawn.NewWithTargets(agents, targets), nil
}
