package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// hostedChildInputFunc routes ordinary child-originated input through the
// parent Session Runtime. The private child identity is revalidated against the
// parent's durable participant topology on every call.
type hostedChildInputFunc func(
	context.Context,
	session.SessionRef,
	session.SessionRef,
	string,
	agent.AgentInput,
) error

// hostedChildInputRouter is the focused process-owned bridge from detached
// Session Runtimes back to the Runtime registry. It is constructed before the
// registry and bound once afterwards, so assembled Runtimes do not own the
// registry itself.
type hostedChildInputRouter struct {
	mu       sync.RWMutex
	runtimes *sessionRuntimeRegistry
}

func (r *hostedChildInputRouter) bind(runtimes *sessionRuntimeRegistry) error {
	if r == nil || runtimes == nil {
		return fmt.Errorf("gatewayapp: hosted child input registry is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runtimes != nil && r.runtimes != runtimes {
		return fmt.Errorf("gatewayapp: hosted child input registry is already bound")
	}
	r.runtimes = runtimes
	return nil
}

func (r *hostedChildInputRouter) route(
	ctx context.Context,
	parentRef session.SessionRef,
	childRef session.SessionRef,
	delegationID string,
	raw agent.AgentInput,
) error {
	if r == nil {
		return fmt.Errorf("gatewayapp: hosted child input route is unavailable")
	}
	r.mu.RLock()
	runtimes := r.runtimes
	r.mu.RUnlock()
	if runtimes == nil {
		return fmt.Errorf("gatewayapp: hosted child input route is unavailable")
	}
	parentRef = session.NormalizeSessionRef(parentRef)
	childRef = session.NormalizeSessionRef(childRef)
	delegationID = strings.TrimSpace(delegationID)
	input := agent.CloneAgentInput(raw)
	if parentRef.SessionID == "" || childRef.SessionID == "" || delegationID == "" || input.Target == "" ||
		(input.Input == "" && len(input.ContentParts) == 0) {
		return errorcode.New(errorcode.InvalidArgument, "gatewayapp: hosted child input requires parent, child, delegation, target, and content")
	}
	parentRuntime, active, release, err := runtimes.acquireControlRuntime(ctx, parentRef.SessionID, true)
	if err != nil {
		return err
	}
	if release != nil {
		defer func() { _ = release(context.WithoutCancel(ctx)) }()
	}
	if parentRuntime == nil || parentRuntime.instance == nil || parentRuntime.instance.engine == nil {
		return fmt.Errorf("gatewayapp: parent Agent input runtime is unavailable")
	}
	binding, ok := hostedChildParticipantByIdentity(active, childRef.SessionID, delegationID)
	if !ok {
		return errorcode.New(errorcode.PermissionDenied, "gatewayapp: managed child no longer matches its parent participant binding")
	}
	if taskapi.NormalizeHandle(input.Target) == taskapi.NormalizeHandle(hostedChildHandle(binding)) {
		return errorcode.New(errorcode.Conflict, fmt.Sprintf("gatewayapp: subagent %q cannot message itself", hostedChildHandle(binding)))
	}
	return parentRuntime.instance.engine.SubmitParticipantInput(
		ctx,
		parentRef,
		binding,
		input,
		func(ctx context.Context, current session.Session, source session.ActorRef, input agent.AgentInput) error {
			return routeHostedChildInputToParent(ctx, &parentRuntime.instance.runtimeComposition, current, source, input)
		},
	)
}

func routeHostedChildInputToParent(
	ctx context.Context,
	composition *runtimeComposition,
	active session.Session,
	source session.ActorRef,
	input agent.AgentInput,
) error {
	if composition == nil || composition.currentGateway() == nil {
		return fmt.Errorf("gatewayapp: parent Agent input gateway is unavailable")
	}
	gw := composition.currentGateway()
	for {
		if turn, ok := gw.ActiveTurn(active.SessionID); ok {
			if turn.Kind != kernel.ActiveTurnKindKernel || strings.TrimSpace(turn.ParticipantID) != "" {
				return errorcode.New(errorcode.Conflict, "gatewayapp: parent controller is not the active Turn owner")
			}
			err := gw.SubmitActiveTurn(ctx, kernel.SubmitActiveTurnRequest{
				SessionRef: active.SessionRef, HandleID: turn.HandleID, RunID: turn.RunID, TurnID: turn.TurnID,
				Kind: kernel.SubmissionKindAgentCommunication, Text: input.Input, DisplayText: input.DisplayInput,
				ContentParts: input.ContentParts, Actor: source,
			})
			if isHostedChildInputClosingEdge(err) {
				if waitErr := gw.WaitActiveTurnChange(ctx, turn); waitErr != nil {
					return waitErr
				}
				continue
			}
			if !isHostedChildInputSelectionRace(err) {
				return err
			}
			continue
		}
		observer, releaseTurn := composition.controlTurnObserver(active.SessionRef)
		result, err := gw.BeginTurn(ctx, kernel.BeginTurnRequest{
			SessionRef:     active.SessionRef,
			RuntimeContext: composition.controlRuntimeContext(context.Background(), active),
			InputKind:      kernel.SubmissionKindAgentCommunication,
			Input:          input.Input, DisplayInput: input.DisplayInput, ContentParts: input.ContentParts,
			InputActor: source, Surface: "agent-input",
			Observer: observer,
		})
		retainControlTurn(result.Handle, releaseTurn)
		if err == nil {
			return nil
		}
		if !isHostedChildInputSelectionRace(err) {
			return err
		}
	}
}

func isHostedChildInputSelectionRace(err error) bool {
	if err == nil {
		return false
	}
	var gatewayErr *kernel.Error
	return kernel.As(err, &gatewayErr) &&
		(gatewayErr.Code == kernel.CodeNoActiveRun || gatewayErr.Code == kernel.CodeActiveRunConflict)
}

func isHostedChildInputClosingEdge(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, agent.ErrRunInputClosed) {
		return true
	}
	var gatewayErr *kernel.Error
	return kernel.As(err, &gatewayErr) && gatewayErr.Code == kernel.CodeSubmissionUnsupported
}

type hostedChildInputSender struct {
	route    hostedChildInputFunc
	sessions session.Service
	parent   session.Session
	child    session.Session
}

func (s *runtimeComposition) hostedChildInputSender(active session.Session) agent.AgentInputSender {
	if s == nil || !sessionvisibility.IsSpawnedSubagentSession(active) {
		return nil
	}
	route := s.childInput()
	if route == nil {
		return nil
	}
	parentID := hostedChildMetadataString(active.Metadata, sessionvisibility.MetadataSystemManagedParent)
	taskID := hostedChildMetadataString(active.Metadata, sessionvisibility.MetadataSystemManagedTask)
	if parentID == "" || taskID == "" {
		return nil
	}
	return hostedChildInputSender{route: route, sessions: s.sessions, child: session.CloneSession(active)}
}

func (s *runtimeComposition) childInput() hostedChildInputFunc {
	if s == nil {
		return nil
	}
	return s.authorities.hostedChildInput
}

func (s hostedChildInputSender) SendAgentInput(ctx context.Context, raw agent.AgentInput) error {
	if s.route == nil {
		return fmt.Errorf("gatewayapp: hosted child input route is unavailable")
	}
	parent, err := s.resolveParent(ctx)
	if err != nil {
		return err
	}
	taskID := hostedChildMetadataString(s.child.Metadata, sessionvisibility.MetadataSystemManagedTask)
	return s.route(ctx, parent.SessionRef, s.child.SessionRef, taskID, raw)
}

func (s hostedChildInputSender) resolveParent(ctx context.Context) (session.Session, error) {
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
	return hostedChildParticipantByIdentity(
		parent,
		strings.TrimSpace(child.SessionID),
		hostedChildMetadataString(child.Metadata, sessionvisibility.MetadataSystemManagedTask),
	)
}

func hostedChildParticipantByIdentity(parent session.Session, childSessionID, delegationID string) (session.ParticipantBinding, bool) {
	childSessionID = strings.TrimSpace(childSessionID)
	delegationID = strings.TrimSpace(delegationID)
	if childSessionID == "" || delegationID == "" {
		return session.ParticipantBinding{}, false
	}
	for _, binding := range parent.Participants {
		if binding.Kind == session.ParticipantKindSubagent &&
			strings.TrimSpace(binding.SessionID) == childSessionID &&
			strings.TrimSpace(binding.DelegationID) == delegationID &&
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
