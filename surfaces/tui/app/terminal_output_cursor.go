package tuiapp

import "unicode/utf8"

// mergeTerminalOutputByCursor reconciles exact stream deltas and durable Task
// observation snapshots against one RunCommand panel. Stream deltas may be
// sliced when delivery overlaps. Compact observations are only appended at an
// exact boundary because their rendered bytes are not a lossless terminal
// delta.
func mergeTerminalOutputByCursor(event *SubagentEvent, output string, meta ToolUpdateMeta) bool {
	if event == nil || output == "" {
		return false
	}
	if event.OutputSynthetic {
		event.Output = ""
		event.OutputSynthetic = false
		event.OutputCursor = 0
		event.OutputCursorKnown = false
	}
	if !meta.OutputCursorKnown {
		event.Output += output
		// Legacy terminal_output has no advertised absolute range, but it is an
		// exact next byte delta for this owner. If the prior absolute cursor is
		// known, advance it by the bytes just represented so a later replay of
		// the older range cannot overwrite these appended bytes.
		if event.OutputCursorKnown {
			event.OutputCursor += int64(len([]byte(output)))
		}
		return true
	}

	end := meta.OutputCursor
	start, startKnown := meta.OutputStartCursor, meta.OutputStartCursorKnown
	if !startKnown && meta.OutputTerminal {
		start = end - int64(len([]byte(output)))
		startKnown = start >= 0
	}
	if end < 0 || (startKnown && (start < 0 || start > end)) {
		return false
	}
	// A Task observation or a resumed stream may arrive after the panel has
	// already advanced past an unavailable prefix. When one later exact range
	// starts at zero and covers every represented byte, it is authoritative
	// recovery rather than a duplicate: replace the current view and clear any
	// locally remembered gap. Do not require that local gap bit here; recovery
	// must also repair inconsistent state produced by an older projection path.
	// Byte-range validation keeps this strictly cursor based; terminal text is
	// never reconciled by overlap. A cursorless legacy delta can make the local
	// byte count exceed the last exact cursor, so such a mixed view is not safe
	// to replace from a repeated range ending at that older cursor.
	representedBytes := int64(len([]byte(event.Output)))
	if event.OutputCursorKnown && representedBytes <= event.OutputCursor && startKnown && start == 0 &&
		end >= event.OutputCursor && int64(len([]byte(output))) == end {
		event.Output = output
		event.OutputGapBefore = meta.OutputGapBefore
		event.OutputCursor = end
		event.OutputCursorKnown = true
		return true
	}
	if event.OutputCursorKnown {
		switch current := event.OutputCursor; {
		case current >= end:
			return false
		case !startKnown:
			return false
		case current < start:
			event.OutputGapBefore = true
		case current > start:
			if !meta.OutputTerminal {
				return false
			}
			output = terminalOutputSuffix(output, current-start)
			if output == "" {
				event.OutputCursor = end
				return false
			}
		}
	} else if startKnown && start > 0 {
		event.OutputGapBefore = true
	}

	event.Output += output
	event.OutputCursor = end
	event.OutputCursorKnown = true
	return true
}

func terminalOutputSuffix(output string, byteOffset int64) string {
	if byteOffset <= 0 {
		return output
	}
	raw := []byte(output)
	if byteOffset >= int64(len(raw)) {
		return ""
	}
	offset := int(byteOffset)
	for offset < len(raw) && !utf8.RuneStart(raw[offset]) {
		offset++
	}
	if offset >= len(raw) {
		return ""
	}
	return string(raw[offset:])
}
