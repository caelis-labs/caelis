package runner

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync/atomic"
	"time"

	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/model"
	caelisplugin "github.com/OnslaughtSnail/caelis/plugin"
	"github.com/OnslaughtSnail/caelis/policy"
	"github.com/OnslaughtSnail/caelis/prompt"
	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/session"
	"github.com/OnslaughtSnail/caelis/skill"
	"github.com/OnslaughtSnail/caelis/tool"
	"github.com/OnslaughtSnail/caelis/tool/mcp"
	"github.com/OnslaughtSnail/caelis/trace"
)

// Config holds dependencies for creating a Runner.
type Config struct {
	AppName          string
	Agent            agent.Agent
	Sessions         session.Service
	ModelRegistry    model.Registry
	ToolRegistry     tool.Registry
	Sandbox          sandbox.Factory
	Policy           policy.Engine
	Skills           skill.Registry
	Plugins          caelisplugin.Registry
	Prompt           prompt.Assembler
	MCPClientFactory mcp.ClientFactory
	Approver         agent.ApprovalRequester
	Hooks            []agent.Hook
	Tracer           trace.Tracer
	SystemPrompt     string    // assembled system prompt
	Compactor        Compactor // optional compaction engine
	TaskStore        TaskStore
	SpawnDelegator   agent.SpawnDelegator
}

// Runner executes one invocation against one session.
type Runner struct {
	cfg Config
}

// New creates a new Runner.
func New(cfg Config) (*Runner, error) {
	if cfg.Agent == nil {
		return nil, fmt.Errorf("runner: Agent is required")
	}
	if cfg.Sessions == nil {
		return nil, fmt.Errorf("runner: Sessions is required")
	}
	return &Runner{cfg: cfg}, nil
}

// Run executes the configured agent against the session and streams events.
func (r *Runner) Run(ctx context.Context, req RunRequest) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		var invocationErr error
		if r.cfg.Tracer != nil {
			var span trace.Span
			ctx, span = r.cfg.Tracer.Start(ctx, trace.SpanStart{
				Name: "runner.invocation",
				Attributes: map[string]any{
					"session.ref": req.SessionRef.String(),
					"branch":      req.Branch,
				},
			})
			defer func() {
				if span != nil {
					span.End(trace.SpanEnd{Error: invocationErr})
				}
			}()
		}

		// Load session.
		sess, err := r.cfg.Sessions.Get(ctx, req.SessionRef)
		if err != nil {
			if !isNotFound(err) {
				invocationErr = fmt.Errorf("runner: load session: %w", err)
				yield(session.Event{}, fmt.Errorf("runner: load session: %w", err))
				return
			}
			sess, err = r.cfg.Sessions.Create(ctx, session.CreateRequest{
				AppName:      req.SessionRef.AppName,
				UserID:       req.SessionRef.UserID,
				WorkspaceKey: req.SessionRef.WorkspaceKey,
			})
			if err != nil {
				invocationErr = fmt.Errorf("runner: create session: %w", err)
				yield(session.Event{}, fmt.Errorf("runner: create session: %w", err))
				return
			}
		}

		invID := fmt.Sprintf("inv-%d", time.Now().UnixNano())
		runAgent := r.cfg.Agent
		observer := newToolObserverBridge(sess.Ref, invID)

		// Load prior session events once so both approval review and model
		// replay see the same durable history snapshot.
		priorEvts, err := r.cfg.Sessions.Events(ctx, session.EventsRequest{
			SessionRef: sess.Ref,
		})
		if err != nil {
			yield(session.Event{}, fmt.Errorf("runner: load events: %w", err))
			return
		}

		runAgent, cleanup, err := r.prepareInvocationAgent(ctx, sess, invID, req, priorEvts, observer)
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			yield(session.Event{}, err)
			return
		}

		priorMessages := session.ModelContextFromEvents(priorEvts)
		priorMessages, err = r.compactBeforeInvocation(ctx, sess.Ref, priorMessages)
		if err != nil {
			yield(session.Event{}, err)
			return
		}

		systemPrompt, err := r.assembleSystemPrompt(ctx)
		if err != nil {
			yield(session.Event{}, err)
			return
		}
		if systemPrompt != "" {
			sysMsg := model.Message{
				Role:    model.RoleSystem,
				Content: []model.Part{{Text: systemPrompt}},
			}
			priorMessages = append([]model.Message{sysMsg}, priorMessages...)
		}

		// Persist the user message.
		userEvt, err := r.cfg.Sessions.AppendEvent(ctx, sess.Ref, session.Event{
			Kind:       session.EventKindUser,
			Visibility: session.VisibilityCanonical,
			UserPayload: &session.UserPayload{
				Parts: []session.EventPart{
					{Kind: session.PartKindText, Text: req.UserMessage.TextContent()},
				},
			},
		})
		if err != nil {
			yield(session.Event{}, fmt.Errorf("runner: persist user event: %w", err))
			return
		}
		if !yield(userEvt, nil) {
			return
		}

		// Create invocation context with prior messages.
		invCtx := &invocationContext{
			Context:       ctx,
			agent:         runAgent,
			session:       sess,
			invocationID:  invID,
			branch:        req.Branch,
			userMessage:   req.UserMessage,
			priorMessages: priorMessages,
			runConfig:     agent.DefaultRunConfig(),
			hooks:         append([]agent.Hook(nil), r.cfg.Hooks...),
			tracer:        r.cfg.Tracer,
		}

		invHook := agent.InvocationHook{
			AgentName:    runAgent.Name(),
			SessionID:    sess.Ref.String(),
			InvocationID: invID,
			Branch:       req.Branch,
			Metadata:     cloneMapAny(req.Metadata),
		}
		if err := runBeforeInvocationHooks(ctx, r.cfg.Hooks, invHook); err != nil {
			invocationErr = fmt.Errorf("runner: before invocation hook: %w", err)
			yield(session.Event{}, invocationErr)
			return
		}
		defer func() {
			_ = runAfterInvocationHooks(ctx, r.cfg.Hooks, agent.InvocationHookResult{
				InvocationHook: invHook,
				Error:          invocationErr,
			})
		}()

		completed, err := r.runAgentWithPersistence(ctx, sess.Ref, invID, runAgent, invCtx, observer, yield)
		if err != nil {
			invocationErr = err
			yield(session.Event{}, err)
			return
		}
		if !completed {
			return
		}
		drainObserverBridge(observer, yield)
	}
}

func routeSandbox(ctx context.Context, factory sandbox.Factory, req sandbox.RouteRequest) (sandbox.Config, error) {
	if router, ok := factory.(sandbox.Router); ok {
		cfg, err := router.Route(ctx, req)
		if err != nil {
			return sandbox.Config{}, err
		}
		if cfg.BackendName == "" {
			return sandbox.Config{}, fmt.Errorf("sandbox router returned empty backend")
		}
		return cfg, nil
	}
	name := strings.TrimSpace(req.RequestedBackend)
	if name != "" {
		for _, backend := range req.Available {
			if backend.Name == name {
				return sandbox.Config{
					BackendName: backend.Name,
					RootDir:     req.WorkspaceRoot,
					Constraints: req.Constraints,
				}, nil
			}
		}
		return sandbox.Config{}, fmt.Errorf("requested backend %q is not available", name)
	}
	if len(req.Available) == 0 {
		return sandbox.Config{}, fmt.Errorf("no sandbox backend available")
	}
	return sandbox.Config{
		BackendName: req.Available[0].Name,
		RootDir:     req.WorkspaceRoot,
		Constraints: req.Constraints,
	}, nil
}

func sandboxBackendFromMetadata(metadata map[string]any) string {
	for _, key := range []string{"sandbox_backend", "sandbox_backend_name", "sandbox.backend"} {
		if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// RunRequest is the input to Runner.Run.
type RunRequest struct {
	SessionRef  session.Ref
	UserMessage model.Message
	Branch      string
	Metadata    map[string]any
}

// isNotFound checks if an error indicates a not-found condition.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "not found")
}

func (r *Runner) assembleSystemPrompt(ctx context.Context) (string, error) {
	var skills []skill.Bundle
	var baseParts []string
	if base := strings.TrimSpace(r.cfg.SystemPrompt); base != "" {
		baseParts = append(baseParts, base)
	}
	if r.cfg.Skills != nil {
		listed, err := r.cfg.Skills.List(ctx)
		if err != nil {
			return "", fmt.Errorf("runner: list skills: %w", err)
		}
		skills = listed
	}
	if r.cfg.Plugins != nil {
		plugins, err := r.cfg.Plugins.List(ctx)
		if err != nil {
			return "", fmt.Errorf("runner: list plugins: %w", err)
		}
		for _, one := range plugins {
			skills = append(skills, one.Skills...)
			if len(one.Skills) == 0 {
				skills = append(skills, one.Runtime.Skills...)
			}
			if prompt := strings.TrimSpace(one.Runtime.SystemPrompt); prompt != "" {
				baseParts = append(baseParts, prompt)
			} else if prompt := strings.TrimSpace(one.Manifest.Contributions.SystemPrompt); prompt != "" {
				baseParts = append(baseParts, prompt)
			}
		}
	}
	assembler := r.cfg.Prompt
	if assembler == nil {
		assembler = prompt.DefaultAssembler()
	}
	text, err := assembler.Assemble(ctx, prompt.Request{
		Base:   strings.Join(baseParts, "\n\n"),
		Skills: skills,
	})
	if err != nil {
		return "", fmt.Errorf("runner: assemble prompt: %w", err)
	}
	return strings.TrimSpace(text), nil
}

func (r *Runner) resolveInvocationTools(ctx context.Context, runAgent agent.Agent) ([]tool.Tool, []mcp.Client, error) {
	tools, err := r.resolveConfiguredTools(ctx, runAgent)
	if err != nil {
		return nil, nil, err
	}
	mcpTools, clients, err := r.resolvePluginMCPTools(ctx)
	if err != nil {
		return nil, nil, err
	}
	tools = append(tools, mcpTools...)
	return tools, clients, nil
}

func (r *Runner) resolveConfiguredTools(ctx context.Context, runAgent agent.Agent) ([]tool.Tool, error) {
	tn, ok := runAgent.(interface{ ToolNames() []string })
	if !ok || r.cfg.ToolRegistry == nil {
		return nil, nil
	}
	var tools []tool.Tool
	for _, name := range tn.ToolNames() {
		t, ok, err := r.cfg.ToolRegistry.Lookup(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("runner: lookup tool %s: %w", name, err)
		}
		if ok {
			tools = append(tools, t)
		}
	}
	return tools, nil
}

func (r *Runner) resolvePluginMCPTools(ctx context.Context) ([]tool.Tool, []mcp.Client, error) {
	if r.cfg.Plugins == nil {
		return nil, nil, nil
	}
	plugins, err := r.cfg.Plugins.List(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("runner: list plugins: %w", err)
	}
	factory := r.cfg.MCPClientFactory
	if factory == nil {
		factory = mcp.DefaultClientFactory{}
	}
	var (
		tools   []tool.Tool
		clients []mcp.Client
	)
	for _, plugin := range plugins {
		servers := plugin.MCPServers
		if len(servers) == 0 {
			servers = plugin.Runtime.MCPServers
		}
		for _, server := range servers {
			server.Name = strings.TrimSpace(server.Name)
			if server.Name == "" {
				closeMCPClients(clients)
				return nil, nil, fmt.Errorf("runner: plugin %s declares MCP server without name", plugin.Manifest.Name)
			}
			client, err := factory.NewClient(ctx, server, plugin.Root)
			if err != nil {
				closeMCPClients(clients)
				return nil, nil, fmt.Errorf("runner: open plugin MCP server %s: %w", server.Name, err)
			}
			toolset := mcp.NewToolset(mcp.Config{
				Name:           server.Name,
				Client:         client,
				ToolNamePrefix: server.Name + ".",
			})
			loaded, err := toolset.Tools(ctx)
			if err != nil {
				_ = client.Close()
				closeMCPClients(clients)
				return nil, nil, fmt.Errorf("runner: list plugin MCP tools %s: %w", server.Name, err)
			}
			clients = append(clients, client)
			tools = append(tools, loaded...)
		}
	}
	return tools, clients, nil
}

func closeMCPClients(clients []mcp.Client) {
	for i := len(clients) - 1; i >= 0; i-- {
		if clients[i] != nil {
			_ = clients[i].Close()
		}
	}
}

// invocationContext implements agent.InvocationContext.
type invocationContext struct {
	context.Context
	agent         agent.Agent
	session       session.Session
	invocationID  string
	branch        string
	userMessage   model.Message
	priorMessages []model.Message
	runConfig     *agent.RunConfig
	hooks         []agent.Hook
	tracer        trace.Tracer
	ended         atomic.Bool
}

func (c *invocationContext) Agent() agent.Agent         { return c.agent }
func (c *invocationContext) Session() session.Session   { return c.session }
func (c *invocationContext) InvocationID() string       { return c.invocationID }
func (c *invocationContext) Branch() string             { return c.branch }
func (c *invocationContext) UserMessage() model.Message { return c.userMessage }
func (c *invocationContext) PriorMessages() []model.Message {
	cp := make([]model.Message, len(c.priorMessages))
	copy(cp, c.priorMessages)
	return cp
}
func (c *invocationContext) RunConfig() *agent.RunConfig { return c.runConfig }
func (c *invocationContext) Hooks() []agent.Hook {
	return append([]agent.Hook(nil), c.hooks...)
}
func (c *invocationContext) Tracer() trace.Tracer { return c.tracer }
func (c *invocationContext) EndInvocation()       { c.ended.Store(true) }
func (c *invocationContext) Ended() bool          { return c.ended.Load() }
