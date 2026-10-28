package compact

import (
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/requestsource"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// Coverage binds a frozen model request to its dispatch source and the raw delta
// summarized by compaction. SourceThroughSeq may include a checkpoint appended
// after the older raw tail identified by SummarizedThroughSeq.
type Coverage struct {
	SourceThroughSeq     uint64
	SummarizedThroughSeq uint64
	SummarizedThroughID  string
	// SourceEventCount counts the raw delta after the latest checkpoint's
	// coverage, not all events in the dispatch source prefix.
	SourceEventCount int
	// Hashes are SHA-256 of the exact JSON source prefix and complete frozen
	// provider request. Request-local source metadata is excluded from JSON.
	SourcePrefixHash  string
	FrozenRequestHash string
	// RequireInContext preserves strict generation policy across handoff.
	// Either this flag or Request.RequireInContext enforces the requirement.
	RequireInContext bool
}

// CoverageError reports an invalid or incomplete compaction source binding.
type CoverageError struct {
	Reason string
}

func (e *CoverageError) Error() string {
	return "agent-sdk/runtime/compact: invalid coverage: " + e.Reason
}

// Retryable is false because another generation cannot repair a source binding.
func (*CoverageError) Retryable() bool { return false }

// ValidateCoverage verifies strict in-context source bindings before generation,
// including requests whose source contains no compactable events.
func (req Request) ValidateCoverage() error {
	if !req.RequireInContext && (req.Coverage == nil || !req.Coverage.RequireInContext) {
		return nil
	}
	invalid := func(reason string) error { return &CoverageError{Reason: reason} }
	if req.InContextRequest == nil {
		return invalid("in-context request is required")
	}
	if req.Coverage == nil {
		return invalid("coverage is required")
	}
	coverage := req.Coverage
	sourceHash, err := requestsource.Fingerprint(req.Events)
	if err != nil {
		return invalid(fmt.Sprintf("source fingerprint: %v", err))
	}
	if sourceHash != coverage.SourcePrefixHash {
		return invalid("source prefix hash mismatch")
	}
	frozenHash, err := requestsource.Fingerprint(req.InContextRequest)
	if err != nil {
		return invalid(fmt.Sprintf("frozen request fingerprint: %v", err))
	}
	if frozenHash != coverage.FrozenRequestHash {
		return invalid("frozen request hash mismatch")
	}
	if session.LastEventSeq(req.Events) != coverage.SourceThroughSeq {
		return invalid("source sequence mismatch")
	}
	delta := EventsAfterLatestCompact(req.Events)
	if len(delta) != coverage.SourceEventCount {
		return invalid("source event count mismatch")
	}
	lastID := ""
	for i := len(delta) - 1; i >= 0; i-- {
		if id := strings.TrimSpace(delta[i].ID); id != "" {
			lastID = id
			break
		}
	}
	if lastID != coverage.SummarizedThroughID {
		return invalid("summarized event ID mismatch")
	}
	if session.LastEventSeq(delta) != coverage.SummarizedThroughSeq {
		return invalid("summarized sequence mismatch")
	}
	if coverage.SummarizedThroughSeq > coverage.SourceThroughSeq {
		return invalid("summarized sequence exceeds source sequence")
	}
	return nil
}
