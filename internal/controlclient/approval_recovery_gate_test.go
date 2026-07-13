package controlclient

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type blockingApprovalRecoveryStore struct {
	started chan struct{}
	release chan struct{}
	err     error
}

func (s *blockingApprovalRecoveryStore) ListSessions(context.Context, session.ListSessionsRequest) (session.SessionList, error) {
	close(s.started)
	<-s.release
	return session.SessionList{}, s.err
}

func (*blockingApprovalRecoveryStore) EventsPage(context.Context, session.EventPageRequest) (session.EventPage, error) {
	return session.EventPage{}, nil
}

func (*blockingApprovalRecoveryStore) AppendEvent(context.Context, session.AppendEventRequest) (*session.Event, error) {
	return nil, nil
}

func TestApprovalRecoveryGateBlocksTurnsWithoutBlockingStartup(t *testing.T) {
	store := &blockingApprovalRecoveryStore{started: make(chan struct{}), release: make(chan struct{})}
	gate := NewApprovalRecoveryGate(store)
	gate.Start(context.Background())
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("approval recovery did not start")
	}

	waited := make(chan error, 1)
	go func() { waited <- gate.Wait(context.Background()) }()
	select {
	case err := <-waited:
		t.Fatalf("Wait() returned before recovery completed: %v", err)
	default:
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := gate.Wait(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait(canceled) error = %v", err)
	}

	close(store.release)
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("Wait() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait() did not return after recovery completed")
	}
}

func TestApprovalRecoveryGateRetainsSweepFailure(t *testing.T) {
	want := errors.New("recovery failed")
	store := &blockingApprovalRecoveryStore{started: make(chan struct{}), release: make(chan struct{}), err: want}
	gate := NewApprovalRecoveryGate(store)
	gate.Start(context.Background())
	<-store.started
	close(store.release)
	if err := gate.Wait(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Wait() error = %v, want %v", err, want)
	}
	if err := gate.Wait(context.Background()); !errors.Is(err, want) {
		t.Fatalf("second Wait() error = %v, want retained %v", err, want)
	}
}
