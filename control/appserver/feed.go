package appserver

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

type FeedSourceClass string

const (
	FeedSourceExact       FeedSourceClass = "exact"
	FeedSourceReplacement FeedSourceClass = "replacement"
	// FeedSourceResult is one cursorless terminal semantic fallback. It is
	// neither replayable history nor an exact spool record.
	FeedSourceResult FeedSourceClass = "result"
)

type FeedDeliveryKind string

const (
	FeedDeliveryReplaceBegin FeedDeliveryKind = "replace_begin"
	FeedDeliveryReplacePage  FeedDeliveryKind = "replace_page"
	FeedDeliveryReplaceEnd   FeedDeliveryKind = "replace_end"
	FeedDeliveryAppendPage   FeedDeliveryKind = "append_page"
	FeedDeliverySync         FeedDeliveryKind = "sync"
)

// FeedDelivery is one explicit Session projection transaction unit. Exact
// spool records append; canonical replay is a bounded replacement that a
// consumer must keep off-screen until the matching end marker. Result is one
// cursorless terminal semantic fallback used only when the spool is unavailable.
type FeedDelivery struct {
	Kind       FeedDeliveryKind       `json:"kind"`
	Source     FeedSourceClass        `json:"source"`
	SnapshotID string                 `json:"snapshot_id,omitempty"`
	Page       uint32                 `json:"page,omitempty"`
	Events     []eventstream.Envelope `json:"events,omitempty"`
	NextCursor string                 `json:"next_cursor,omitempty"`
}

// SubscribeRequest requests one authorized Session feed. Cursor is the only
// public resume identity; EventID and ProjectionID are never accepted here.
type SubscribeRequest struct {
	SessionID string `json:"session_id"`
	Cursor    string `json:"cursor,omitempty"`
}

// FeedSubscription is an independent view of a Session feed. Closing it does
// not cancel the underlying Runtime Turn or any child task. Consumers commit
// Delivery.NextCursor only after successfully applying that delivery.
type FeedSubscription interface {
	Deliveries() <-chan FeedDelivery
	Close() error
	Err() error
}

// SubscribeResult reports the captured boundary for a newly created
// subscription. A valid cursor prefers exact spool resume; if its cache bytes
// no longer exist, the new attachment begins with one atomic canonical
// replacement. Consumers that cannot replace already-presented output must
// reject that transaction at their own irreversible presentation boundary.
type SubscribeResult struct {
	Subscription   FeedSubscription `json:"-"`
	BoundaryCursor string           `json:"boundary_cursor,omitempty"`
	// BoundaryPosition is the decoded in-process form of BoundaryCursor. The
	// Cursor remains the sole public resume token.
	BoundaryPosition *eventstream.FeedPosition `json:"-"`
}

// SessionFeed is the narrow Control-owned feed used by adapters and Surfaces.
type SessionFeed interface {
	Prime(context.Context) error
	Publish(eventstream.Envelope) error
	Subscribe(context.Context, SubscribeRequest) (SubscribeResult, error)
	Boundary() (*eventstream.FeedPosition, string)
}

// FeedRegistry resolves Session feeds strictly by SessionRef.SessionID.
type FeedRegistry interface {
	Session(session.SessionRef) (SessionFeed, error)
}

// FeedRegistryLifecycle closes Control-owned delivery resources when their
// product address becomes permanently unavailable. It is deliberately
// separate from FeedRegistry so read-only consumers do not gain lifecycle
// authority.
type FeedRegistryLifecycle interface {
	FeedRegistry
	CloseSession(context.Context, session.SessionRef) error
	Close() error
}
