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
	"github.com/OnslaughtSnail/caelis/policy"
	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/session"
	"github.com/OnslaughtSnail/caelis/skill"
	"github.com/OnslaughtSnail/caelis/tool"
)

// Config holds dependencies for creating a Runner.
type Config struct {
	AppName       string
	Agent         agent.Agent
	Sessions      session.Service
	ModelRegistry model.Registry
	ToolRegistry  tool.Registry
	Sandbox       sandbox.Factory
	Policy        policy.Engine
	Skills        skill.Registry
	Approver      agent.ApprovalRequester
	SystemPrompt  string    // assembled system prompt
	Compactor     Compactor // optional compaction engine
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
		// Load session.
		sess, err := r.cfg.Sessions.Get(ctx, req.SessionRef)
		if err != nil {
			if !isNotFound(err) {
				yield(session.Event{}, fmt.Errorf("runner: load session: %w", err))
				return
			}
			sess, err = r.cfg.Sessions.Create(ctx, session.CreateRequest{
				AppName:      req.SessionRef.AppName,
				UserID:       req.SessionRef.UserID,
				WorkspaceKey: req.SessionRef.WorkspaceKey,
			})
			if err != nil {
				yield(session.Event{}, fmt.Errorf("runner: create session: %w", err))
				return
			}
		}

		invID := fmt.Sprintf("inv-%d", time.Now().UnixNano())
		runAgent := r.cfg.Agent

		// Prepare agent dependencies via Prepareable interface.
		if p, ok := r.cfg.Agent.(agent.Prepareable); ok {
			prepReq := agent.PrepareRequest{}

			// Resolve LLM from registry.
			if mr, ok := r.cfg.Agent.(interface{ ModelRef() model.Ref }); ok && r.cfg.ModelRegistry != nil {
				llm, _, err := r.cfg.ModelRegistry.Resolve(ctx, mr.ModelRef())
				if err != nil {
					yield(session.Event{}, fmt.Errorf("runner: resolve model: %w", err))
					return
				}
				prepReq.LLM = llm
			}

			// Wire tool context with sandbox.
			tc := &toolContext{
				Context:       ctx,
				sessionRef:    sess.Ref.String(),
				invocationID:  invID,
				agentName:     runAgent.Name(),
				workspaceRoot: sess.Workspace.Root,
			}
			var invBackend sandbox.Backend
			if r.cfg.Sandbox != nil {
				backends, _ := r.cfg.Sandbox.Available(ctx)
				if len(backends) > 0 {
					b, err := r.cfg.Sandbox.Create(ctx, sandbox.Config{
						BackendName: backends[0].Name,
					})
					if err == nil {
						invBackend = b
						tc.backend = b
						fs, _ := b.FileSystem(ctx, sandbox.Constraints{})
						tc.fs = fs
					}
				}
			}
			prepReq.ToolContext = tc

			// Resolve tools from registry.
			if tn, ok := r.cfg.Agent.(interface{ ToolNames() []string }); ok && r.cfg.ToolRegistry != nil {
				var tools []tool.Tool
				for _, name := range tn.ToolNames() {
					t, ok, err := r.cfg.ToolRegistry.Lookup(ctx, name)
					if err != nil {
						yield(session.Event{}, fmt.Errorf("runner: lookup tool %s: %w", name, err))
						return
					}
					if ok {
						tools = append(tools, t)
					}
				}
				// Augment + wrap tools with per-invocation task manager.
				invTaskMgr := NewTaskManager(invBackend)
				if len(tools) > 0 {
					tools = AugmentTools(tools, invTaskMgr)
					prepReq.Tools = WrapTools(tools, r.cfg.Policy, r.cfg.Approver, nil)
				}
			}

			runAgent = p.Prepare(prepReq)
		}

		// Load prior session events and rebuild model context.
		priorEvts, err := r.cfg.Sessions.Events(ctx, session.EventsRequest{
			SessionRef: sess.Ref,
		})
		if err != nil {
			yield(session.Event{}, fmt.Errorf("runner: load events: %w", err))
			return
		}
		priorMessages := session.ModelContextFromEvents(priorEvts)

		// Compact if needed using the configured compactor.
		if r.cfg.Compactor != nil {
			budget := DefaultCompactionPolicy().MaxContextTokens
			if ok, reason := r.cfg.Compactor.ShouldCompact(priorMessages, budget); ok {
				compactedMsgs, compactionEvt, didCompact := r.cfg.Compactor.Compact(ctx, priorMessages, budget)
				if didCompact {
					priorMessages = compactedMsgs
					if compactionEvt != nil {
						if _, err := r.cfg.Sessions.AppendEvent(ctx, sess.Ref, *compactionEvt); err != nil {
							yield(session.Event{}, fmt.Errorf("runner: persist compaction event: %w", err))
							return
						}
					}
				}
				_ = reason
			}
		} else {
			// Fallback: heuristic compaction.
			compacted, reason := NeedsCompaction(priorMessages, DefaultCompactionPolicy())
			if compacted {
				compactedMsgs, ok, summaryText := CompactModelContext(priorMessages, int(float64(DefaultCompactionPolicy().MaxContextTokens)*0.6))
				if ok {
					priorMessages = compactedMsgs
					if _, err := r.cfg.Sessions.AppendEvent(ctx, sess.Ref, session.Event{
						Kind:       session.EventKindCompaction,
						Visibility: session.VisibilityCanonical,
						CompactionPayload: &session.CompactionPayload{
							Reason:      reason,
							Previous:    len(priorEvts),
							SummaryText: summaryText,
						},
					}); err != nil {
						yield(session.Event{}, fmt.Errorf("runner: persist compaction event: %w", err))
						return
					}
				}
			}
		}

		// Prepend system prompt if configured.
		if r.cfg.SystemPrompt != "" {
			sysMsg := model.Message{
				Role:    model.RoleSystem,
				Content: []model.Part{{Text: r.cfg.SystemPrompt}},
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
		}

		// Run the agent and persist non-transient events.
		for evt, err := range runAgent.Run(invCtx) {
			if err != nil {
				yield(session.Event{}, fmt.Errorf("runner: agent error: %w", err))
				return
			}

			evt.SessionRef = sess.Ref
			evt.RunID = invID

			// Filter transient events from persistence.
			if evt.Visibility.IsTransient() {
				if !yield(evt, nil) {
					return
				}
				continue
			}

			persisted, err := r.cfg.Sessions.AppendEvent(ctx, sess.Ref, evt)
			if err != nil {
				yield(session.Event{}, fmt.Errorf("runner: persist event: %w", err))
				return
			}

			if !yield(persisted, nil) {
				return
			}
		}
	}
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
func (c *invocationContext) EndInvocation()              { c.ended.Store(true) }
func (c *invocationContext) Ended() bool                 { return c.ended.Load() }
