package appserver

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

const (
	maxFeedReplacementEvents = 8192
	maxFeedReplacementBytes  = 32 << 20
)

// FeedDeliveryAssembler applies the common append/replacement transaction
// contract for Session consumers. Replacement pages remain private until end.
type FeedDeliveryAssembler struct {
	snapshotID string
	nextPage   uint32
	bytes      int
	events     []eventstream.Envelope
}

func (a *FeedDeliveryAssembler) Accept(delivery FeedDelivery) ([]eventstream.Envelope, bool, error) {
	if a == nil {
		return nil, false, errorcode.New(errorcode.InvalidArgument, "Session delivery assembler is required")
	}
	switch delivery.Kind {
	case FeedDeliveryAppendPage:
		if a.Pending() || delivery.SnapshotID != "" {
			return nil, false, invalidFeedDelivery("append")
		}
		switch delivery.Source {
		case FeedSourceExact:
			if !validExactFeedAppend(delivery) {
				return nil, false, invalidFeedDelivery("exact append")
			}
		case FeedSourceResult:
			if delivery.Page != 0 || delivery.NextCursor != "" || len(delivery.Events) != 1 || !validTerminalResult(delivery.Events[0]) {
				return nil, false, invalidFeedDelivery("terminal result")
			}
		default:
			return nil, false, invalidFeedDelivery("append source")
		}
		return cloneEnvelopes(delivery.Events), false, nil
	case FeedDeliverySync:
		if delivery.Source != FeedSourceExact || a.Pending() || len(delivery.Events) != 0 || delivery.SnapshotID != "" {
			return nil, false, invalidFeedDelivery("sync")
		}
		return nil, false, nil
	case FeedDeliveryReplaceBegin:
		if delivery.Source != FeedSourceReplacement || a.Pending() || strings.TrimSpace(delivery.SnapshotID) == "" || delivery.Page != 0 || len(delivery.Events) != 0 || delivery.NextCursor != "" {
			return nil, false, invalidFeedDelivery("replacement begin")
		}
		a.snapshotID = delivery.SnapshotID
		return nil, false, nil
	case FeedDeliveryReplacePage:
		if delivery.Source != FeedSourceReplacement || !a.Pending() || delivery.SnapshotID != a.snapshotID || delivery.Page != a.nextPage || delivery.NextCursor != "" || !cursorlessReplacementEnvelopes(delivery.Events) {
			return nil, false, invalidFeedDelivery("replacement page")
		}
		if len(a.events)+len(delivery.Events) > maxFeedReplacementEvents {
			a.Reset()
			return nil, false, errorcode.New(errorcode.ResourceExhausted, "Session replacement exceeds event limit")
		}
		for _, event := range delivery.Events {
			raw, err := json.Marshal(event)
			if err != nil {
				a.Reset()
				return nil, false, err
			}
			if a.bytes+len(raw) > maxFeedReplacementBytes {
				a.Reset()
				return nil, false, errorcode.New(errorcode.ResourceExhausted, "Session replacement exceeds byte limit")
			}
			a.bytes += len(raw)
		}
		a.events = append(a.events, cloneEnvelopes(delivery.Events)...)
		a.nextPage++
		return nil, false, nil
	case FeedDeliveryReplaceEnd:
		if delivery.Source != FeedSourceReplacement || !a.Pending() || delivery.SnapshotID != a.snapshotID || delivery.Page != a.nextPage || len(delivery.Events) != 0 || delivery.NextCursor != "" {
			return nil, false, invalidFeedDelivery("replacement end")
		}
		events := cloneEnvelopes(a.events)
		a.Reset()
		return events, true, nil
	default:
		return nil, false, invalidFeedDelivery("kind")
	}
}

func validExactFeedAppend(delivery FeedDelivery) bool {
	if strings.TrimSpace(delivery.NextCursor) == "" {
		return false
	}
	for _, envelope := range delivery.Events {
		if strings.TrimSpace(envelope.Cursor) == "" || envelope.Position == nil || envelope.Position.Validate() != nil {
			return false
		}
	}
	return len(delivery.Events) == 0 || delivery.Events[len(delivery.Events)-1].Cursor == delivery.NextCursor
}

func cursorlessReplacementEnvelopes(envelopes []eventstream.Envelope) bool {
	for _, envelope := range envelopes {
		if strings.TrimSpace(envelope.Cursor) != "" {
			return false
		}
		if envelope.Position != nil && envelope.Position.Validate() != nil {
			return false
		}
	}
	return true
}

func validTerminalResult(envelope eventstream.Envelope) bool {
	return eventstream.IsTurnTerminalLifecycle(envelope) &&
		strings.TrimSpace(envelope.Cursor) == "" && envelope.Position == nil &&
		envelope.Delivery != nil && envelope.Delivery.Mode == eventstream.DeliveryTransient
}

func (a *FeedDeliveryAssembler) Pending() bool { return a != nil && a.snapshotID != "" }

func (a *FeedDeliveryAssembler) Reset() {
	if a == nil {
		return
	}
	a.snapshotID = ""
	a.nextPage = 0
	a.bytes = 0
	a.events = nil
}

func cloneEnvelopes(events []eventstream.Envelope) []eventstream.Envelope {
	out := make([]eventstream.Envelope, 0, len(events))
	for _, event := range events {
		out = append(out, eventstream.CloneEnvelope(event))
	}
	return out
}

func invalidFeedDelivery(part string) error {
	return errorcode.New(errorcode.InvalidArgument, "invalid Session delivery: "+part)
}
