package appserver

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

// CapabilityConversationInteractions identifies durable display-only command
// input. These messages do not enter model context or admit a model Turn.
const CapabilityConversationInteractions = "conversation-interactions-v1"

const ActionConversationInteraction Action = "conversation.interaction"

const CommandResourceConversationMessage = "conversation-message"

// RecordInteractionRequest appends a user command input to the native Session
// conversation before the shared command router dispatches its effect.
type RecordInteractionRequest struct {
	WriteBase
	Text string `json:"text"`
	// RequireIdleRun preserves conditional admission through the native owner.
	// A busy Session is rejected before the conversation input is appended.
	RequireIdleRun bool `json:"require_idle_run,omitempty"`
}

// InteractionClient is an additive principal-bound conversation capability.
// It is separate from SessionClient to preserve existing embedded clients.
type InteractionClient interface {
	RecordInteraction(context.Context, RecordInteractionRequest) (CommandResult, error)
}

// InteractionService uses the same authorizer, operation ledger and canonical
// Session Store as the other Session mutations.
type InteractionService interface {
	RecordInteraction(context.Context, Principal, RecordInteractionRequest) (CommandResult, error)
}

func (s *CommandService) RecordInteraction(ctx context.Context, principal Principal, request RecordInteractionRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionConversationInteraction, request.WriteBase, request.SessionID, request)
}

func (c *Client) RecordInteraction(ctx context.Context, principal Principal, request RecordInteractionRequest) (CommandResult, error) {
	service, ok := c.CommandClient.(InteractionService)
	if !ok {
		return CommandResult{}, errorcode.New(errorcode.Unsupported, "controlclient: conversation interaction capability is unavailable")
	}
	return service.RecordInteraction(ctx, principal, request)
}

// SupportsConversationInteractions reports the underlying owner's capability,
// rather than inferring support from this forwarding facade's method set.
func (c *Client) SupportsConversationInteractions() bool {
	_, ok := c.CommandClient.(InteractionService)
	return ok
}

func (c *boundSessionClient) RecordInteraction(ctx context.Context, request RecordInteractionRequest) (CommandResult, error) {
	service, ok := c.service.(InteractionService)
	if !ok {
		return CommandResult{}, errorcode.New(errorcode.Unsupported, "controlclient: conversation interaction capability is unavailable")
	}
	return service.RecordInteraction(ctx, c.boundPrincipal(), request)
}

// SupportsConversationInteractions checks an assembled Service's real owner.
func SupportsConversationInteractions(service Service) bool {
	if owner, ok := service.(interface{ SupportsConversationInteractions() bool }); ok {
		return owner.SupportsConversationInteractions()
	}
	_, ok := service.(InteractionService)
	return ok
}
