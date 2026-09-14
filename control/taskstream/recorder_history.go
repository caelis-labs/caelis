package taskstream

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	"github.com/caelis-labs/caelis/control/streamspool"
)

// ReplaceTaskHistoryStream queues one producer replay boundary and joins its
// consumer before returning. Later live callbacks keep their bounded queue and
// never wait for this reader's I/O. Only the complete incarnation is published.
func (o *boundRecorder) ReplaceTaskHistoryStream(ctx context.Context, source output.HistorySource) error {
	if source == nil {
		return fmt.Errorf("taskstream: history source is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if o == nil || o.partition == nil {
		return nil
	}
	barrier := make(chan error, 1)
	item := outputWrite{replace: true, barrier: barrier}
	item.stream = func(writer streamspool.Writer) error {
		var records []streamspool.Record
		bytes := 0
		flush := func() error {
			if len(records) == 0 {
				return nil
			}
			_, err := writer.AppendBatch(ctx, records)
			records, bytes = nil, 0
			return err
		}
		err := source(ctx, func(event *session.Event) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if event == nil {
				return nil
			}
			record, err := historyOutputRecord(o.binding.ActivityID, event)
			if err != nil {
				return err
			}
			if len(record.Payload) > taskOutputQueueBytes {
				return streamspool.ErrLimit
			}
			if len(records) >= maxDeliveryRecords || bytes+len(record.Payload) > taskOutputBatchBytes {
				if err := flush(); err != nil {
					return err
				}
			}
			records = append(records, record)
			bytes += len(record.Payload)
			return nil
		})
		if err != nil {
			return err
		}
		return flush()
	}
	if err := o.recorder.enqueue(o.partition, item); err != nil {
		return err
	}
	// Even a cancelled caller must not release its source while the writer is
	// still reading it. The source and append operations receive that context.
	return <-barrier
}

func historyOutputRecord(activityID string, event *session.Event) (streamspool.Record, error) {
	terminalID := ""
	if event.Scope != nil {
		terminalID = event.Scope.TurnID
	}
	payload, err := json.Marshal(recordedTaskOutput{TerminalID: terminalID, ActivityID: activityID, Event: output.Event{Event: event, OccurredAt: event.Time}})
	return streamspool.Record{Type: taskOutputRecordType, OccurredAt: event.Time, Payload: payload}, err
}
