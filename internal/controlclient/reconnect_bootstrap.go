package controlclient

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

type checkpointSessionFeed interface {
	subscribeCheckpoint(context.Context, controlport.SubscribeRequest) (controlport.SubscribeResult, session.EventCheckpoint, error)
}

// Reconnect registers the continuation first and assembles typed state from
// that exact feed cut. It never waits for a quiescent publish window.
func (s *StateService) Reconnect(
	ctx context.Context,
	req controlport.ReconnectRequest,
) (controlport.ReconnectResult, error) {
	if s == nil {
		return controlport.ReconnectResult{}, errors.New("controlclient: nil state service")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return controlport.ReconnectResult{}, session.ErrInvalidSession
	}
	ref := session.SessionRef{SessionID: sessionID}
	feed, err := s.config.Feeds.Session(ref)
	if err != nil {
		return controlport.ReconnectResult{}, err
	}

	subscribeRequest := controlport.SubscribeRequest{SessionID: sessionID, Cursor: strings.TrimSpace(req.Cursor)}
	var subscribed controlport.SubscribeResult
	var checkpoint session.EventCheckpoint
	if prepared, ok := feed.(checkpointSessionFeed); ok {
		subscribed, checkpoint, err = prepared.subscribeCheckpoint(ctx, subscribeRequest)
	} else {
		subscribed, err = feed.Subscribe(ctx, subscribeRequest)
	}
	if err != nil {
		return controlport.ReconnectResult{}, err
	}
	if subscribed.Subscription == nil {
		return controlport.ReconnectResult{}, errors.New("controlclient: reconnect feed returned no continuation")
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
			return controlport.ReconnectResult{}, err
		}
	}
	runtimeState, err := s.config.Runtime.ControlClientRuntimeState(ctx, activeSession.SessionRef)
	if err != nil {
		return controlport.ReconnectResult{}, err
	}
	state := sessionStateAtFeedCut(activeSession, runtimeState, subscribed)
	abort = false
	return controlport.ReconnectResult{State: state, Subscription: subscribed.Subscription}, nil
}

func sessionStateAtFeedCut(
	activeSession session.Session,
	runtimeState controlport.RuntimeState,
	subscribed controlport.SubscribeResult,
) controlport.SessionState {
	position := eventstream.CloneFeedPosition(subscribed.BoundaryPosition)
	return controlport.SessionState{
		ProtocolVersion:  schema.CurrentProtocolVersion,
		EnvelopeVersion:  controlport.EnvelopeVersion,
		APIVersion:       controlport.HTTPAPIVersion,
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
		Capabilities: controlport.ClientCapabilities{
			ClientManagedTerminal: false, CaelisTerminalStream: true,
			GoalBootstrapSupported: false, ManageLoopBootstrapSupported: false,
		},
	}
}
