package runner

import (
	"context"
	"fmt"

	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/session"
	"github.com/OnslaughtSnail/caelis/tool"
	"github.com/OnslaughtSnail/caelis/tool/mcp"
)

func (r *Runner) prepareInvocationAgent(
	ctx context.Context,
	sess session.Session,
	invID string,
	req RunRequest,
	priorEvts []session.Event,
	observer tool.Observer,
) (agent.Agent, func(), error) {
	runAgent := r.cfg.Agent
	p, ok := r.cfg.Agent.(agent.Prepareable)
	if !ok {
		return runAgent, nil, nil
	}

	var mcpClients []mcp.Client
	cleanup := func() {
		closeMCPClients(mcpClients)
	}

	prepReq := agent.PrepareRequest{}
	if mr, ok := r.cfg.Agent.(interface{ ModelRef() model.Ref }); ok && r.cfg.ModelRegistry != nil {
		llm, _, err := r.cfg.ModelRegistry.Resolve(ctx, mr.ModelRef())
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("runner: resolve model: %w", err)
		}
		prepReq.LLM = llm
	}

	tc, invBackend, err := r.prepareToolContext(ctx, sess, invID, runAgent, req.Metadata)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	prepReq.ToolContext = tc

	tools, clients, err := r.resolveInvocationTools(ctx, runAgent)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	mcpClients = append(mcpClients, clients...)

	prepReq = r.prepareInvocationTools(prepReq, invocationToolSetup{
		Context:      tc,
		Session:      sess,
		PriorEvents:  priorEvts,
		Tools:        tools,
		Backend:      invBackend,
		Agent:        runAgent,
		Branch:       req.Branch,
		InvocationID: invID,
		Observer:     observer,
	})

	return p.Prepare(prepReq), cleanup, nil
}

func (r *Runner) prepareToolContext(
	ctx context.Context,
	sess session.Session,
	invID string,
	runAgent agent.Agent,
	metadata map[string]any,
) (*toolContext, sandbox.Backend, error) {
	tc := &toolContext{
		Context:       ctx,
		sessionRef:    sess.Ref.String(),
		invocationID:  invID,
		agentName:     runAgent.Name(),
		workspaceRoot: sess.Workspace.Root,
	}
	if r.cfg.Sandbox == nil {
		return tc, nil, nil
	}
	backends, err := r.cfg.Sandbox.Available(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("runner: list sandboxes: %w", err)
	}
	if len(backends) == 0 {
		return nil, nil, fmt.Errorf("runner: no sandbox backend available")
	}
	sandboxCfg, err := routeSandbox(ctx, r.cfg.Sandbox, sandbox.RouteRequest{
		WorkspaceRoot:    sess.Workspace.Root,
		RequestedBackend: sandboxBackendFromMetadata(metadata),
		Metadata:         cloneMapAny(metadata),
		Available:        append([]sandbox.Descriptor(nil), backends...),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("runner: route sandbox: %w", err)
	}
	backend, err := r.cfg.Sandbox.Create(ctx, sandboxCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("runner: create sandbox %s: %w", sandboxCfg.BackendName, err)
	}
	fs, err := backend.FileSystem(ctx, sandbox.Constraints{})
	if err != nil {
		return nil, nil, fmt.Errorf("runner: create sandbox filesystem: %w", err)
	}
	tc.backend = backend
	tc.fs = fs
	return tc, backend, nil
}

type invocationToolSetup struct {
	Context      *toolContext
	Session      session.Session
	PriorEvents  []session.Event
	Tools        []tool.Tool
	Backend      sandbox.Backend
	Agent        agent.Agent
	Branch       string
	InvocationID string
	Observer     tool.Observer
}

func (r *Runner) prepareInvocationTools(prepReq agent.PrepareRequest, setup invocationToolSetup) agent.PrepareRequest {
	taskStore := r.cfg.TaskStore
	if taskStore == nil {
		taskStore = NewMemoryTaskStore()
	}
	invTaskMgr := NewTaskManagerWithStore(setup.Backend, taskStore, setup.Session.Ref.String())
	spawnDelegator := r.cfg.SpawnDelegator
	if spawnDelegator == nil {
		spawnCfg := r.cfg
		spawnCfg.TaskStore = taskStore
		spawnDelegator = newRunnerSpawnDelegator(spawnCfg, setup.Session, setup.Agent, setup.Branch, setup.InvocationID)
	}
	if writer, ok := spawnDelegator.(taskWriter); ok {
		invTaskMgr.SetWriter(writer)
	}
	if len(setup.Tools) == 0 {
		return prepReq
	}

	tools := AugmentTools(setup.Tools, invTaskMgr, spawnDelegator)
	prepReq.Tools = WrapTools(
		tools,
		r.cfg.Policy,
		r.cfg.Approver,
		setup.Observer,
		WithApprovalContext(setup.Session, setup.PriorEvents),
		WithToolTracer(r.cfg.Tracer),
		WithToolHooks(r.cfg.Hooks...),
	)
	catalog := tool.NewMemoryRegistry()
	catalog.RegisterAll(prepReq.Tools)
	prepReq.ToolCatalog = catalog
	prepReq.ToolExecutor = newToolExecutor(setup.Context, prepReq.Tools)
	return prepReq
}
