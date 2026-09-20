package session

import "context"

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
	HasMore bool
}

// EventMetadataReader optionally accelerates structural history indexing.
// Consumers select their own windows; the Store owns only durable provenance.
type EventMetadataReader interface {
	EventMetadataPage(context.Context, EventPageRequest) (EventMetadataPage, error)
}

// RunJournalSnapshot contains the latest Run record and, while it is waiting
// for approval, its latest pause token when present.
// An empty RunID selects the Run at the greatest event sequence; an explicit
// RunID selects its greatest revision, with the earliest sequence breaking ties.
type RunJournalSnapshot struct {
	Run   *ExecutionRecord
	Pause *PauseToken
}

// RunJournalReader optionally reads execution state without loading transcript
// content. Runtime supports other Stores through the base Events query.
type RunJournalReader interface {
	RunJournal(context.Context, SessionRef, string) (RunJournalSnapshot, error)
}
