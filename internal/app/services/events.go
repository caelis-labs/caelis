package services

import (
	"context"
	"errors"
	"strings"

	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	"github.com/OnslaughtSnail/caelis/core/session"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

type EventService struct {
	services Services
}

type EventReplayRequest struct {
	SessionRef       session.Ref    `json:"session_ref,omitempty"`
	After            session.Cursor `json:"after,omitempty"`
	Limit            int            `json:"limit,omitempty"`
	IncludeTransient bool           `json:"include_transient,omitempty"`
}

func (s EventService) Replay(ctx context.Context, req EventReplayRequest) (<-chan appviewmodel.SessionEventEnvelope, error) {
	if s.services.engine == nil {
		return nil, errors.New("app/services: runtime engine is required")
	}
	ref := defaultSessionRef(s.services.Runtime(), req.SessionRef)
	stream, err := s.services.engine.Replay(ctx, coreruntime.ReplayRequest{
		SessionRef:       ref,
		After:            req.After,
		Limit:            req.Limit,
		IncludeTransient: req.IncludeTransient,
	})
	if err != nil {
		return nil, err
	}
	return s.Project(ctx, stream), nil
}

func (s EventService) SubscribeTurn(ctx context.Context, turn coreruntime.Turn) (<-chan appviewmodel.SessionEventEnvelope, error) {
	if turn == nil {
		return nil, errors.New("app/services: active turn is required")
	}
	return s.Project(ctx, turn.Events()), nil
}

func (s EventService) Project(ctx context.Context, stream <-chan coreruntime.EventEnvelope) <-chan appviewmodel.SessionEventEnvelope {
	if ctx == nil {
		ctx = context.Background()
	}
	out := make(chan appviewmodel.SessionEventEnvelope, 32)
	if stream == nil {
		close(out)
		return out
	}
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case env, ok := <-stream:
				if !ok {
					return
				}
				projected, ok := projectRuntimeEventEnvelope(env)
				if !ok {
					continue
				}
				select {
				case out <- projected:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

func projectRuntimeEventEnvelope(env coreruntime.EventEnvelope) (appviewmodel.SessionEventEnvelope, bool) {
	if errText := strings.TrimSpace(env.Err); errText != "" {
		return appviewmodel.EventEnvelopeFromError(errText), true
	}
	if env.Event.Type == "" {
		return appviewmodel.SessionEventEnvelope{}, false
	}
	return appviewmodel.EventEnvelopeFromSession(strings.TrimSpace(string(env.Cursor)), env.Event), true
}
