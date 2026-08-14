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
