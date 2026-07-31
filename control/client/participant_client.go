package controlclient

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// ParticipantClient is the principal-bound participant command contract. It
// stays separate from SessionClient so participant execution remains a focused
// capability across embedded and HTTP AppServer transports.
type ParticipantClient interface {
	Handles(context.Context, string) ([]string, error)
	StartParticipant(context.Context, StartParticipantRequest) (CommandResult, error)
	PromptParticipant(context.Context, PromptParticipantRequest) (CommandResult, error)
	CancelParticipant(context.Context, CancelParticipantRequest) (CommandResult, error)
}

// ParticipantHandleReader returns the directly runnable handle names visible
// in one Session Runtime snapshot without activating an idle Runtime.
type ParticipantHandleReader interface {
	ParticipantHandles(context.Context, string) ([]string, error)
}

// ParticipantHandleService authorizes the Session-scoped handle projection.
type ParticipantHandleService interface {
	ListParticipantHandles(context.Context, Principal, string) ([]string, error)
}

// ParticipantService is the focused principal-aware server-side capability
// used to bind participant execution without growing the Session Service.
type ParticipantService interface {
	ParticipantStarter
	ParticipantHandleService
	PromptParticipant(context.Context, Principal, PromptParticipantRequest) (CommandResult, error)
	CancelParticipant(context.Context, Principal, CancelParticipantRequest) (CommandResult, error)
}

type boundParticipantClient struct {
	service   ParticipantService
	principal Principal
}

// BindParticipantClient binds one trusted principal to participant commands.
func BindParticipantClient(service ParticipantService, principal Principal) (ParticipantClient, error) {
	if service == nil {
		return nil, errors.New("controlclient: service is required")
	}
	principal.ID = strings.TrimSpace(principal.ID)
	if principal.ID == "" {
		return nil, errors.New("controlclient: principal ID is required")
	}
	principal.Roles = append([]string(nil), principal.Roles...)
	return &boundParticipantClient{service: service, principal: principal}, nil
}

func (c *boundParticipantClient) StartParticipant(ctx context.Context, request StartParticipantRequest) (CommandResult, error) {
	return c.service.StartParticipant(ctx, c.boundPrincipal(), request)
}

func (c *boundParticipantClient) Handles(ctx context.Context, sessionID string) ([]string, error) {
	return c.service.ListParticipantHandles(ctx, c.boundPrincipal(), strings.TrimSpace(sessionID))
}

func (c *boundParticipantClient) PromptParticipant(ctx context.Context, request PromptParticipantRequest) (CommandResult, error) {
	return c.service.PromptParticipant(ctx, c.boundPrincipal(), request)
}

func (c *boundParticipantClient) CancelParticipant(ctx context.Context, request CancelParticipantRequest) (CommandResult, error) {
	return c.service.CancelParticipant(ctx, c.boundPrincipal(), request)
}

func (c *boundParticipantClient) boundPrincipal() Principal {
	principal := c.principal
	principal.Roles = append([]string(nil), principal.Roles...)
	return principal
}

// ParticipantTurnStartRequest starts a new handle-selected participant and
// its first Turn through the addressed Session Runtime.
type ParticipantTurnStartRequest struct {
	SessionID      string
	Handle         string
	Role           session.ParticipantRole
	Label          string
	Source         string
	Input          string
	DisplayInput   string
	DisplayAddress string
	DisplayTitle   string
	ContentParts   []model.ContentPart
	Transient      bool
	DetachSource   string
}

// ParticipantTurnPromptRequest starts a Turn on one already attached
// participant.
type ParticipantTurnPromptRequest struct {
	SessionID      string
	ParticipantID  string
	Input          string
	DisplayInput   string
	DisplayAddress string
	DisplayTitle   string
	ContentParts   []model.ContentPart
	Source         string
}

// ParticipantTurnClient owns target observation and participant writes while
// reusing the authoritative Session feed for output and approval resolution.
type ParticipantTurnClient struct {
	sessions     SessionClient
	participants ParticipantClient
}

// NewParticipantTurnClient constructs a participant Turn client from the
// transport-neutral Session and participant contracts.
func NewParticipantTurnClient(sessions SessionClient, participants ParticipantClient) (*ParticipantTurnClient, error) {
	if sessions == nil || participants == nil {
		return nil, errors.New("controlclient: Session and participant clients are required")
	}
	return &ParticipantTurnClient{sessions: sessions, participants: participants}, nil
}

// Start attaches one participant and starts its first target-filtered Turn.
func (c *ParticipantTurnClient) Start(ctx context.Context, request ParticipantTurnStartRequest) (TargetTurn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID := strings.TrimSpace(request.SessionID)
	if sessionID == "" || strings.TrimSpace(request.Handle) == "" {
		return nil, errors.New("controlclient: participant Turn requires Session ID and handle")
	}
	if strings.TrimSpace(request.Input) == "" && len(request.ContentParts) == 0 {
		return nil, errors.New("controlclient: participant Turn requires prompt input")
	}
	return c.startObserved(ctx, sessionID, func(state SessionState) (CommandResult, error) {
		revision := state.Revision
		return c.participants.StartParticipant(ctx, StartParticipantRequest{
			WriteBase: WriteBase{
				OperationID:             newParticipantOperationID("start"),
				SessionID:               sessionID,
				ExpectedRevision:        &revision,
				ExpectedControllerEpoch: strings.TrimSpace(state.Controller.EpochID),
			},
			Handle:         strings.TrimSpace(request.Handle),
			Role:           request.Role,
			Label:          strings.TrimSpace(request.Label),
			Source:         strings.TrimSpace(request.Source),
			Input:          strings.TrimSpace(request.Input),
			DisplayInput:   normalizedDisplayInput(request.Input, request.DisplayInput),
			DisplayAddress: strings.TrimSpace(request.DisplayAddress),
			DisplayTitle:   strings.TrimSpace(request.DisplayTitle),
			ContentParts:   append([]model.ContentPart(nil), request.ContentParts...),
			Transient:      request.Transient,
			DetachSource:   strings.TrimSpace(request.DetachSource),
		})
	}, func(state SessionState) string {
		return matchingParticipantID(state.Participants, request.Label, request.Source)
	})
}

// Prompt starts a target-filtered Turn on one attached participant.
func (c *ParticipantTurnClient) Prompt(ctx context.Context, request ParticipantTurnPromptRequest) (TargetTurn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID := strings.TrimSpace(request.SessionID)
	participantID := strings.TrimSpace(request.ParticipantID)
	if sessionID == "" || participantID == "" {
		return nil, errors.New("controlclient: participant prompt requires Session and participant IDs")
	}
	if strings.TrimSpace(request.Input) == "" && len(request.ContentParts) == 0 {
		return nil, errors.New("controlclient: participant prompt requires input")
	}
	return c.startObserved(ctx, sessionID, func(state SessionState) (CommandResult, error) {
		revision := state.Revision
		return c.participants.PromptParticipant(ctx, PromptParticipantRequest{
			WriteBase: WriteBase{
				OperationID:             newParticipantOperationID("prompt"),
				SessionID:               sessionID,
				ExpectedRevision:        &revision,
				ExpectedControllerEpoch: strings.TrimSpace(state.Controller.EpochID),
			},
			ParticipantID:  participantID,
			Input:          strings.TrimSpace(request.Input),
			DisplayInput:   normalizedDisplayInput(request.Input, request.DisplayInput),
			DisplayAddress: strings.TrimSpace(request.DisplayAddress),
			DisplayTitle:   strings.TrimSpace(request.DisplayTitle),
			ContentParts:   append([]model.ContentPart(nil), request.ContentParts...),
			Source:         strings.TrimSpace(request.Source),
		})
	}, func(SessionState) string { return participantID })
}

func (c *ParticipantTurnClient) startObserved(
	ctx context.Context,
	sessionID string,
	start func(SessionState) (CommandResult, error),
	resolveParticipantID func(SessionState) string,
) (TargetTurn, error) {
	if c == nil || c.sessions == nil || c.participants == nil {
		return nil, errors.New("controlclient: participant Turn client is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	feedCtx, stopFeed, reconnected, err := openTargetObservation(ctx, c.sessions, sessionID)
	if err != nil {
		return nil, err
	}
	result, err := start(reconnected.State)
	if err != nil {
		_ = reconnected.Subscription.Close()
		stopFeed()
		return nil, err
	}
	if result.Outcome != OutcomeCommitted && result.Outcome != OutcomeAccepted {
		_ = reconnected.Subscription.Close()
		stopFeed()
		return nil, fmt.Errorf("controlclient: participant operation %q ended with outcome %q", result.OperationID, result.Outcome)
	}
	if !validSessionTurnTarget(result.Target) {
		_ = reconnected.Subscription.Close()
		stopFeed()
		return nil, errors.New("controlclient: participant operation returned no complete Turn target")
	}
	participantID := strings.TrimSpace(result.ParticipantID)
	if participantID == "" {
		latest, inspectErr := c.sessions.InspectSession(ctx, StateRequest{SessionID: sessionID})
		if inspectErr != nil {
			_ = reconnected.Subscription.Close()
			stopFeed()
			return nil, inspectErr
		}
		participantID = strings.TrimSpace(resolveParticipantID(latest))
	}
	if participantID == "" {
		_ = reconnected.Subscription.Close()
		stopFeed()
		return nil, errors.New("controlclient: participant operation returned no addressable participant")
	}
	turn := newTargetTurn(c.sessions, sessionID, reconnected, feedCtx, stopFeed, result.Target)
	turn.cancelFn = func(cancelCtx context.Context, reason string) error {
		_, cancelErr := c.participants.CancelParticipant(cancelCtx, CancelParticipantRequest{
			WriteBase: WriteBase{
				OperationID:             newParticipantOperationID("cancel"),
				SessionID:               sessionID,
				ExpectedControllerEpoch: turn.controllerEpoch,
			},
			ParticipantID: participantID,
			Target:        result.Target,
			Reason:        strings.TrimSpace(reason),
		})
		return cancelErr
	}
	go turn.relay()
	return turn, nil
}

func matchingParticipantID(participants []session.ParticipantBinding, label, source string) string {
	label = strings.TrimSpace(label)
	source = strings.TrimSpace(source)
	for i := len(participants) - 1; i >= 0; i-- {
		participant := participants[i]
		if label != "" && !strings.EqualFold(strings.TrimSpace(participant.Label), label) {
			continue
		}
		if source != "" && strings.TrimSpace(participant.Source) != source {
			continue
		}
		if id := strings.TrimSpace(participant.ID); id != "" {
			return id
		}
	}
	return ""
}

func normalizedDisplayInput(input, display string) string {
	input = strings.TrimSpace(input)
	display = strings.TrimSpace(display)
	if display == input {
		return ""
	}
	return display
}

func newParticipantOperationID(kind string) string {
	return "participant-turn-" + strings.TrimSpace(kind) + "-" + uuid.NewString()
}

var _ ParticipantClient = (*boundParticipantClient)(nil)
