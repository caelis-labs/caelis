package gatewayapp

import (
	"context"
	"fmt"
	"strings"
	"sync"

	agentmessage "github.com/caelis-labs/caelis/agent-sdk/message"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
)

// hostedChildMailboxFunc routes one child-originated Agent message through the
// parent Session Runtime. Session Runtime instances receive this Host-owned
// callback because they must not own the Runtime registry.
type hostedChildMailboxFunc func(context.Context, session.SessionRef, agentmessage.Request) (agentmessage.Response, error)

// hostedChildMailboxRouter is the focused process-owned bridge from detached
// Session Runtimes back to the Runtime registry. It is constructed before the
// registry and bound once afterwards, so neither the registry nor an assembled
// Runtime retains a closure over the complete Host Stack.
type hostedChildMailboxRouter struct {
	mu       sync.RWMutex
	runtimes *sessionRuntimeRegistry
}

func (r *hostedChildMailboxRouter) bind(runtimes *sessionRuntimeRegistry) error {
	if r == nil || runtimes == nil {
		return fmt.Errorf("gatewayapp: hosted child mailbox registry is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runtimes != nil && r.runtimes != runtimes {
		return fmt.Errorf("gatewayapp: hosted child mailbox registry is already bound")
	}
	r.runtimes = runtimes
	return nil
}

func (r *hostedChildMailboxRouter) deliver(ctx context.Context, parentRef session.SessionRef, req agentmessage.Request) (agentmessage.Response, error) {
	if r == nil {
		return agentmessage.Response{}, fmt.Errorf("gatewayapp: hosted child mailbox is unavailable")
	}
	r.mu.RLock()
	runtimes := r.runtimes
	r.mu.RUnlock()
	if runtimes == nil {
		return agentmessage.Response{}, fmt.Errorf("gatewayapp: hosted child mailbox is unavailable")
	}
	parentRef = session.NormalizeSessionRef(parentRef)
	if strings.TrimSpace(parentRef.SessionID) == "" {
		return agentmessage.Response{}, fmt.Errorf("gatewayapp: parent Session is required")
	}
	runtime, _, release, err := runtimes.acquireControlRuntime(ctx, parentRef.SessionID, true)
	if err != nil {
		return agentmessage.Response{}, err
	}
	if release != nil {
		defer func() {
			_ = release(context.WithoutCancel(ctx))
		}()
	}
	if runtime == nil || runtime.instance == nil || runtime.instance.engine == nil {
		return agentmessage.Response{}, fmt.Errorf("gatewayapp: parent Agent message runtime is unavailable")
	}
	return runtime.instance.engine.SendAgentMessage(ctx, parentRef, req)
}

type hostedChildMessageSender struct {
	deliver  hostedChildMailboxFunc
	sessions session.Service
	parent   session.Session
	child    session.Session
}

func (s *runtimeComposition) hostedChildMessageSender(active session.Session) agentmessage.Sender {
	if s == nil || !sessionvisibility.IsSpawnedSubagentSession(active) {
		return nil
	}
	deliver := s.childMailbox()
	if deliver == nil {
		return nil
	}
	parentID := hostedChildMetadataString(active.Metadata, sessionvisibility.MetadataSystemManagedParent)
	taskID := hostedChildMetadataString(active.Metadata, sessionvisibility.MetadataSystemManagedTask)
	if parentID == "" || taskID == "" {
		return nil
	}
	return hostedChildMessageSender{
		deliver:  deliver,
		sessions: s.Sessions,
		child:    session.CloneSession(active),
	}
}

func (s *runtimeComposition) childMailbox() hostedChildMailboxFunc {
	if s == nil {
		return nil
	}
	return s.hostedChildMailbox
}

func (s hostedChildMessageSender) SendMessage(ctx context.Context, raw agentmessage.Request) (agentmessage.Response, error) {
	if s.deliver == nil {
		return agentmessage.Response{}, fmt.Errorf("gatewayapp: hosted child mailbox is unavailable")
	}
	parent, err := s.resolveParent(ctx)
	if err != nil {
		return agentmessage.Response{}, err
	}
	binding, ok := hostedChildParticipant(parent, s.child)
	if !ok {
		return agentmessage.Response{}, fmt.Errorf("gatewayapp: managed child Task does not match its parent participant binding")
	}
	handle := hostedChildHandle(binding)
	req := agentmessage.NormalizeRequest(raw)
	if taskapi.NormalizeHandle(req.To) != "" && taskapi.NormalizeHandle(req.To) == taskapi.NormalizeHandle(handle) {
		return agentmessage.Response{}, fmt.Errorf("gatewayapp: subagent %q cannot message itself", handle)
	}
	role := binding.Role
	if role == "" {
		role = session.ParticipantRoleDelegated
	}
	req.From = session.ActorRef{
		Kind: session.ActorKindParticipant,
		ID:   strings.TrimSpace(binding.ID),
		Name: "@" + strings.TrimPrefix(handle, "@"),
		Role: string(role),
	}
	req.Scope = &session.EventScope{
		Source: "subagent_message",
		Participant: session.ParticipantRef{
			ID:           strings.TrimSpace(binding.ID),
			Kind:         session.ParticipantKindSubagent,
			Role:         role,
			DelegationID: strings.TrimSpace(binding.DelegationID),
		},
	}
	return s.deliver(ctx, parent.SessionRef, req)
}

func (s hostedChildMessageSender) resolveParent(ctx context.Context) (session.Session, error) {
	if strings.TrimSpace(s.parent.SessionID) != "" {
		return s.parent, nil
	}
	parentID := hostedChildMetadataString(s.child.Metadata, sessionvisibility.MetadataSystemManagedParent)
	if parentID == "" || s.sessions == nil {
		return session.Session{}, fmt.Errorf("gatewayapp: parent Session is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	parent, err := s.sessions.Session(ctx, session.SessionRef{SessionID: parentID})
	if err != nil {
		return session.Session{}, fmt.Errorf("gatewayapp: inspect managed child parent: %w", err)
	}
	return parent, nil
}

func hostedChildParticipant(parent session.Session, child session.Session) (session.ParticipantBinding, bool) {
	childSessionID := strings.TrimSpace(child.SessionID)
	taskID := hostedChildMetadataString(child.Metadata, sessionvisibility.MetadataSystemManagedTask)
	if childSessionID == "" || taskID == "" {
		return session.ParticipantBinding{}, false
	}
	for _, binding := range parent.Participants {
		if binding.Kind != session.ParticipantKindSubagent {
			continue
		}
		if strings.TrimSpace(binding.SessionID) == childSessionID &&
			strings.TrimSpace(binding.DelegationID) == taskID &&
			strings.TrimSpace(binding.ID) != "" {
			return binding, true
		}
	}
	return session.ParticipantBinding{}, false
}

func hostedChildHandle(binding session.ParticipantBinding) string {
	handle := strings.TrimSpace(binding.Label)
	if handle == "" {
		handle = strings.TrimSpace(binding.DelegationID)
	}
	return strings.TrimPrefix(handle, "@")
}

func hostedChildMetadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}
