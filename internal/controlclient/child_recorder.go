// Package controlclient owns private Control implementation for the Caelis
// client protocol. Transport-neutral public contracts live in ports/controlclient.
package controlclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// ChildRecordRequest describes one normalized child semantic event before it
// is durably mirrored into the parent Session.
type ChildRecordRequest struct {
	SessionRef session.SessionRef
	Event      *session.Event
	Origin     session.EventChildOrigin
}

// ChildRecorder appends normalized child events as durable VisibilityMirror
// facts before a client projection is published.
type ChildRecorder struct {
	appender session.EventAppender
}

// NewChildRecorder constructs one durable child semantic recorder.
func NewChildRecorder(appender session.EventAppender) *ChildRecorder {
	return &ChildRecorder{appender: appender}
}

// Record appends or deduplicates one child mirror. Reusing the stable source
// identity with changed semantics is reported by the Session store as an event
// conflict.
func (r *ChildRecorder) Record(ctx context.Context, req ChildRecordRequest) (*session.Event, error) {
	if r == nil || r.appender == nil {
		return nil, fmt.Errorf("internal/controlclient: child event appender is required")
	}
	if req.Event == nil {
		return nil, session.ErrInvalidEvent
	}
	origin := session.CloneEventChildOrigin(req.Origin)
	if err := session.ValidateEventChildOrigin(origin); err != nil {
		return nil, err
	}
	event := session.CloneEvent(req.Event)
	event.Visibility = session.VisibilityMirror
	event.ChildOrigin = &origin
	event.ID = childMirrorIdentity(req.SessionRef.SessionID, origin)
	event.IdempotencyKey = event.ID
	return r.appender.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef:    req.SessionRef,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeParticipant),
		Event:         event,
	})
}

func childMirrorIdentity(sessionID string, origin session.EventChildOrigin) string {
	identity := strings.Join([]string{
		strings.TrimSpace(sessionID),
		string(origin.Scope),
		strings.TrimSpace(origin.ScopeID),
		strings.TrimSpace(origin.TaskID),
		strings.TrimSpace(origin.DelegationID),
		strings.TrimSpace(origin.ParticipantID),
		strings.TrimSpace(origin.ACPSessionID),
		strings.TrimSpace(origin.SourceEventID),
		strings.TrimSpace(origin.ParentTool.CallID),
		strings.TrimSpace(origin.ParentTool.Name),
	}, "\x00")
	digest := sha256.Sum256([]byte(identity))
	return "child-mirror:" + hex.EncodeToString(digest[:])
}
