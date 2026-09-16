package taskstream

import (
	"cmp"
	"encoding/json"
	"slices"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/history"
	"github.com/caelis-labs/caelis/control/streamspool"
)

type childOutputOwner struct{ terminal, activity string }

type retainedChildFrame struct {
	position int
	record   recordedTaskOutput
}

// The transcript owns display retention. Companion frames preserve transport
// metadata and the latest lifecycle fact for each activity still represented in
// that window. Neither cache supplies authoritative Task state.
type retainedChildOutput struct {
	transcript history.Transcript
	frames     map[*session.Event]retainedChildFrame
	lifecycle  map[childOutputOwner]retainedChildFrame
	tail       retainedChildFrame
	tailNested bool
}

func childFrameOwner(record recordedTaskOutput) childOutputOwner {
	return childOutputOwner{record.TerminalID, record.ActivityID}
}

func (w *retainedChildOutput) append(record recordedTaskOutput) {
	if w.frames == nil {
		w.frames = make(map[*session.Event]retainedChildFrame)
		w.lifecycle = make(map[childOutputOwner]retainedChildFrame)
	}
	e := record.Event.Event
	if e != nil {
		if e.Scope == nil {
			e.Scope = &session.EventScope{}
		}
		if e.Scope.TurnID == "" {
			e.Scope.TurnID = taskOutputTurn(record)
		}
	}
	// Only the bounded transcript retains nested event bodies.
	record.Event.Event = nil
	frame := retainedChildFrame{position: w.tail.position + 1, record: record}
	if projected := w.transcript.Append(e); projected != nil {
		w.frames[projected] = frame
	}
	if event := record.Event; event.State != "" || event.Running || event.Closed || event.ExitCode != nil {
		w.lifecycle[childFrameOwner(record)] = frame
	}
	w.tail = frame
	w.tailNested = e != nil
	if len(w.frames) > len(w.transcript.Events()) || len(w.lifecycle) > history.MaxTurns+1 {
		w.prune()
	}
}

func (w *retainedChildOutput) prune() {
	kept := make(map[*session.Event]bool, len(w.transcript.Events()))
	owners := map[childOutputOwner]bool{childFrameOwner(w.tail.record): true}
	for _, event := range w.transcript.Events() {
		kept[event] = true
		owners[childFrameOwner(w.frames[event].record)] = true
	}
	for event := range w.frames {
		if !kept[event] {
			delete(w.frames, event)
		}
	}
	for owner := range w.lifecycle {
		if !owners[owner] {
			delete(w.lifecycle, owner)
		}
	}
}

func (w *retainedChildOutput) records() ([]streamspool.Record, error) {
	w.prune()
	frames := make(map[int]recordedTaskOutput, len(w.frames)+len(w.lifecycle))
	for _, event := range w.transcript.Events() {
		frame := w.frames[event]
		frame.record.Event.Event = event
		frames[frame.position] = frame.record
	}
	for _, frame := range w.lifecycle {
		if _, exists := frames[frame.position]; !exists {
			frames[frame.position] = frame.record
		}
	}
	if w.tail.position > 0 && !w.tailNested {
		frames[w.tail.position] = w.tail.record
	}
	ordered := make([]retainedChildFrame, 0, len(frames))
	for position, record := range frames {
		ordered = append(ordered, retainedChildFrame{position, record})
	}
	slices.SortFunc(ordered, func(a, b retainedChildFrame) int { return cmp.Compare(a.position, b.position) })
	projected := make([]streamspool.Record, 0, len(ordered))
	for _, frame := range ordered {
		raw, err := json.Marshal(frame.record)
		if err != nil {
			return nil, err
		}
		projected = append(projected, streamspool.Record{Type: taskOutputRecordType, OccurredAt: frame.record.Event.OccurredAt, Payload: raw})
	}
	return projected, nil
}
