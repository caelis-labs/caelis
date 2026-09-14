package taskstream

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/history"
	"github.com/caelis-labs/caelis/control/streamspool"
)

func (s *service) childHistoryWindow(ctx context.Context, entry *task.Entry, source exactSource, req SubscribeRequest) (exactSource, string, error) {
	name := fmt.Sprint(source.key)
	high := uint64(source.bounds.High)
	turns := req.HistoryTurns
	if req.HistoryBefore != "" {
		p, err := history.Decode(s.cursors.secret, req.HistoryBefore, entry.Session.SessionID, entry.TaskID)
		if err != nil {
			return source, "", err
		}
		if p.Source != name || p.Before > high || source.bounds.Low != 0 || !source.bounds.OriginComplete {
			return source, "", history.Stale()
		}
		high = p.Before
		if turns == 0 {
			turns = history.DefaultTurns
		}
	}
	if turns == 0 {
		return source, "", nil
	}
	e := s.histories.Get(name)
	e.Lock()
	defer e.Unlock()
	if e.Index.High < high {
		reader, err := s.spool.Reader(ctx, source.key, streamspool.Offset(e.Index.High))
		if err != nil {
			return source, "", err
		}
		defer reader.Close()
		for e.Index.High < high {
			raw, err := reader.Next(ctx)
			if err != nil {
				return source, "", err
			}
			var record recordedTaskOutput
			if err := json.Unmarshal(raw.Payload, &record); err != nil {
				return source, "", err
			}
			id := record.ActivityID
			if event := record.Event.Event; event != nil && event.Scope != nil && event.Scope.TurnID != "" {
				id = event.Scope.TurnID
			}
			e.Index.Observe(uint64(raw.Offset), id)
			e.Index.High = uint64(raw.Offset) + 1
		}
	}
	start := e.Index.Start(high, turns)
	source.offset, source.seq = streamspool.Offset(start), start
	source.bounds.High = streamspool.Offset(high)
	before := history.Encode(s.cursors.secret, history.Position{SessionID: entry.Session.SessionID, TaskID: entry.TaskID, Source: name, Before: start})
	return source, before, nil
}
