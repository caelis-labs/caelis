// Package controlclient defines transport-neutral client contracts owned by
// the Caelis Control layer.
package controlclient

import (
	"context"
	"errors"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

// ResumeMode describes how a Session subscription reconstructed its prefix.
type ResumeMode string

const (
	ResumeModeExact           ResumeMode = "exact"
	ResumeModeDurableFallback ResumeMode = "durable_fallback"
)

var ErrSlowConsumer = errors.New("controlclient: slow feed consumer")

// SubscribeRequest requests one authorized Session feed. Cursor is the only
// public resume identity; EventID and ProjectionID are never accepted here.
type SubscribeRequest struct {
	SessionID string `json:"session_id"`
	Cursor    string `json:"cursor,omitempty"`
}

// FeedSubscription is an independent view of a Session feed. Closing it does
// not cancel the underlying Runtime Turn or any child task.
type FeedSubscription interface {
	Events() <-chan eventstream.Envelope
	BackfillDone() <-chan struct{}
	Close() error
	Err() error
	LastCursor() string
}

// SubscribeResult reports the recovery guarantee and captured boundary for a
// newly created subscription.
type SubscribeResult struct {
	Subscription   FeedSubscription `json:"-"`
	Mode           ResumeMode       `json:"resume_mode"`
	TransientGap   bool             `json:"transient_gap,omitempty"`
	BoundaryCursor string           `json:"boundary_cursor,omitempty"`
}

// SessionFeed is the narrow Control-owned feed used by adapters and Surfaces.
type SessionFeed interface {
	Prime(context.Context) error
	Publish(eventstream.Envelope) error
	Subscribe(context.Context, SubscribeRequest) (SubscribeResult, error)
	SubscribeFromNow() (FeedSubscription, error)
	Attach(<-chan eventstream.Envelope)
	Boundary() (*eventstream.FeedPosition, string)
}

// FeedRegistry resolves Session feeds strictly by SessionRef.SessionID.
type FeedRegistry interface {
	Session(session.SessionRef) (SessionFeed, error)
}
