package taskstream

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/caelis-labs/caelis/control/history"
	"github.com/caelis-labs/caelis/control/streamspool"
)

// retainChildWindow runs only on the Task writer. Live delivery continues via
// immutable records; compaction publishes one atomic replacement. No durable
// Session record or model context is rewritten.
func (r *Recorder) retainChildWindow(p *recordingPartition, logical streamspool.LogicalKey, records []streamspool.Record) error {
	compact := false
	for _, raw := range records {
		p.writtenBytes += len(raw.Payload)
		var record recordedTaskOutput
		if err := json.Unmarshal(raw.Payload, &record); err != nil {
			return err
		}
		id := taskOutputTurn(record)
		if record.Event.Event != nil && id != "" && id != p.lastTurn {
			p.lastTurn = id
			p.turnCount++
			compact = p.turnCount > 2
		}
	}
	if !compact && p.writtenBytes < 2*history.TranscriptBytes {
		return nil
	}
	r.compactMu.Lock()
	defer r.compactMu.Unlock()
	ctx := context.Background()
	bounds, err := p.writer.Bounds(ctx)
	if err != nil {
		return err
	}
	if !bounds.OriginComplete {
		p.writtenBytes = 0
		return nil
	}
	reader, err := r.store.Reader(ctx, p.writer.Key(), bounds.Low)
	if err != nil {
		if errors.Is(err, streamspool.ErrExpired) {
			p.writtenBytes = 0
			return nil
		}
		return err
	}
	var window retainedChildOutput
	for pos := bounds.Low; pos < bounds.High; pos++ {
		raw, err := reader.Next(ctx)
		if err != nil {
			_ = reader.Close()
			if errors.Is(err, streamspool.ErrExpired) {
				// Another Session reclaimed this optional compaction source.
				// Its live writer still owns a valid retained suffix.
				p.writtenBytes = 0
				return nil
			}
			return err
		}
		var record recordedTaskOutput
		if err := json.Unmarshal(raw.Payload, &record); err != nil {
			_ = reader.Close()
			return err
		}
		window.append(record)
	}
	_ = reader.Close()
	projected, err := window.records()
	if err != nil {
		return err
	}
	return r.replacePartition(p, logical, projected, nil)
}

func taskOutputTurn(record recordedTaskOutput) string {
	if event := record.Event.Event; event != nil && event.Scope != nil && event.Scope.TurnID != "" {
		return event.Scope.TurnID
	}
	if record.ActivityID != "" {
		return record.ActivityID
	}
	return record.TerminalID
}
