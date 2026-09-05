package tuiapp

import (
	"context"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
)

// taskStreamMailbox batches exact append pages for one Surface reader. It never
// merges across replacement or activity boundaries, and leaves each delivery's
// cursor attached to all of its events until the UI applies the resulting batch.
type taskStreamMailbox struct {
	assembler taskstream.DeliveryAssembler
	pending   *taskstream.Delivery
}

func (m *taskStreamMailbox) read(ctx context.Context, deliveries <-chan taskstream.Delivery) ([]eventstream.Envelope, string, string, bool, bool, error) {
	for {
		var delivery taskstream.Delivery
		if m.pending != nil {
			delivery = *m.pending
			m.pending = nil
		} else {
			select {
			case <-ctx.Done():
				return nil, "", "", false, false, ctx.Err()
			case next, open := <-deliveries:
				if !open {
					return nil, "", "", false, false, m.closedError()
				}
				delivery = next
			}
		}
		events, replacement, err := m.assembler.Accept(delivery)
		if err != nil {
			return nil, "", "", false, false, err
		}
		if len(events) == 0 && !replacement && delivery.NextCursor == "" &&
			(delivery.ActivityID == "" || delivery.Kind != taskstream.DeliveryStatus) {
			continue
		}
		if delivery.Kind != taskstream.DeliveryAppendPage || replacement {
			return events, delivery.NextCursor, delivery.ActivityID, replacement, true, nil
		}
		cursor := delivery.NextCursor
		timer := time.NewTimer(taskStreamMailboxBudget)
		defer timer.Stop()
		for len(events) < taskStreamMailboxBatchSize {
			select {
			case <-ctx.Done():
				return events, cursor, delivery.ActivityID, false, false, ctx.Err()
			case <-timer.C:
				return events, cursor, delivery.ActivityID, false, true, nil
			case next, open := <-deliveries:
				if !open {
					return events, cursor, delivery.ActivityID, false, false, m.closedError()
				}
				if next.Kind != delivery.Kind || next.Source != delivery.Source || next.ActivityID != delivery.ActivityID {
					m.pending = &next
					return events, cursor, delivery.ActivityID, false, true, nil
				}
				more, _, err := m.assembler.Accept(next)
				if err != nil {
					return events, cursor, delivery.ActivityID, false, false, err
				}
				events = append(events, more...)
				cursor = next.NextCursor
			}
		}
		return events, cursor, delivery.ActivityID, false, true, nil
	}
}

func (m *taskStreamMailbox) closedError() error {
	if m.assembler.Pending() {
		return errorcode.New(errorcode.Unavailable, "Task replacement ended before commit")
	}
	return nil
}
