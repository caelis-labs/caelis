// Package requestsource tracks the durable source actually observed by a chat
// attempt. It never reloads the Store to extend an assembled request's coverage.
package requestsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type trackerKey struct{}

// Tracker is owned by the foreground attempt. Append runs after persistence and
// before control returns to chat; background jobs receive cloned snapshots only.
type Tracker struct {
	events []*session.Event
}

// WithTracker installs the attempt's initial canonical source.
func WithTracker(ctx context.Context, events []*session.Event) (context.Context, *Tracker) {
	t := &Tracker{}
	t.Append(events...)
	return context.WithValue(ctx, trackerKey{}, t), t
}

// FromContext returns the attempt tracker, or nil for standalone chat agents.
func FromContext(ctx context.Context) *Tracker {
	t, _ := ctx.Value(trackerKey{}).(*Tracker)
	return t
}

// Append records only canonical model-visible events observed by this attempt.
func (t *Tracker) Append(events ...*session.Event) {
	if t == nil {
		return
	}
	for _, event := range events {
		if session.IsMainInvocationVisibleEvent(event) {
			t.events = append(t.events, session.CloneEvent(event))
		}
	}
}

// Events returns an isolated source snapshot for request assembly.
func (t *Tracker) Events() []*session.Event {
	return session.CloneEvents(t.events)
}

// Fingerprint binds exact JSON content, including message origins and media.
func Fingerprint(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("runtime: fingerprint compact source: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
