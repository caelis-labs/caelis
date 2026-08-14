// Package message defines the small, provider-neutral contract used by Agents
// to exchange messages without borrowing user-input or Task-control semantics.
package message

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const (
	Parent              = "parent"
	toolMessageIDPrefix = "tool-message:v1:"
	StatePending        = "pending"
	StateDelivered      = "delivered"
	StateRunning        = "running"
	StateCompleted      = "completed"
	// StateUnknownOutcome means delivery may have committed but its response
	// could not be proven. Callers must not retry it with a fresh MessageID.
	StateUnknownOutcome = "unknown_outcome"

	// StateAcceptedUnpersisted means the delivery boundary accepted ownership
	// but the sender could not durably refresh its local observation record.
	// Retrying with a new message id may enqueue a duplicate delivery.
	StateAcceptedUnpersisted = "accepted_unpersisted"
)

// ToolMessageID derives the stable delivery identity for one SendMessage tool
// call. The source Session and tool-call identity are authoritative: retrying
// the same call produces the same id, while reusing that identity with changed
// content is rejected by canonical Session idempotency checks.
func ToolMessageID(ref session.SessionRef, callID string) (string, error) {
	ref = session.NormalizeSessionRef(ref)
	sessionID := strings.TrimSpace(ref.SessionID)
	callID = strings.TrimSpace(callID)
	if sessionID == "" || callID == "" {
		return "", fmt.Errorf("agent-sdk/message: source session and tool call id are required")
	}
	digest := sha256.Sum256([]byte(toolMessageIDPrefix + sessionID + "\x00" + callID))
	return toolMessageIDPrefix + hex.EncodeToString(digest[:16]), nil
}

// Request is one accepted Agent-to-Agent message. From is assigned by the
// trusted runtime boundary; model-facing tools control only To and Text.
type Request struct {
	MessageID string              `json:"message_id,omitempty"`
	To        string              `json:"to,omitempty"`
	Text      string              `json:"text,omitempty"`
	From      session.ActorRef    `json:"from,omitempty"`
	Scope     *session.EventScope `json:"scope,omitempty"`
	// DisplayFrom preserves an untrusted transport label for display and debug
	// metadata only. It never supplies canonical Actor identity.
	DisplayFrom string `json:"display_from,omitempty"`
}

// Response acknowledges that the routing boundary accepted ownership of
// delivery. It does not acknowledge target consumption or Turn completion.
type Response struct {
	MessageID   string `json:"message_id,omitempty"`
	Accepted    bool   `json:"accepted,omitempty"`
	State       string `json:"state,omitempty"`
	TurnID      string `json:"turn_id,omitempty"`
	StartedTurn bool   `json:"started_turn,omitempty"`
}

// Sender accepts messages for routing by the current Agent host.
type Sender interface {
	SendMessage(context.Context, Request) (Response, error)
}

type senderContextKey struct{}

// WithSender exposes a trusted message transport to runtime-wrapped tools.
func WithSender(ctx context.Context, sender Sender) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if sender == nil {
		return ctx
	}
	return context.WithValue(ctx, senderContextKey{}, sender)
}

// SenderFromContext returns the message transport bound by an Agent host.
func SenderFromContext(ctx context.Context) Sender {
	if ctx == nil {
		return nil
	}
	sender, _ := ctx.Value(senderContextKey{}).(Sender)
	return sender
}

// NormalizeRequest returns a detached canonical request.
func NormalizeRequest(req Request) Request {
	req.MessageID = strings.TrimSpace(req.MessageID)
	req.To = strings.TrimSpace(req.To)
	req.Text = strings.TrimSpace(req.Text)
	req.DisplayFrom = strings.TrimSpace(req.DisplayFrom)
	req.From.ID = strings.TrimSpace(req.From.ID)
	req.From.Name = strings.TrimSpace(req.From.Name)
	req.From.Role = strings.TrimSpace(req.From.Role)
	if req.Scope != nil {
		scope := session.CloneEventScope(*req.Scope)
		req.Scope = &scope
	}
	return req
}

// ContextEvent returns the canonical provider-neutral Session fact for one
// accepted Agent message. fallback carries target-owned Turn/controller scope;
// an explicitly supplied request scope may refine it but cannot erase those
// identities accidentally.
func ContextEvent(raw Request, fallback session.EventScope) (*session.Event, error) {
	req := NormalizeRequest(raw)
	if req.MessageID == "" || req.Text == "" || !session.ActorRefHasIdentity(req.From) {
		return nil, fmt.Errorf("agent-sdk/message: message id, text, and source are required")
	}
	fallback = session.CloneEventScope(fallback)
	if strings.TrimSpace(fallback.Source) == "" {
		fallback.Source = "agent_message"
	}
	scope := fallback
	if req.Scope != nil {
		scope = session.CloneEventScope(*req.Scope)
		if strings.TrimSpace(scope.TurnID) == "" {
			scope.TurnID = fallback.TurnID
		}
		if strings.TrimSpace(scope.Source) == "" {
			scope.Source = fallback.Source
		}
		if scope.Controller.Kind == "" {
			scope.Controller = fallback.Controller
		}
		if !session.ActorRefHasIdentity(scope.Executor) {
			scope.Executor = fallback.Executor
		}
	}
	message := model.NewTextMessage(model.RoleUser, req.Text)
	metadata := map[string]any{"agent_message": true, "to": req.To}
	if req.DisplayFrom != "" {
		metadata["display_from"] = req.DisplayFrom
	}
	return &session.Event{
		IdempotencyKey: "agent-message:" + req.MessageID,
		MessageID:      req.MessageID,
		Type:           session.EventTypeContext,
		Visibility:     session.VisibilityCanonical,
		Actor:          session.CloneActorRef(req.From),
		Scope:          &scope,
		Message:        &message,
		Text:           req.Text,
		Meta:           metadata,
	}, nil
}

// AppendContext persists one canonical Agent message and reports whether this
// call created it. Precise new-vs-existing outcome is mandatory because only a
// newly appended fact may wake a live target; a read-before-append fallback is
// not safe under concurrent delivery.
func AppendContext(
	ctx context.Context,
	appender session.EventAppender,
	ref session.SessionRef,
	guard session.MutationGuard,
	fallback session.EventScope,
	req Request,
) (session.AppendEventResult, error) {
	return AppendContextConditional(ctx, appender, ref, guard, fallback, req, nil, nil)
}

// AppendContextConditional persists one canonical Agent message only while
// the target and every related Session remain at the revisions used to resolve
// its trusted source. Related revision checks and the append are atomic.
func AppendContextConditional(
	ctx context.Context,
	appender session.EventAppender,
	ref session.SessionRef,
	guard session.MutationGuard,
	fallback session.EventScope,
	req Request,
	expectedRevision *uint64,
	relatedRevisions []session.SessionRevisionPrecondition,
) (session.AppendEventResult, error) {
	precise, ok := appender.(session.EventAppenderWithOutcome)
	if !ok {
		return session.AppendEventResult{}, fmt.Errorf("agent-sdk/message: Agent message persistence requires EventAppenderWithOutcome")
	}
	event, err := ContextEvent(req, fallback)
	if err != nil {
		return session.AppendEventResult{}, err
	}
	appendRequest := session.AppendEventRequest{
		SessionRef: session.NormalizeSessionRef(ref), ExpectedRevision: expectedRevision,
		MutationGuard: guard, Event: event,
	}
	if len(relatedRevisions) == 0 {
		return precise.AppendEventWithOutcome(ctx, appendRequest)
	}
	conditional, ok := appender.(session.ConditionalEventAppenderWithOutcome)
	if !ok {
		return session.AppendEventResult{}, fmt.Errorf(
			"agent-sdk/message: related Agent message authority requires ConditionalEventAppenderWithOutcome",
		)
	}
	return conditional.AppendEventWithOutcomeConditional(ctx, session.ConditionalAppendEventRequest{
		AppendEventRequest: appendRequest,
		RelatedRevisions:   append([]session.SessionRevisionPrecondition(nil), relatedRevisions...),
	})
}
