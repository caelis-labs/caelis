package appserver

import (
	"encoding/json"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

const (
	// feedReplacementPageEventLimit and feedReplacementPageByteLimit bound one
	// canonical ReplacePage SSE payload. They stay well below the HTTP client's
	// 8 MiB scanner cap and the assembler hard caps (8192 events / 32 MiB). A
	// single envelope larger than the transport byte bound is still emitted
	// alone when it fits the assembler byte cap.
	feedReplacementPageEventLimit = 256
	feedReplacementPageByteLimit  = 1 << 20
)

type feedReplacementPageBuilder struct {
	snapshotID string
	maxEvents  int
	maxBytes   int
	page       uint32
	pending    []eventstream.Envelope
	bytes      int
}

func newFeedReplacementPageBuilder(snapshotID string) *feedReplacementPageBuilder {
	return &feedReplacementPageBuilder{
		snapshotID: snapshotID,
		maxEvents:  feedReplacementPageEventLimit,
		maxBytes:   feedReplacementPageByteLimit,
	}
}

func (b *feedReplacementPageBuilder) eventLimit() int {
	limit := b.maxEvents
	if limit <= 0 {
		limit = feedReplacementPageEventLimit
	}
	if limit > maxFeedReplacementPageEvents {
		return maxFeedReplacementPageEvents
	}
	return limit
}

func (b *feedReplacementPageBuilder) byteLimit() int {
	limit := b.maxBytes
	if limit <= 0 {
		limit = feedReplacementPageByteLimit
	}
	if limit > maxFeedReplacementPageBytes {
		return maxFeedReplacementPageBytes
	}
	return limit
}

func (b *feedReplacementPageBuilder) nextPage() uint32 {
	if b == nil {
		return 0
	}
	return b.page
}

// add records one cursorless envelope. If the current page must be emitted
// first to stay inside the transport bounds, that page is returned.
func (b *feedReplacementPageBuilder) add(envelope eventstream.Envelope) (FeedDelivery, bool, error) {
	if b == nil {
		return FeedDelivery{}, false, errorcode.New(errorcode.InvalidArgument, "controlclient: Session replacement page builder is required")
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return FeedDelivery{}, false, err
	}
	if len(raw) > maxFeedReplacementPageBytes {
		return FeedDelivery{}, false, errorcode.New(errorcode.ResourceExhausted, "controlclient: Session replacement page exceeds byte limit")
	}
	if len(b.pending) > 0 && (len(b.pending) >= b.eventLimit() || b.bytes+len(raw) > b.byteLimit()) {
		flushed := b.takePage()
		b.pending = append(b.pending, eventstream.CloneEnvelope(envelope))
		b.bytes = len(raw)
		return flushed, true, nil
	}
	b.pending = append(b.pending, eventstream.CloneEnvelope(envelope))
	b.bytes += len(raw)
	return FeedDelivery{}, false, nil
}

func (b *feedReplacementPageBuilder) flush() (FeedDelivery, bool) {
	if b == nil || len(b.pending) == 0 {
		return FeedDelivery{}, false
	}
	return b.takePage(), true
}

func (b *feedReplacementPageBuilder) takePage() FeedDelivery {
	delivery := FeedDelivery{
		Kind:       FeedDeliveryReplacePage,
		Source:     FeedSourceReplacement,
		SnapshotID: b.snapshotID,
		Page:       b.page,
		Events:     b.pending,
	}
	b.page++
	b.pending = nil
	b.bytes = 0
	return delivery
}
