package taskstream

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/streamspool"
)

const subagentReplayTimeout = 30 * time.Second

// childSource recovers a missing origin at the producer boundary. Both finite
// reads and subscriptions subsequently read only the same spool incarnation.
func (s *service) childSource(ctx context.Context, entry *task.Entry) (exactSource, error) {
	if s.recorder == nil {
		return exactSource{}, errorcode.New(errorcode.Unavailable, "Task output recorder is unavailable")
	}
	ref := task.Ref{SessionID: entry.Session.SessionID, TaskID: entry.TaskID}
	flushErr := s.recorder.Flush(ctx, ref)
	if flushErr != nil && ctx.Err() != nil {
		return exactSource{}, ctx.Err()
	}
	if flushErr == nil {
		if source, exact, err := s.selectExact(ctx, entry, cursorPoint{}, false); err != nil || exact {
			return source, err
		}
	}
	key := streamspool.DigestStrings(ref.SessionID, ref.TaskID).Hex()
	// A viewer leaving does not cancel another viewer's shared history import.
	// The import is bounded, opens no prompt, and retains no Surface state.
	result := s.historyLoads.DoChan(key, func() (any, error) {
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), subagentReplayTimeout)
		defer cancel()
		flushErr := s.recorder.Flush(loadCtx, ref)
		if flushErr != nil && loadCtx.Err() != nil {
			return nil, loadCtx.Err()
		}
		if flushErr == nil {
			if source, exact, err := s.selectExact(loadCtx, entry, cursorPoint{}, false); err != nil || exact {
				return source, err
			}
		}
		if _, err := s.loadProviderSubagentHistory(loadCtx, entry, taskHistoryChildSessionID(entry)); err != nil {
			return nil, err
		}
		if err := s.recorder.Flush(loadCtx, ref); err != nil {
			return nil, err
		}
		source, exact, err := s.selectExact(loadCtx, entry, cursorPoint{}, false)
		if err != nil {
			return nil, err
		}
		if !exact {
			return nil, errorcode.New(errorcode.Unavailable, "Child replay did not publish a complete output cache")
		}
		return source, nil
	})
	select {
	case <-ctx.Done():
		return exactSource{}, ctx.Err()
	case result := <-result:
		if result.Err != nil {
			return exactSource{}, result.Err
		}
		return result.Val.(exactSource), nil
	}
}

// childRead pages an exact origin or cursor. Only a stale cursor needs an
// atomic replacement of a document the consumer has already received.
func (s *service) childRead(ctx context.Context, entry *task.Entry, point cursorPoint, present bool) (ReadResult, exactSource, error) {
	current, err := s.childSource(ctx, entry)
	if err != nil {
		return ReadResult{}, exactSource{}, err
	}
	if !present || point.Key == current.key && point.Offset >= current.bounds.Low && point.Offset <= current.bounds.High {
		if present {
			current.offset, current.seq = point.Offset, point.Sequence
		}
		return s.childAppendPage(ctx, entry, current)
	}
	var records []Record
	next := cursorPoint{Key: current.key}
	bytes := 0
	for next.Offset < current.bounds.High {
		current.offset, current.seq = next.Offset, next.Sequence
		page, after, err := s.readAvailable(ctx, entry, current)
		if err != nil {
			return ReadResult{}, exactSource{}, err
		}
		if after.Offset == next.Offset {
			return ReadResult{}, exactSource{}, fmt.Errorf("taskstream: history read made no progress")
		}
		for _, record := range page {
			raw, err := json.Marshal(record)
			if err != nil {
				return ReadResult{}, exactSource{}, err
			}
			bytes += len(raw)
		}
		records = append(records, page...)
		if len(records) > maxReplacementRecords || bytes > maxReplacementBytes {
			return ReadResult{}, exactSource{}, errorcode.New(errorcode.ResourceExhausted, "Child history exceeds replacement limit")
		}
		next = after
	}
	sealChildHistoryTail(entry, records)
	deliveries, err := replacementDeliveries(entry, records)
	if err != nil {
		return ReadResult{}, exactSource{}, err
	}
	cursor, err := s.cursors.encode(entry.Session.SessionID, entry.TaskID, next)
	if err != nil {
		return ReadResult{}, exactSource{}, err
	}
	deliveries[len(deliveries)-1].NextCursor = cursor
	current.offset, current.seq = next.Offset, next.Sequence
	return ReadResult{Deliveries: deliveries, ActivityID: descriptorFromEntry(entry).ActivityID}, current, nil
}

func (s *service) childAppendPage(ctx context.Context, entry *task.Entry, source exactSource) (ReadResult, exactSource, error) {
	records, next, err := s.readAvailable(ctx, entry, source)
	if err != nil {
		return ReadResult{}, exactSource{}, err
	}
	if next.Offset == source.bounds.High {
		sealChildHistoryTail(entry, records)
	}
	cursor, err := s.cursors.encode(entry.Session.SessionID, entry.TaskID, next)
	activityID := descriptorFromEntry(entry).ActivityID
	source.offset, source.seq = next.Offset, next.Sequence
	return ReadResult{ActivityID: activityID, Deliveries: []Delivery{{
		Kind: DeliveryAppendPage, Source: SourceExact, Records: records, NextCursor: cursor, ActivityID: activityID,
	}}}, source, err
}

// Idle history uses the directory's lifecycle facts for its final frame only.
// The annotation never changes producer output or its resume position.
func sealChildHistoryTail(entry *task.Entry, records []Record) {
	if !entry.Running && task.IsTerminalState(entry.State) && len(records) > 0 {
		last := &records[len(records)-1]
		if last.Frame != nil && last.Frame.ActivityID == descriptorFromEntry(entry).ActivityID {
			// Keep the last replayed Turn identity. A successor's first output
			// can precede its directory commit and must never be sealed here.
			last.Frame.Closed = true
			last.Frame.Running = false
			last.Frame.State = string(entry.State)
		}
	}
}

func (s *service) forwardChild(sub *subscription, entry *task.Entry, point cursorPoint, present, follow bool) {
	for {
		result, source, err := s.childRead(sub.ctx, entry, point, present)
		if err != nil {
			sub.finish(err)
			return
		}
		for {
			for _, delivery := range result.Deliveries {
				if !sub.deliver(delivery) {
					sub.finish(sub.ctx.Err())
					return
				}
			}
			if source.offset >= source.bounds.High {
				break
			}
			result, source, err = s.childAppendPage(sub.ctx, entry, source)
			if err != nil {
				sub.finish(err)
				return
			}
		}
		if !follow && !entry.Running {
			sub.finish(nil)
			return
		}
		if !s.forwardExact(sub, entry, source, follow) {
			return
		}
		// A new incarnation must replace the prefix already delivered, even
		// when this subscription originally opened without a cursor.
		point, present = cursorPoint{Key: source.key}, true
	}
}

func (s *service) retainChildObservation(ref session.SessionRef) (func(), error) {
	if s.retainObservation != nil {
		return s.retainObservation(ref)
	}
	return func() {}, nil
}
