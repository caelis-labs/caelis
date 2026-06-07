package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"sync"

	"github.com/OnslaughtSnail/caelis/acp"
	"github.com/OnslaughtSnail/caelis/acp/client"
	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/session"
)

// ACPClient is the narrow client surface an ACP-backed agent needs.
type ACPClient = client.ACPClient

// ACPReusableClient can load an existing external ACP session for continuation.
type ACPReusableClient = client.ACPReusableClient

// ACPClientCallbacks are installed by ACPAgent so the transport can stream
// remote updates and permission requests back into Layer 4 contracts.
type ACPClientCallbacks = client.ACPClientCallbacks

// ACPClientFactory creates one ACP client per agent run.
type ACPClientFactory = client.ACPClientFactory

// ProcessFactoryConfig configures a process-backed ACP client factory.
type ProcessFactoryConfig = client.ProcessFactoryConfig

// ProcessFactory starts an ACP agent process through acp/client.
type ProcessFactory = client.ProcessFactory

// Config configures an ACP-backed remote agent.
type Config struct {
	Name              string
	Description       string
	ClientFactory     ACPClientFactory
	ApprovalRequester agent.ApprovalRequester
}

// NewACP creates an agent.Agent backed by an external ACP agent.
func NewACP(cfg Config) agent.Agent {
	return &ACPAgent{
		name:              strings.TrimSpace(cfg.Name),
		description:       strings.TrimSpace(cfg.Description),
		factory:           cfg.ClientFactory,
		approvalRequester: cfg.ApprovalRequester,
		remoteSessions:    map[string]string{},
	}
}

// ACPAgent adapts an external ACP agent to the Layer 4 agent.Agent contract.
type ACPAgent struct {
	name              string
	description       string
	factory           ACPClientFactory
	approvalRequester agent.ApprovalRequester

	mu             sync.Mutex
	remoteSessions map[string]string
}

func (a *ACPAgent) Name() string {
	if a == nil || a.name == "" {
		return "acp-agent"
	}
	return a.name
}

func (a *ACPAgent) Description() string {
	if a == nil {
		return ""
	}
	return a.description
}

func (a *ACPAgent) SubAgents() []agent.Agent { return nil }

func (a *ACPAgent) FindAgent(string) agent.Agent { return nil }

func (a *ACPAgent) Run(inv agent.InvocationContext) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		if a == nil || a.factory == nil {
			yield(session.Event{}, fmt.Errorf("agent/remote: ACP client factory is required"))
			return
		}
		runCtx, cancel := context.WithCancel(inv)
		defer cancel()

		events := make(chan remoteEvent, 32)
		callbacks := ACPClientCallbacks{
			OnUpdate: func(env client.UpdateEnvelope) {
				event, err := a.normalizeUpdate(inv.Session().Ref, env)
				if event == nil && err == nil {
					return
				}
				select {
				case events <- remoteEvent{event: event, err: err}:
				case <-runCtx.Done():
				}
			},
			OnPermissionRequest: func(ctx context.Context, req client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
				return a.requestPermission(ctx, inv, req)
			},
		}

		remoteClient, err := a.factory.Start(runCtx, callbacks)
		if err != nil {
			yield(session.Event{}, err)
			return
		}
		defer remoteClient.Close()
		remoteState := &acpRunState{cancel: remoteClient.Cancel}

		done := make(chan error, 1)
		go func() {
			defer close(events)
			if _, err := remoteClient.Initialize(runCtx); err != nil {
				done <- err
				return
			}
			remoteSessionID, err := a.ensureRemoteSession(runCtx, remoteClient, inv.Session())
			if err != nil {
				done <- err
				return
			}
			remoteState.setSessionID(remoteSessionID)
			go func() {
				<-runCtx.Done()
				remoteState.cancelRemote(context.WithoutCancel(runCtx))
			}()
			_, err = remoteClient.PromptText(runCtx, remoteState.sessionID(), userText(inv))
			if runCtx.Err() != nil {
				remoteState.cancelRemote(context.WithoutCancel(runCtx))
			}
			done <- err
		}()

		for item := range events {
			event := session.Event{}
			if item.event != nil {
				event = *item.event
			}
			if !yield(event, item.err) {
				cancel()
				remoteState.cancelRemote(context.WithoutCancel(runCtx))
				return
			}
		}
		if err := <-done; err != nil {
			yield(session.Event{}, err)
		}
	}
}

func (a *ACPAgent) ensureRemoteSession(ctx context.Context, remoteClient ACPClient, local session.Session) (string, error) {
	key := localSessionKey(local)
	if key != "" {
		if remoteSessionID := a.lookupRemoteSession(key); remoteSessionID != "" {
			reusable, ok := remoteClient.(ACPReusableClient)
			if !ok {
				return "", fmt.Errorf("agent/remote: ACP client cannot load existing session %q", remoteSessionID)
			}
			if _, err := reusable.LoadSession(ctx, acp.LoadSessionRequest{
				SessionID: remoteSessionID,
			}); err != nil {
				return "", err
			}
			return remoteSessionID, nil
		}
	}
	newSession, err := remoteClient.NewSession(ctx, acp.NewSessionRequest{
		CWD: local.Workspace.Root,
	})
	if err != nil {
		return "", err
	}
	remoteSessionID := strings.TrimSpace(newSession.SessionID)
	if key != "" && remoteSessionID != "" {
		a.rememberRemoteSession(key, remoteSessionID)
	}
	return remoteSessionID, nil
}

func (a *ACPAgent) lookupRemoteSession(key string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.TrimSpace(a.remoteSessions[key])
}

func (a *ACPAgent) rememberRemoteSession(key string, remoteSessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.remoteSessions == nil {
		a.remoteSessions = map[string]string{}
	}
	a.remoteSessions[key] = strings.TrimSpace(remoteSessionID)
}

func localSessionKey(local session.Session) string {
	if strings.TrimSpace(local.Ref.SessionID) == "" {
		return ""
	}
	return local.Ref.String()
}

type remoteEvent struct {
	event *session.Event
	err   error
}

type acpRunState struct {
	mu      sync.Mutex
	once    sync.Once
	session string
	cancel  func(context.Context, string) error
}

func (s *acpRunState) setSessionID(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session = strings.TrimSpace(sessionID)
}

func (s *acpRunState) sessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.session
}

func (s *acpRunState) cancelRemote(ctx context.Context) {
	if s == nil || s.cancel == nil {
		return
	}
	s.once.Do(func() {
		sessionID := s.sessionID()
		if sessionID == "" {
			return
		}
		_ = s.cancel(ctx, sessionID)
	})
}

func (a *ACPAgent) normalizeUpdate(local session.Ref, env client.UpdateEnvelope) (*session.Event, error) {
	raw, err := json.Marshal(env.Update)
	if err != nil {
		return nil, err
	}
	event, err := acp.NormalizeExternalUpdateJSON(local.SessionID, raw)
	if event == nil || err != nil {
		return nil, err
	}
	event.SessionRef = local
	event.Actor.Scope = "participant"
	event.Actor.Source = "acp_agent"
	event.Actor.ParticipantID = a.Name()
	if event.ProviderMeta == nil {
		event.ProviderMeta = map[string]any{}
	}
	if remoteSessionID := strings.TrimSpace(env.SessionID); remoteSessionID != "" {
		event.ProviderMeta["acp_session_id"] = remoteSessionID
	}
	event.ProviderMeta["acp_agent"] = a.Name()
	return event, nil
}

func (a *ACPAgent) requestPermission(ctx context.Context, inv agent.InvocationContext, req client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
	if a.approvalRequester == nil {
		return client.PermissionSelectedOutcome(client.SelectPermissionOptionID(req.Options, false)), nil
	}
	resp, err := a.approvalRequester.RequestApproval(ctx, agent.ApprovalRequest{
		ToolName: toolCallName(req.ToolCall),
		CallID:   strings.TrimSpace(req.ToolCall.ToolCallID),
		Args:     rawInputMap(req.ToolCall.RawInput),
		Reason:   "external ACP agent requested permission",
		Session:  inv.Session(),
		RunID:    inv.InvocationID(),
	})
	if err != nil {
		return client.RequestPermissionResponse{}, err
	}
	return client.PermissionSelectedOutcome(client.SelectPermissionOptionID(req.Options, resp.Approved)), nil
}

func userText(inv agent.InvocationContext) string {
	var b strings.Builder
	for _, part := range inv.UserMessage().Content {
		if strings.TrimSpace(part.Text) == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.TrimSpace(part.Text))
	}
	return b.String()
}

func toolCallName(call acp.ToolCallUpdate) string {
	if strings.TrimSpace(call.Title) != "" {
		return strings.TrimSpace(call.Title)
	}
	if strings.TrimSpace(call.Kind) != "" {
		return strings.TrimSpace(call.Kind)
	}
	return strings.TrimSpace(call.ToolCallID)
}

func rawInputMap(raw any) map[string]any {
	if raw == nil {
		return nil
	}
	if m, ok := raw.(map[string]any); ok {
		return m
	}
	return map[string]any{"raw_input": raw}
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
