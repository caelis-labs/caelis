package controlclient

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

type checkpointSessionFeed interface {
	subscribeCheckpoint(context.Context, SubscribeRequest) (SubscribeResult, session.EventCheckpoint, error)
}

// Reconnect registers the continuation first and assembles typed state from
// that exact feed cut. It never waits for a quiescent publish window.
func (s *StateService) Reconnect(
	ctx context.Context,
	req ReconnectRequest,
) (ReconnectResult, error) {
	if s == nil {
		return ReconnectResult{}, errors.New("controlclient: nil state service")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return ReconnectResult{}, session.ErrInvalidSession
	}
	ref := session.SessionRef{SessionID: sessionID}
	feed, err := s.config.Feeds.Session(ref)
	if err != nil {
		return ReconnectResult{}, err
	}

	subscribeRequest := SubscribeRequest{SessionID: sessionID, Cursor: strings.TrimSpace(req.Cursor)}
	var subscribed SubscribeResult
	var checkpoint session.EventCheckpoint
	if prepared, ok := feed.(checkpointSessionFeed); ok {
		subscribed, checkpoint, err = prepared.subscribeCheckpoint(ctx, subscribeRequest)
	} else {
		subscribed, err = feed.Subscribe(ctx, subscribeRequest)
	}
	if err != nil {
		// A just-accepted durable Envelope may briefly lead the atomic store
		// checkpoint. Expose that expected bootstrap race as a stable retryable
		// conflict instead of leaking it as an opaque HTTP 500.
		if errors.Is(err, errDurableCheckpointBehindAcceptedFeed) {
			return ReconnectResult{}, ErrStateRevisionConflict
		}
		return ReconnectResult{}, err
	}
	if subscribed.Subscription == nil {
		return ReconnectResult{}, errors.New("controlclient: reconnect feed returned no continuation")
	}
	abort := true
	defer func() {
		if abort {
			_ = subscribed.Subscription.Close()
		}
	}()

	activeSession := checkpoint.Session
	if strings.TrimSpace(activeSession.SessionID) == "" {
		activeSession, err = s.config.Sessions.Session(ctx, ref)
		if err != nil {
			return ReconnectResult{}, err
		}
	}
	runtimeState, err := s.config.Runtime.ControlClientRuntimeState(ctx, activeSession.SessionRef)
	if err != nil {
		return ReconnectResult{}, err
	}
	state := sessionStateAtFeedCut(activeSession, runtimeState, subscribed)
	abort = false
	return ReconnectResult{State: state, Subscription: subscribed.Subscription}, nil
}

func sessionStateAtFeedCut(
	activeSession session.Session,
	runtimeState RuntimeState,
	subscribed SubscribeResult,
) SessionState {
	position := eventstream.CloneFeedPosition(subscribed.BoundaryPosition)
	return SessionState{
		ProtocolVersion:  schema.CurrentProtocolVersion,
		EnvelopeVersion:  EnvelopeVersion,
		APIVersion:       HTTPAPIVersion,
		SessionID:        activeSession.SessionID,
		Revision:         activeSession.Revision,
		WorkspaceKey:     activeSession.WorkspaceKey,
		CWD:              activeSession.CWD,
		Title:            activeSession.Title,
		Metadata:         cloneAnyMap(activeSession.Metadata),
		BoundaryCursor:   subscribed.BoundaryCursor,
		BoundaryPosition: position,
		ResumeMode:       subscribed.Mode,
		TransientGap:     subscribed.TransientGap,
		Run:              runtimeState.Run,
		Controller:       activeSession.Controller,
		Participants:     append([]session.ParticipantBinding(nil), activeSession.Participants...),
		Approval:         runtimeState.Approval,
		Capabilities: ClientCapabilities{
			ClientManagedTerminal: false, CaelisTerminalStream: true,
			GoalBootstrapSupported: false, ManageLoopBootstrapSupported: false,
		},
	}
}
