package gatewayapp

import (
	"context"
	"fmt"
	"strings"

	agentmessage "github.com/caelis-labs/caelis/agent-sdk/message"
	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	sdksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	acpassembly "github.com/caelis-labs/caelis/internal/acpagentbridge/assembly"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// AgentMessageDelivery reports durable delivery and any new target turn.
type AgentMessageDelivery struct {
	Accepted bool
	State    string
	Turn     kernel.TurnHandle
}

// DeliverAgentMessageRequest carries the trusted source and every durable
// revision used to resolve it into the Runtime delivery boundary.
type DeliverAgentMessageRequest struct {
	SessionRef       session.SessionRef
	Message          agentmessage.Request
	ExpectedRevision *uint64
	RelatedRevisions []session.SessionRevisionPrecondition
}

// AgentMessageDeliveryService is the focused Host-private write authority used
// by AppServer Agent-message ingress. It retains only Runtime delivery
// authorities and never the concrete Host Stack.
type AgentMessageDeliveryService struct {
	composition *runtimeComposition
	registry    *sessionRuntimeRegistry
}

// AgentMessageDelivery returns the focused Agent-message delivery authority.
func (s *Stack) AgentMessageDelivery() AgentMessageDeliveryService {
	if s == nil {
		return AgentMessageDeliveryService{}
	}
	return AgentMessageDeliveryService{composition: &s.composition, registry: s.sessionRuntimes}
}

// DeliverAgentMessage activates the target Session Runtime when necessary and
// delegates message ownership to its Control Gateway.
func (s *Stack) DeliverAgentMessage(ctx context.Context, request DeliverAgentMessageRequest) (AgentMessageDelivery, error) {
	return s.AgentMessageDelivery().Deliver(ctx, request)
}

// Deliver activates the target Session Runtime when necessary and delegates
// message ownership to its Control Gateway.
func (s AgentMessageDeliveryService) Deliver(ctx context.Context, request DeliverAgentMessageRequest) (AgentMessageDelivery, error) {
	if s.composition == nil {
		return AgentMessageDelivery{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	ref := session.NormalizeSessionRef(request.SessionRef)
	composition := s.composition
	var release func(context.Context) error
	var active session.Session
	if s.registry != nil {
		runtime, loaded, releaseRuntime, err := s.registry.acquireControlRuntime(ctx, ref.SessionID, true)
		if err != nil {
			return AgentMessageDelivery{}, err
		}
		active = loaded
		if runtime != nil && runtime.instance != nil {
			composition = &runtime.instance.runtimeComposition
		}
		release = releaseRuntime
	} else if s.composition.sessions != nil {
		if loaded, err := s.composition.sessions.Session(ctx, ref); err == nil {
			active = loaded
		}
	}
	if release != nil {
		defer func() {
			_ = release(context.WithoutCancel(ctx))
		}()
	}
	gateway := composition.currentGateway()
	if gateway == nil {
		return AgentMessageDelivery{}, fmt.Errorf("gatewayapp: Agent message gateway is unavailable")
	}
	delivery, err := gateway.DeliverAgentMessage(ctx, kernel.DeliverAgentMessageRequest{
		SessionRef: ref, RuntimeContext: composition.controlRuntimeContext(ctx, active), Message: request.Message,
		ExpectedRevision: request.ExpectedRevision,
		RelatedRevisions: append([]session.SessionRevisionPrecondition(nil), request.RelatedRevisions...),
	})
	if err != nil {
		return AgentMessageDelivery{}, err
	}
	if delivery.Turn != nil {
		composition.attachControlClientHandle(delivery.Turn)
	}
	return AgentMessageDelivery{Accepted: delivery.Accepted, State: delivery.State, Turn: delivery.Turn}, nil
}

// bindSubagentMessageRouter assigns child source identity from the trusted
// Spawn binding and keeps Session-scoped parent/sibling routing in Runtime.
func bindSubagentMessageRouter(controlPlane *acpassembly.ControlPlane, runtime *sdkruntime.Runtime) {
	if controlPlane == nil || runtime == nil {
		return
	}
	controlPlane.BindMessageHandler(func(ctx context.Context, spawn sdksubagent.SpawnContext, anchor delegation.Anchor, req agentmessage.Request) (agentmessage.Response, error) {
		req = agentmessage.NormalizeRequest(req)
		handle := strings.TrimSpace(spawn.Handle)
		if handle == "" {
			handle = strings.TrimSpace(spawn.TaskID)
		}
		if strings.EqualFold(strings.TrimPrefix(req.To, "@"), strings.TrimPrefix(handle, "@")) {
			return agentmessage.Response{}, fmt.Errorf("gatewayapp: subagent %q cannot message itself", handle)
		}
		role := spawn.Role
		if role == "" {
			role = session.ParticipantRoleDelegated
		}
		req.From = session.ActorRef{
			Kind: session.ActorKindParticipant, ID: strings.TrimSpace(anchor.AgentID),
			Name: "@" + strings.TrimPrefix(handle, "@"), Role: string(role),
		}
		req.Scope = &session.EventScope{Source: "subagent_message", Participant: session.ParticipantRef{
			ID: strings.TrimSpace(anchor.AgentID), Kind: session.ParticipantKindSubagent,
			Role: role, DelegationID: strings.TrimSpace(spawn.TaskID),
		}}
		return runtime.SendAgentMessage(ctx, spawn.SessionRef, req)
	})
}
