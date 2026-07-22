package controlclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// StateServiceConfig supplies bootstrap sources owned below presentation.
type StateServiceConfig struct {
	Sessions interface {
		Session(context.Context, session.SessionRef) (session.Session, error)
	}
	Runtime RuntimeStateReader
	Feeds   FeedRegistry
}

// StateService assembles revision/boundary-consistent SessionState.
type StateService struct{ config StateServiceConfig }

// NewStateService constructs a typed reconnect bootstrap service.
func NewStateService(config StateServiceConfig) (*StateService, error) {
	if config.Sessions == nil || config.Runtime == nil || config.Feeds == nil {
		return nil, errors.New("controlclient: state service dependencies are required")
	}
	return &StateService{config: config}, nil
}

// State returns one consistent bootstrap selected only by Session ID. It uses
// the same reconnect cut as a live subscriber, then immediately releases the
// diagnostic subscription.
func (s *StateService) State(ctx context.Context, req StateRequest) (SessionState, error) {
	if s == nil {
		return SessionState{}, errors.New("controlclient: nil state service")
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return SessionState{}, session.ErrInvalidSession
	}
	result, err := s.Reconnect(ctx, ReconnectRequest{SessionID: sessionID})
	if err != nil {
		return SessionState{}, err
	}
	if result.Subscription != nil {
		_ = result.Subscription.Close()
	}
	return result.State, nil
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
