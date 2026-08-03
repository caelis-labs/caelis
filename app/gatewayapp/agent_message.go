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

// DeliverAgentMessage activates the target Session Runtime when necessary and
// delegates message ownership to its Control Gateway.
func (s *Stack) DeliverAgentMessage(ctx context.Context, ref session.SessionRef, req agentmessage.Request) (AgentMessageDelivery, error) {
	if s == nil {
		return AgentMessageDelivery{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	runtimeStack := s
	var release func(context.Context) error
	if s.sessionRuntimes != nil {
		runtime, _, releaseRuntime, err := s.sessionRuntimes.acquireControlRuntime(ctx, ref.SessionID, true)
		if err != nil {
			return AgentMessageDelivery{}, err
		}
		if runtime != nil && runtime.stack != nil {
			runtimeStack = runtime.stack
		}
		release = releaseRuntime
	}
	if release != nil {
		defer func() {
			_ = release(context.WithoutCancel(ctx))
		}()
	}
	gateway := runtimeStack.currentGateway()
	if gateway == nil {
		return AgentMessageDelivery{}, fmt.Errorf("gatewayapp: Agent message gateway is unavailable")
	}
	delivery, err := gateway.DeliverAgentMessage(ctx, ref, req)
	if err != nil {
		return AgentMessageDelivery{}, err
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
