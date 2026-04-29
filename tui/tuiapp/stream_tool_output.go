package tuiapp

import "strings"

// resolveCallAnchor returns the block ID of the tool-call TranscriptBlock
// ("▸ TOOLNAME ...") that corresponds to the given callID and toolName.
// It first checks the stable callAnchorIndex; if not found, it claims the
// oldest pending anchor matching toolName (FIFO).
func (m *Model) resolveCallAnchor(callID, toolName string) string {
	if m.callAnchorIndex == nil {
		m.callAnchorIndex = map[string]string{}
	}
	if callID != "" {
		if bid, ok := m.callAnchorIndex[callID]; ok {
			return bid
		}
	}
	normalized := strings.ToUpper(strings.TrimSpace(toolName))
	for i, a := range m.pendingToolAnchors {
		if strings.EqualFold(a.toolName, normalized) {
			m.pendingToolAnchors = append(m.pendingToolAnchors[:i], m.pendingToolAnchors[i+1:]...)
			if callID != "" {
				m.callAnchorIndex[callID] = a.blockID
			}
			return a.blockID
		}
	}
	if len(m.pendingToolAnchors) > 0 {
		a := m.pendingToolAnchors[0]
		m.pendingToolAnchors = m.pendingToolAnchors[1:]
		if callID != "" {
			m.callAnchorIndex[callID] = a.blockID
		}
		return a.blockID
	}
	return ""
}

// extractToolCallName extracts the tool name from a "▸ TOOLNAME ..." log line.
// Returns the name and true if the line is a tool call start; empty and false otherwise.
func extractToolCallName(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(trimmed, "▸"):
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "▸"))
	case strings.HasPrefix(trimmed, "▾"):
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "▾"))
	case strings.HasPrefix(trimmed, "▶"):
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "▶"))
	default:
		return "", false
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return "", false
	}
	return strings.ToUpper(fields[0]), true
}

// panelProducingTools lists the tool names that can claim a transcript anchor.
var panelProducingTools = map[string]bool{
	"BASH":  true,
	"SPAWN": true,
}

func inlineBashAnchorLabel(raw string, expanded bool) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return raw
	}
	prefix := ""
	rest := trimmed
	for _, candidate := range []string{"▸", "▾", "▶"} {
		if strings.HasPrefix(trimmed, candidate) {
			prefix = candidate
			rest = strings.TrimSpace(strings.TrimPrefix(trimmed, candidate))
			break
		}
	}
	if prefix == "" {
		return raw
	}
	next := "▸"
	if expanded {
		next = "▾"
	}
	leadingEnd := strings.Index(raw, trimmed)
	if leadingEnd < 0 {
		return next + " " + rest
	}
	leading := raw[:leadingEnd]
	return leading + next + " " + rest
}

func formatSubagentPreviewText(text string, stream string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\t", " "))
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "__", "")
	text = strings.ReplaceAll(text, "`", "")
	text = strings.TrimLeft(text, "#*- ")
	text = collapseSubagentInlineSpaces(text)
	if text == "" {
		return ""
	}
	if stream == "assistant" {
		if text == "answer" || text == "assistant" {
			return ""
		}
		text = strings.TrimPrefix(text, "answer ")
		text = strings.TrimPrefix(text, "assistant ")
	}
	if stream == "reasoning" {
		if text == "reasoning" {
			return ""
		}
		text = strings.TrimPrefix(text, "reasoning ")
	}
	return strings.TrimSpace(text)
}

func collapseSubagentInlineSpaces(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(text))
	spaceRun := false
	for _, r := range text {
		if r == ' ' || r == '\n' || r == '\r' || r == '\f' || r == '\v' {
			if !spaceRun {
				b.WriteByte(' ')
				spaceRun = true
			}
			continue
		}
		spaceRun = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func wrapToolOutputText(text string, width int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	width = maxInt(1, width)
	parts := strings.Split(text, "\n")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		for displayColumns(part) > width {
			cut := width
			slice := sliceByDisplayColumns(part, 0, cut)
			lastSpace := strings.LastIndex(slice, " ")
			if lastSpace > 8 {
				cut = displayColumns(slice[:lastSpace])
				slice = sliceByDisplayColumns(part, 0, cut)
			}
			out = append(out, strings.TrimSpace(slice))
			part = strings.TrimSpace(sliceByDisplayColumns(part, cut, displayColumns(part)))
		}
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return []string{text}
	}
	return out
}

func isSpawnLikeTool(name string) bool {
	name = strings.TrimSpace(name)
	return strings.EqualFold(name, "SPAWN")
}
