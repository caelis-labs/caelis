package taskstream

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

const (
	maxReplacementEvents = 8192
	maxReplacementBytes  = 32 << 20
)

// DeliveryAssembler validates Task delivery ordering and keeps replacement
// pages invisible until their matching end marker. It is intentionally owned
// by the Control client boundary so every Surface applies the same transaction
// semantics without teaching agent-sdk how output is consumed.
type DeliveryAssembler struct {
	snapshotID string
	nextPage   uint32
	bytes      int
	events     []eventstream.Envelope
}

// Accept returns immediately displayable events. replacement is true only
// when the returned events are a complete replacement snapshot.
func (a *DeliveryAssembler) Accept(delivery Delivery) (events []eventstream.Envelope, replacement bool, err error) {
	if a == nil {
		return nil, false, errorcode.New(errorcode.InvalidArgument, "Task delivery assembler is required")
	}
	switch delivery.Kind {
	case DeliveryAppendPage:
		if delivery.Source != SourceExact || a.Pending() || delivery.SnapshotID != "" || !validExactTaskAppend(delivery) {
			return nil, false, invalidTaskDelivery("exact append")
		}
		return append([]eventstream.Envelope(nil), delivery.Events...), false, nil
	case DeliveryStatus:
		if delivery.Source != SourceStatus || a.Pending() || len(delivery.Events) != 0 || delivery.SnapshotID != "" || delivery.NextCursor != "" {
			return nil, false, invalidTaskDelivery("status")
		}
		return nil, false, nil
	case DeliveryReplaceBegin:
		if delivery.Source != SourceReplacement || a.Pending() || strings.TrimSpace(delivery.SnapshotID) == "" || delivery.Page != 0 || len(delivery.Events) != 0 || delivery.NextCursor != "" {
			return nil, false, invalidTaskDelivery("replacement begin")
		}
		a.snapshotID = delivery.SnapshotID
		a.nextPage = 0
		a.bytes = 0
		a.events = nil
		return nil, false, nil
	case DeliveryReplacePage:
		if delivery.Source != SourceReplacement || !a.Pending() || delivery.SnapshotID != a.snapshotID || delivery.Page != a.nextPage || delivery.NextCursor != "" || !nonResumableTaskEnvelopes(delivery.Events) {
			return nil, false, invalidTaskDelivery("replacement page")
		}
		if len(a.events)+len(delivery.Events) > maxReplacementEvents {
			a.Reset()
			return nil, false, errorcode.New(errorcode.ResourceExhausted, "Task replacement exceeds event limit")
		}
		for _, event := range delivery.Events {
			raw, marshalErr := json.Marshal(event)
			if marshalErr != nil {
				a.Reset()
				return nil, false, fmt.Errorf("encode Task replacement event: %w", marshalErr)
			}
			if a.bytes+len(raw) > maxReplacementBytes {
				a.Reset()
				return nil, false, errorcode.New(errorcode.ResourceExhausted, "Task replacement exceeds byte limit")
			}
			a.bytes += len(raw)
		}
		a.events = append(a.events, delivery.Events...)
		a.nextPage++
		return nil, false, nil
	case DeliveryReplaceEnd:
		if delivery.Source != SourceReplacement || !a.Pending() || delivery.SnapshotID != a.snapshotID || delivery.Page != a.nextPage || len(delivery.Events) != 0 || delivery.NextCursor != "" {
			return nil, false, invalidTaskDelivery("replacement end")
		}
		events = append([]eventstream.Envelope(nil), a.events...)
		a.Reset()
		return events, true, nil
	default:
		return nil, false, invalidTaskDelivery("kind")
	}
}

func validExactTaskAppend(delivery Delivery) bool {
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

func nonResumableTaskEnvelopes(envelopes []eventstream.Envelope) bool {
	for _, envelope := range envelopes {
		if strings.TrimSpace(envelope.Cursor) != "" || envelope.Position != nil {
			return false
		}
	}
	return true
}

func (a *DeliveryAssembler) Pending() bool {
	return a != nil && a.snapshotID != ""
}

func (a *DeliveryAssembler) Reset() {
	if a == nil {
		return
	}
	a.snapshotID = ""
	a.nextPage = 0
	a.bytes = 0
	a.events = nil
}

func invalidTaskDelivery(part string) error {
	return errorcode.New(errorcode.InvalidArgument, "invalid Task delivery: "+part)
}
