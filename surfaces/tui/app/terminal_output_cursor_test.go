package tuiapp

import "testing"

func TestMergeTerminalOutputByCursorIsOrderIndependent(t *testing.T) {
	t.Parallel()

	const (
		first  = "first\n"
		second = "second\n"
	)
	firstEnd := int64(len([]byte(first)))
	secondEnd := int64(len([]byte(first + second)))
	exactSecond := ToolUpdateMeta{
		OutputTerminal: true, OutputCursor: secondEnd, OutputCursorKnown: true,
	}
	observedSecond := ToolUpdateMeta{
		OutputTerminal:    true,
		OutputStartCursor: firstEnd, OutputStartCursorKnown: true,
		OutputCursor: secondEnd, OutputCursorKnown: true,
	}

	t.Run("stream then observation", func(t *testing.T) {
		event := SubagentEvent{Output: first, OutputCursor: firstEnd, OutputCursorKnown: true}
		if !mergeTerminalOutputByCursor(&event, second, exactSecond) {
			t.Fatal("stream delta was not merged")
		}
		if mergeTerminalOutputByCursor(&event, second, observedSecond) {
			t.Fatal("durable observation duplicated already streamed bytes")
		}
		if event.Output != first+second {
			t.Fatalf("output = %q, want %q", event.Output, first+second)
		}
	})

	t.Run("observation then stream", func(t *testing.T) {
		event := SubagentEvent{Output: first, OutputCursor: firstEnd, OutputCursorKnown: true}
		if !mergeTerminalOutputByCursor(&event, second, observedSecond) {
			t.Fatal("durable observation was not merged at its exact boundary")
		}
		if mergeTerminalOutputByCursor(&event, second, exactSecond) {
			t.Fatal("late stream delta duplicated observed bytes")
		}
		if event.Output != first+second {
			t.Fatalf("output = %q, want %q", event.Output, first+second)
		}
	})
}

func TestContentlessFinalDoesNotAdvanceRepresentedOutputCursor(t *testing.T) {
	t.Parallel()

	event := SubagentEvent{
		Kind: SEToolCall, Name: "RUN_COMMAND", ToolKind: "execute", Terminal: true,
		Output: "abc", OutputTerminal: true, OutputCursor: 3, OutputCursorKnown: true,
	}
	final := SubagentEvent{
		Kind: SEToolCall, Name: "RUN_COMMAND", ToolKind: "execute", Terminal: true,
		OutputCursor: 6, OutputCursorKnown: true, Done: true,
	}
	mergeFinalToolEvent(&event, &final, false)
	if event.OutputCursor != 3 || !event.OutputCursorKnown {
		t.Fatalf("contentless final cursor = (%d,%v), want represented cursor 3", event.OutputCursor, event.OutputCursorKnown)
	}

	if !mergeTerminalOutputByCursor(&event, "def", ToolUpdateMeta{
		OutputTerminal:    true,
		OutputStartCursor: 3, OutputStartCursorKnown: true,
		OutputCursor: 6, OutputCursorKnown: true,
	}) {
		t.Fatal("durable observation did not repair suffix after contentless final")
	}
	if event.Output != "abcdef" || event.OutputCursor != 6 {
		t.Fatalf("repaired event = %#v, want output abcdef at cursor 6", event)
	}
}

func TestLegacyCompactTaskObservationCannotClaimExactCursor(t *testing.T) {
	t.Parallel()

	const exact = "line 1\r\nline 2\r\nline 3\r\nline 4\r\nline 5\r\nline 6\r\n"
	exactMeta := ToolUpdateMeta{
		TaskHandle: "command-3", OutputTerminal: true,
		OutputStartCursor: 0, OutputStartCursorKnown: true,
		OutputCursor: int64(len([]byte(exact))), OutputCursorKnown: true,
	}
	compactMutation := func() transcriptToolMutation {
		return transcriptToolMutation{
			output: "...1 line hidden...\nline 2\nline 3\nline 4\nline 5\nline 6\n",
			meta:   ToolUpdateMeta{TaskHandle: "command-3"},
		}
	}
	exactMutation := func() transcriptToolMutation {
		return transcriptToolMutation{output: exact, meta: exactMeta}
	}
	newOwner := func() []SubagentEvent {
		return []SubagentEvent{{
			Kind: SEToolCall, Name: "RUN_COMMAND", ToolKind: "execute",
			Terminal: true, TaskHandle: "command-3",
		}}
	}

	t.Run("compact then exact", func(t *testing.T) {
		events := newOwner()
		compact := compactMutation()
		if !absorbCommandTaskObservationIntoEvents(events, &compact) || !events[0].OutputSynthetic || events[0].OutputCursorKnown {
			t.Fatalf("compact owner = %#v, want replaceable snapshot without cursor", events[0])
		}
		exactObservation := exactMutation()
		if !absorbCommandTaskObservationIntoEvents(events, &exactObservation) {
			t.Fatal("exact observation did not find owner")
		}
		if events[0].Output != exact || events[0].OutputSynthetic || events[0].OutputCursor != exactMeta.OutputCursor {
			t.Fatalf("exact owner = %#v, want exact bytes and cursor", events[0])
		}
	})

	t.Run("exact then compact", func(t *testing.T) {
		events := newOwner()
		exactObservation := exactMutation()
		if !absorbCommandTaskObservationIntoEvents(events, &exactObservation) {
			t.Fatal("exact observation did not find owner")
		}
		compact := compactMutation()
		if !absorbCommandTaskObservationIntoEvents(events, &compact) {
			t.Fatal("compact observation did not find owner")
		}
		if events[0].Output != exact || events[0].OutputSynthetic || events[0].OutputCursor != exactMeta.OutputCursor {
			t.Fatalf("owner after late compact = %#v, want unchanged exact bytes and cursor", events[0])
		}
	})
}

func TestMergeTerminalOutputByCursorSlicesExactOverlapOnly(t *testing.T) {
	t.Parallel()

	event := SubagentEvent{Output: "abc", OutputCursor: 3, OutputCursorKnown: true}
	meta := ToolUpdateMeta{
		OutputTerminal:    true,
		OutputCursor:      6,
		OutputCursorKnown: true,
	}
	if !mergeTerminalOutputByCursor(&event, "bcdef", meta) {
		t.Fatal("overlapping exact stream frame was not merged")
	}
	if event.Output != "abcdef" {
		t.Fatalf("output = %q, want abcdef", event.Output)
	}
}
