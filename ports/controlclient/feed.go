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

// FeedGapError reports a recoverable continuation loss together with the only
// public token a caller needs to retry. It is used both for a splice whose
// bounded ring was overtaken and for an ordinary live slow-consumer cutoff.
type FeedGapError struct {
	Cause        error
	RetryCursor  string
	Mode         ResumeMode
	TransientGap bool
}

func (e *FeedGapError) Error() string {
	if e == nil || e.Cause == nil {
		return "controlclient: recoverable feed gap"
	}
	return e.Cause.Error()
}

func (e *FeedGapError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// SubscribeRequest requests one authorized Session feed. Cursor is the only
// public resume identity; EventID and ProjectionID are never accepted here.
type SubscribeRequest struct {
	SessionID string `json:"session_id"`
	Cursor    string `json:"cursor,omitempty"`
}

// FeedSubscription is an independent view of a Session feed. Closing it does
// not cancel the underlying Runtime Turn or any child task.
type FeedSubscription interface {
	// Backfill streams the bounded-memory captured prefix. It closes before any
	// Envelope becomes observable on Events.
	Backfill() <-chan eventstream.Envelope
	// Events is the live continuation after Backfill has closed.
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
	// BoundaryPosition is the decoded in-process form of BoundaryCursor. The
	// Cursor remains the sole public resume token.
	BoundaryPosition *eventstream.FeedPosition `json:"-"`
	// Backfill is the exact captured prefix ending at BoundaryCursor. It is an
	// in-process compatibility handoff for finite test feeds and is never
	// encoded on the wire. Production brokers stream through Subscription and
	// close BackfillDone without materializing the full prefix here.
	Backfill []eventstream.Envelope `json:"-"`
}

// SessionFeed is the narrow Control-owned feed used by adapters and Surfaces.
type SessionFeed interface {
	Prime(context.Context) error
	Publish(eventstream.Envelope) error
	Subscribe(context.Context, SubscribeRequest) (SubscribeResult, error)
	// SubscribeFromNow is the internal active-Turn handoff. Implementations
	// register its no-history boundary before Turn start and bind its lifetime
	// to the supplied context. Ordinary fanout retains the bounded
	// slow-consumer disconnect policy: one paused Surface must never block the
	// Session sequencer or a sibling ingress.
	SubscribeFromNow(context.Context) (FeedSubscription, error)
	// Attach publishes one finite ingress and reports an asynchronous delivery
	// failure. A closed channel without a value means the ingress completed.
	Attach(<-chan eventstream.Envelope) <-chan error
	// AttachTo binds one finite Turn ingress to its prepared internal
	// subscription. Globally accepted Envelopes are held for that target in
	// sequence under event/byte bounds, then delivered without holding the
	// Session sequencer. Only target delivery may wait, and only through the
	// bounded stall timeout. Target teardown detaches its delivery while the
	// attachment continues publishing the ingress untargeted for siblings.
	AttachTo(FeedSubscription, <-chan eventstream.Envelope) <-chan error
	Boundary() (*eventstream.FeedPosition, string)
}

// FeedRegistry resolves Session feeds strictly by SessionRef.SessionID.
type FeedRegistry interface {
	Session(session.SessionRef) (SessionFeed, error)
}
