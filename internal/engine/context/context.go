// Package context rebuilds provider-visible model context from canonical
// session events.
package context

import (
	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
)

func Messages(events []session.Event) []model.Message {
	if len(events) == 0 {
		return nil
	}
	events = eventsFromLatestCompact(events)
	out := make([]model.Message, 0, len(events))
	for _, event := range events {
		if session.IsTransient(event) || event.Message == nil {
			continue
		}
		out = append(out, model.CloneMessage(*event.Message))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func SnapshotMessages(snapshot session.Snapshot) []model.Message {
	return Messages(snapshot.Events)
}

func eventsFromLatestCompact(events []session.Event) []session.Event {
	for i := len(events) - 1; i >= 0; i-- {
		if session.IsTransient(events[i]) {
			continue
		}
		if events[i].Type == session.EventCompact {
			return events[i:]
		}
	}
	return events
}
