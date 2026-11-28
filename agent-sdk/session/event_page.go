package session

import "context"

const defaultEventPageLimit = 200

// EventReceiptReader optionally provides indexed lookup of a durable receipt by
// immutable Event ID, including mirror receipts. A missing ID returns nil.
type EventReceiptReader interface {
	EventReceipt(context.Context, SessionRef, string) (*Event, error)
}

// EventPageVisibility selects the durable visibility classes returned by one
// sequence page. Transient events have no durable Seq and are never returned.
type EventPageVisibility string

const (
	// EventPageCanonical returns canonical model/session history only.
	EventPageCanonical EventPageVisibility = "canonical"
	// EventPageClientReplay returns durable events with a client projection,
	// including completed reviewed approval facts.
	EventPageClientReplay EventPageVisibility = "client_replay"
	// EventPageAllDurable also returns approval, accounting and compaction receipts.
	EventPageAllDurable EventPageVisibility = "all_durable"
)

// EventPageRequest reads events strictly after AfterSeq and, when non-zero, no
// later than ThroughSeq. Limit counts returned events rather than skipped
// visibility classes.
type EventPageRequest struct {
	SessionRef SessionRef          `json:"session_ref"`
	AfterSeq   uint64              `json:"after_seq,omitempty"`
	ThroughSeq uint64              `json:"through_seq,omitempty"`
	Limit      int                 `json:"limit,omitempty"`
	Visibility EventPageVisibility `json:"visibility,omitempty"`
}

// EventPage is one forward sequence page. NextSeq is the highest source
// sequence consumed by the page, including skipped visibility classes.
type EventPage struct {
	Events  []*Event `json:"events,omitempty"`
	NextSeq uint64   `json:"next_seq,omitempty"`
	HasMore bool     `json:"has_more,omitempty"`
}

// PagedReader exposes bounded forward event reads without materializing an
// entire Session history.
type PagedReader interface {
	EventsPage(context.Context, EventPageRequest) (EventPage, error)
}

// EventCheckpoint is an atomic durable cut used by forward readers. Session
// and ThroughSeq are observed under the same store lock. LastClientReplayEvent
// is the last event at or before ThroughSeq that can project to a client feed;
// it lets callers derive a boundary without scanning the complete history.
type EventCheckpoint struct {
	Session               Session `json:"session"`
	ThroughSeq            uint64  `json:"through_seq,omitempty"`
	LastClientReplayEvent *Event  `json:"last_client_replay_event,omitempty"`
}

// EventCheckpointReader returns one immutable Session/event high-water cut.
// Implementations must not materialize the full event history to answer it.
type EventCheckpointReader interface {
	EventCheckpoint(context.Context, SessionRef) (EventCheckpoint, error)
}

// EventMetadata is content-free durable provenance. HasScope distinguishes an
// explicit empty scope from legacy records; reviewed decisions without a scope
// must be read by their sequence to resolve their original owner.
type EventMetadata struct {
	Seq              uint64 `json:"seq"`
	TurnID           string `json:"turn_id,omitempty"`
	HasScope         bool   `json:"has_scope,omitempty"`
	Child            bool   `json:"child,omitempty"`
	ReviewedApproval bool   `json:"reviewed_approval,omitempty"`
}

// EventMetadataPage contains ordered provenance anchors in the requested
// sequence/visibility range. Equal consecutive provenance may be coalesced;
// every change and reviewed approval is retained. NextSeq includes coalesced
// and filtered records. Metadata never substitutes for payload replay.
type EventMetadataPage struct {
	Events  []EventMetadata
	NextSeq uint64
}

// EventMetadataReader optionally accelerates structural history indexing.
// Consumers select their own windows; the Store owns only durable provenance.
type EventMetadataReader interface {
	EventMetadataPage(context.Context, EventPageRequest) (EventMetadataPage, error)
}

// NormalizeEventPageRequest applies stable defaults for store implementations.
func NormalizeEventPageRequest(req EventPageRequest) EventPageRequest {
	req.SessionRef = NormalizeSessionRef(req.SessionRef)
	if req.Limit <= 0 {
		req.Limit = defaultEventPageLimit
	}
	if req.Visibility == "" {
		req.Visibility = EventPageCanonical
	}
	return req
}

// EventMatchesPageVisibility reports whether a durable event belongs to one
// sequence-page visibility class.
func EventMatchesPageVisibility(event *Event, visibility EventPageVisibility) bool {
	if event == nil || IsTransient(event) {
		return false
	}
	switch visibility {
	case EventPageClientReplay:
		return IsClientReplayEvent(event)
	case EventPageAllDurable:
		return true
	case EventPageCanonical, "":
		return IsCanonicalHistoryEvent(event)
	default:
		return false
	}
}

// PageEvents selects one bounded page from an already ordered event slice.
// Stores with streaming persistence should apply the same rules while decoding
// so they do not first load the entire log.
func PageEvents(events []*Event, req EventPageRequest) EventPage {
	req = NormalizeEventPageRequest(req)
	out := EventPage{NextSeq: req.AfterSeq}
	for _, event := range events {
		if event == nil || event.Seq <= req.AfterSeq {
			continue
		}
		if req.ThroughSeq > 0 && event.Seq > req.ThroughSeq {
			break
		}
		if EventMatchesPageVisibility(event, req.Visibility) && len(out.Events) >= req.Limit {
			out.HasMore = true
			break
		}
		out.NextSeq = event.Seq
		if EventMatchesPageVisibility(event, req.Visibility) {
			out.Events = append(out.Events, CanonicalizeEvent(event))
		}
	}
	return out
}
