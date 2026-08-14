package subagent

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/authentication"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpcleanup"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acputil"
	"github.com/caelis-labs/caelis/protocol/acp/client"
)

// LoadHistory starts a short-lived ACP transport and calls session/load for an
// existing provider-owned child Session. It never resumes the child, applies
// execution configuration, or closes the durable Session.
func (r *Runner) LoadHistory(ctx context.Context, raw tasksubagent.HistoryRequest) (session.LoadedSession, error) {
	if r == nil {
		return session.LoadedSession{}, fmt.Errorf("internal/acpagentbridge/subagent: runner is unavailable")
	}
	if ctx == nil {
		return session.LoadedSession{}, fmt.Errorf("internal/acpagentbridge/subagent: history context is required")
	}
	req := tasksubagent.CloneHistoryRequest(raw)
	anchor := req.Anchor
	reconnect := req.Reconnect
	spawn := reconnect.Spawn
	if strings.TrimSpace(anchor.TaskID) == "" || strings.TrimSpace(anchor.SessionID) == "" {
		return session.LoadedSession{}, fmt.Errorf("internal/acpagentbridge/subagent: child history anchor is incomplete")
	}
	if strings.TrimSpace(spawn.TaskID) != strings.TrimSpace(anchor.TaskID) || strings.TrimSpace(spawn.SessionRef.SessionID) == "" {
		return session.LoadedSession{}, fmt.Errorf("internal/acpagentbridge/subagent: child history identity does not match its durable Task")
	}
	if err := delegation.ValidateTarget(reconnect.Target); err != nil {
		return session.LoadedSession{}, err
	}
	cfg, err := r.resolveSpawnConfig(ctx, spawn, delegation.TargetRequest{Target: reconnect.Target})
	if err != nil {
		return session.LoadedSession{}, err
	}

	collector := newHistoryCollector(r, anchor, cfg.Name)
	launchEnv := maps.Clone(cfg.Env)
	if strings.EqualFold(strings.TrimSpace(cfg.Name), "self") {
		if launchEnv == nil {
			launchEnv = map[string]string{}
		}
		launchEnv["SDK_ACP_ENABLE_SPAWN"] = "0"
		launchEnv["SDK_ACP_CHILD_NO_SPAWN"] = "1"
	}
	acpClient, err := client.Start(ctx, client.Config{
		Command: cfg.Command, Args: append([]string(nil), cfg.Args...), Env: launchEnv,
		WorkDir: pickWorkDir(cfg.WorkDir, spawn.CWD), ClientInfo: r.clientInfo,
		OnUpdate: collector.observe,
	})
	if err != nil {
		return session.LoadedSession{}, err
	}
	defer func() {
		_ = acpcleanup.CloseClient(context.WithoutCancel(ctx), acpClient)
	}()
	initialize, err := acpClient.Initialize(ctx)
	if err != nil {
		return session.LoadedSession{}, err
	}
	if !initialize.AgentCapabilities.LoadSession {
		return session.LoadedSession{}, fmt.Errorf("internal/acpagentbridge/subagent: child Agent %q does not support session/load", cfg.Name)
	}
	methods := authentication.Methods(initialize)
	if _, err := authentication.RecoverConfiguredCall(
		ctx,
		acpClient,
		methods,
		cfg.Name,
		controlagents.NormalizeAuthentication(cfg.Authentication),
		func(loadCtx context.Context, activeClient *client.Client) (client.LoadSessionResponse, error) {
			return activeClient.LoadSession(loadCtx, strings.TrimSpace(anchor.SessionID), strings.TrimSpace(spawn.CWD), nil)
		},
	); err != nil {
		return session.LoadedSession{}, err
	}
	if err := collector.errSnapshot(); err != nil {
		return session.LoadedSession{}, err
	}
	return session.LoadedSession{
		Session: session.Session{
			SessionRef: session.SessionRef{SessionID: strings.TrimSpace(anchor.SessionID)},
			CWD:        strings.TrimSpace(spawn.CWD),
		},
		Events: collector.eventsSnapshot(),
	}, nil
}

type historyCollector struct {
	mu sync.Mutex

	runner            *Runner
	run               childRun
	turnSeq           int64
	lastUpdateType    string
	lastUserMessageID string
	events            []*session.Event
	err               error
}

func newHistoryCollector(runner *Runner, anchor delegation.Anchor, agentName string) *historyCollector {
	return &historyCollector{
		runner: runner,
		run: childRun{
			anchor: anchor, taskID: strings.TrimSpace(anchor.TaskID), agentName: strings.TrimSpace(agentName),
		},
	}
}

func (c *historyCollector) observe(env client.UpdateEnvelope) {
	if c == nil || c.runner == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	wantSessionID := strings.TrimSpace(c.run.anchor.SessionID)
	gotSessionID := strings.TrimSpace(env.SessionID)
	if gotSessionID != wantSessionID {
		if c.err == nil {
			c.err = fmt.Errorf(
				"internal/acpagentbridge/subagent: session/load update belongs to Session %q, want %q",
				gotSessionID,
				wantSessionID,
			)
		}
		return
	}
	env.Update = acputil.StripTerminalConsoleFenceUpdate(env.Update)
	updateType := historyUpdateType(env.Update)
	if updateType == client.UpdateUserMessage {
		messageID := ""
		if chunk, ok := env.Update.(client.ContentChunk); ok {
			messageID = strings.TrimSpace(chunk.MessageID)
		}
		if c.lastUpdateType != client.UpdateUserMessage || messageID != "" && messageID != c.lastUserMessageID {
			c.turnSeq++
		}
		c.lastUserMessageID = messageID
	}
	if c.turnSeq <= 0 {
		c.turnSeq = 1
	}
	event := c.run.acpUpdateEvent(env, c.runner.clock())
	if event == nil {
		c.lastUpdateType = updateType
		return
	}
	if updateType == client.UpdateUserMessage {
		markSubagentInputEvent(event)
	}
	if event.Scope == nil {
		event.Scope = &session.EventScope{}
	}
	event.Scope.TurnID = fmt.Sprintf("%s:%d", strings.TrimSpace(c.run.taskID), c.turnSeq)
	event.SessionID = strings.TrimSpace(c.run.anchor.SessionID)
	event.ID = fmt.Sprintf("subagent-load:%s:%d", strings.TrimSpace(c.run.taskID), len(c.events)+1)
	c.events = append(c.events, event)
	c.lastUpdateType = updateType
}

func historyUpdateType(update client.Update) string {
	switch typed := update.(type) {
	case client.ContentChunk:
		return strings.TrimSpace(typed.SessionUpdate)
	case client.ToolCall:
		return strings.TrimSpace(typed.SessionUpdate)
	case client.ToolCallUpdate:
		return strings.TrimSpace(typed.SessionUpdate)
	case client.PlanUpdate:
		return strings.TrimSpace(typed.SessionUpdate)
	default:
		return ""
	}
}

func (c *historyCollector) eventsSnapshot() []*session.Event {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*session.Event, 0, len(c.events))
	for _, event := range c.events {
		out = append(out, session.CloneEvent(event))
	}
	return out
}

func (c *historyCollector) errSnapshot() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

var _ tasksubagent.HistoryRunner = (*Runner)(nil)
