package session_test

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// Compile-time contracts prove external adapters can implement and request
// only the session capabilities they consume.
var _ session.Reader = externalReader{}
var _ session.EventAppender = externalAppender{}
var _ session.StateReader = externalStateReader{}

type externalReader struct{}

func (externalReader) Session(context.Context, session.SessionRef) (session.Session, error) {
	return session.Session{}, nil
}

func (externalReader) Events(context.Context, session.EventsRequest) ([]*session.Event, error) {
	return nil, nil
}

type externalAppender struct{}

func (externalAppender) AppendEvent(context.Context, session.AppendEventRequest) (*session.Event, error) {
	return nil, nil
}

type externalStateReader struct{}

func (externalStateReader) SnapshotState(context.Context, session.SessionRef) (map[string]any, error) {
	return nil, nil
}
