package controlclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

const defaultStateRetries = 3

// StateServiceConfig supplies bootstrap sources owned below presentation.
type StateServiceConfig struct {
	Sessions interface {
		Session(context.Context, session.SessionRef) (session.Session, error)
	}
	Runtime    controlport.RuntimeStateReader
	Feeds      controlport.FeedRegistry
	MaxRetries int
}

// StateService assembles revision/boundary-consistent SessionState.
type StateService struct{ config StateServiceConfig }

// NewStateService constructs a typed reconnect bootstrap service.
func NewStateService(config StateServiceConfig) (*StateService, error) {
	if config.Sessions == nil || config.Runtime == nil || config.Feeds == nil {
		return nil, errors.New("controlclient: state service dependencies are required")
	}
	if config.MaxRetries <= 0 {
		config.MaxRetries = defaultStateRetries
	}
	return &StateService{config: config}, nil
}

// State returns one consistent bootstrap selected only by Session ID.
func (s *StateService) State(ctx context.Context, req controlport.StateRequest) (controlport.SessionState, error) {
	if s == nil {
		return controlport.SessionState{}, errors.New("controlclient: nil state service")
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return controlport.SessionState{}, session.ErrInvalidSession
	}
	ref := session.SessionRef{SessionID: sessionID}
	for range s.config.MaxRetries {
		before, err := s.config.Sessions.Session(ctx, ref)
		if err != nil {
			return controlport.SessionState{}, err
		}
		feed, err := s.config.Feeds.Session(before.SessionRef)
		if err != nil {
			return controlport.SessionState{}, err
		}
		if err := feed.Prime(ctx); err != nil {
			return controlport.SessionState{}, err
		}
		positionBefore, cursorBefore := feed.Boundary()
		runtimeState, err := s.config.Runtime.ControlClientRuntimeState(ctx, before.SessionRef)
		if err != nil {
			return controlport.SessionState{}, err
		}
		after, err := s.config.Sessions.Session(ctx, before.SessionRef)
		if err != nil {
			return controlport.SessionState{}, err
		}
		positionAfter, cursorAfter := feed.Boundary()
		if before.Revision != after.Revision || cursorBefore != cursorAfter {
			continue
		}
		_ = positionBefore
		return controlport.SessionState{
			ProtocolVersion:  schema.CurrentProtocolVersion,
			EnvelopeVersion:  controlport.EnvelopeVersion,
			APIVersion:       controlport.HTTPAPIVersion,
			SessionID:        after.SessionID,
			Revision:         after.Revision,
			WorkspaceKey:     after.WorkspaceKey,
			CWD:              after.CWD,
			Title:            after.Title,
			Metadata:         cloneAnyMap(after.Metadata),
			BoundaryCursor:   cursorAfter,
			BoundaryPosition: positionAfter,
			ResumeMode:       controlport.ResumeModeExact,
			Run:              runtimeState.Run,
			Controller:       after.Controller,
			Participants:     append([]session.ParticipantBinding(nil), after.Participants...),
			Approval:         runtimeState.Approval,
			Capabilities: controlport.ClientCapabilities{
				ClientManagedTerminal: false, CaelisTerminalStream: true,
				GoalBootstrapSupported: false, ManageLoopBootstrapSupported: false,
			},
		}, nil
	}
	return controlport.SessionState{}, controlport.ErrStateRevisionConflict
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	encoded, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil
	}
	return out
}
