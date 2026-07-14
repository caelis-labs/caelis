// Package controlclient owns private Control implementation for the Caelis
// client protocol. Transport-neutral public contracts live in ports/controlclient.
package controlclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	// FallbackSourceEventID is used only when Origin.SourceEventID collides
	// with different durable content. It lets upgraded brokers first dedupe the
	// legacy child identity, while a genuinely distinct continuation whose
	// cursor restarted can move to its versioned physical-source identity.
	FallbackSourceEventID string
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
	ref, event, err := prepareChildRecord(req, false)
	if err != nil {
		return nil, err
	}
	stored, err := r.appender.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef:    ref,
		MutationGuard: childRecordMutationGuard(),
		Event:         event,
	})
	if !errors.Is(err, session.ErrEventConflict) ||
		strings.TrimSpace(req.FallbackSourceEventID) == "" ||
		strings.TrimSpace(req.FallbackSourceEventID) == strings.TrimSpace(req.Origin.SourceEventID) {
		return stored, err
	}
	ref, event, prepareErr := prepareChildRecord(req, true)
	if prepareErr != nil {
		return nil, prepareErr
	}
	return r.appender.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef:    ref,
		MutationGuard: childRecordMutationGuard(),
		Event:         event,
	})
}

// RecordBatch validates one child snapshot and appends all of its semantic
// mirrors atomically when the Session store supports EventBatchService. Legacy
// appenders retain ordered one-at-a-time behavior after the full input batch
// has passed Control-side validation.
func (r *ChildRecorder) RecordBatch(ctx context.Context, requests []ChildRecordRequest) ([]*session.Event, error) {
	if r == nil || r.appender == nil {
		return nil, fmt.Errorf("internal/controlclient: child event appender is required")
	}
	if len(requests) == 0 {
		return nil, nil
	}

	if batch, ok := r.appender.(session.EventBatchService); ok {
		ref, events, err := prepareChildRecordBatch(requests, false)
		if err != nil {
			return nil, err
		}
		stored, err := batch.AppendEvents(ctx, session.AppendEventsRequest{
			SessionRef:    ref,
			MutationGuard: childRecordMutationGuard(),
			Events:        events,
		})
		if !errors.Is(err, session.ErrEventConflict) || !childRecordBatchHasFallback(requests) {
			return stored, err
		}
		ref, events, prepareErr := prepareChildRecordBatch(requests, true)
		if prepareErr != nil {
			return nil, prepareErr
		}
		return batch.AppendEvents(ctx, session.AppendEventsRequest{
			SessionRef:    ref,
			MutationGuard: childRecordMutationGuard(),
			Events:        events,
		})
	}

	// Preserve the legacy appender contract: reject an invalid or cross-Session
	// snapshot before the first one-at-a-time append can mutate durable state.
	if _, _, err := prepareChildRecordBatch(requests, false); err != nil {
		return nil, err
	}
	stored := make([]*session.Event, 0, len(requests))
	for _, req := range requests {
		next, err := r.Record(ctx, req)
		if err != nil {
			return nil, err
		}
		stored = append(stored, next)
	}
	return stored, nil
}

func prepareChildRecordBatch(requests []ChildRecordRequest, fallback bool) (session.SessionRef, []*session.Event, error) {
	ref := session.NormalizeSessionRef(requests[0].SessionRef)
	events := make([]*session.Event, 0, len(requests))
	for _, req := range requests {
		nextRef, event, err := prepareChildRecord(req, fallback)
		if err != nil {
			return session.SessionRef{}, nil, err
		}
		if nextRef.SessionID != ref.SessionID {
			return session.SessionRef{}, nil, fmt.Errorf(
				"internal/controlclient: child event batch spans sessions %q and %q: %w",
				ref.SessionID, nextRef.SessionID, session.ErrInvalidSession,
			)
		}
		events = append(events, event)
	}
	return ref, events, nil
}

func childRecordBatchHasFallback(requests []ChildRecordRequest) bool {
	for _, req := range requests {
		if strings.TrimSpace(req.FallbackSourceEventID) != "" &&
			strings.TrimSpace(req.FallbackSourceEventID) != strings.TrimSpace(req.Origin.SourceEventID) {
			return true
		}
	}
	return false
}

func prepareChildRecord(req ChildRecordRequest, fallback bool) (session.SessionRef, *session.Event, error) {
	ref := session.NormalizeSessionRef(req.SessionRef)
	if req.Event == nil {
		return ref, nil, session.ErrInvalidEvent
	}
	origin := session.CloneEventChildOrigin(req.Origin)
	if fallback {
		if sourceID := strings.TrimSpace(req.FallbackSourceEventID); sourceID != "" {
			origin.SourceEventID = sourceID
		}
	}
	if err := session.ValidateEventChildOrigin(origin); err != nil {
		return ref, nil, err
	}
	event := session.CloneEvent(req.Event)
	event.Visibility = session.VisibilityMirror
	event.ChildOrigin = &origin
	event.ID = childMirrorIdentity(ref.SessionID, origin)
	event.IdempotencyKey = event.ID
	return ref, event, nil
}

func childRecordMutationGuard() session.MutationGuard {
	return session.ControlMutationGuard(session.ControlMutationPurposeParticipant)
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
