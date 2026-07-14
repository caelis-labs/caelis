package controlclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlport "github.com/caelis-labs/caelis/ports/controlclient"
)

// StateServiceConfig supplies bootstrap sources owned below presentation.
type StateServiceConfig struct {
	Sessions interface {
		Session(context.Context, session.SessionRef) (session.Session, error)
	}
	Runtime controlport.RuntimeStateReader
	Feeds   controlport.FeedRegistry
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
func (s *StateService) State(ctx context.Context, req controlport.StateRequest) (controlport.SessionState, error) {
	if s == nil {
		return controlport.SessionState{}, errors.New("controlclient: nil state service")
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return controlport.SessionState{}, session.ErrInvalidSession
	}
	result, err := s.Reconnect(ctx, controlport.ReconnectRequest{SessionID: sessionID})
	if err != nil {
		return controlport.SessionState{}, err
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
