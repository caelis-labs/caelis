package subagent

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

const (
	maxSubagentPreviewBytes         = 1024
	maxSubagentPreviewBlocks        = 4
	maxSubagentActivityBlockBytes   = 240
	maxSubagentNarrativeBufferBytes = 4096
	subagentPreviewTruncationMarker = " ...[truncated]... "
)

// subagentActionSummary is the bounded, transient activity tail used by Task
// read/wait. It retains only a few normalized display slices, never the child
// transcript or raw tool payloads.
type subagentActionSummary struct {
	blocks           []subagentActivityBlock
	thoughtBuffer    string
	thoughtMessageID string
	thoughtOpen      bool
	sequence         uint64
	observed         bool
}

type subagentActivityBlock struct {
	key    string
	title  string
	detail string
}

func (s *subagentActionSummary) reset() {
	if s == nil {
		return
	}
	*s = subagentActionSummary{}
}

func (s *subagentActionSummary) observeAssistant(messageID string, text string) {
	if s == nil {
		return
	}
	s.thoughtOpen = false
	text = compactActivityText(text, maxSubagentActivityBlockBytes)
	if text == "" {
		return
	}
	s.upsertBlock(activityKey("assistant", messageID), "Assistant: "+text, "")
}

func (s *subagentActionSummary) observeThought(messageID string, delta string) {
	if s == nil {
		return
	}
	messageID = strings.TrimSpace(messageID)
	if !s.thoughtOpen || (messageID != "" && s.thoughtMessageID != "" && messageID != s.thoughtMessageID) {
		s.thoughtBuffer = ""
	}
	s.thoughtOpen = true
	if messageID != "" {
		s.thoughtMessageID = messageID
	}
	s.thoughtBuffer = appendBoundedActivity(s.thoughtBuffer, delta)
	text := compactActivityText(s.thoughtBuffer, maxSubagentActivityBlockBytes)
	if text == "" {
		return
	}
	s.upsertBlock(activityKey("reasoning", messageID), "Reasoning: "+text, "")
}

func (s *subagentActionSummary) observeTool(toolCallID string, action string, output string) {
	if s == nil {
		return
	}
	s.thoughtOpen = false
	action = compactActivityLabel(action)
	if strings.EqualFold(action, "working") {
		action = ""
	}
	output = compactActivityText(compactToolOutput(output), maxSubagentActivityBlockBytes)
	if action == "" && output == "" {
		return
	}
	s.upsertBlock(activityKey("tool", toolCallID), action, output)
}

func (s *subagentActionSummary) observeAction(action string) {
	if s == nil {
		return
	}
	s.thoughtOpen = false
	action = compactActivityLabel(action)
	if action == "" || strings.EqualFold(action, "working") {
		return
	}
	s.sequence++
	s.upsertBlock(fmt.Sprintf("action:%d", s.sequence), action, "")
}

func (s *subagentActionSummary) upsertBlock(key string, title string, detail string) {
	if s == nil {
		return
	}
	title = strings.TrimSpace(title)
	detail = strings.TrimSpace(detail)
	if title == "" && detail == "" {
		return
	}
	if key == "" {
		s.sequence++
		key = fmt.Sprintf("activity:%d", s.sequence)
	}
	block := subagentActivityBlock{key: key, title: title, detail: detail}
	for index := range s.blocks {
		if s.blocks[index].key != key {
			continue
		}
		if block.title == "" {
			block.title = s.blocks[index].title
		}
		if block.detail == "" {
			block.detail = s.blocks[index].detail
		}
		s.blocks = append(s.blocks[:index], s.blocks[index+1:]...)
		break
	}
	s.blocks = append(s.blocks, block)
	if len(s.blocks) > maxSubagentPreviewBlocks {
		s.blocks = s.blocks[len(s.blocks)-maxSubagentPreviewBlocks:]
	}
	s.observed = true
}

func (s subagentActionSummary) previewOrEmpty() string {
	blocks := make([]string, 0, len(s.blocks))
	for _, block := range s.blocks {
		if text := block.preview(); text != "" {
			blocks = append(blocks, text)
		}
	}
	preview := strings.Join(blocks, "\n")
	if preview != "" {
		return truncateMiddleUTF8(preview, maxSubagentPreviewBytes)
	}
	if s.observed {
		return "working"
	}
	return ""
}

func (b subagentActivityBlock) preview() string {
	title := compactActivityLabel(b.title)
	detail := strings.TrimSpace(b.detail)
	if title == "" {
		title = "Working"
	}
	if detail == "" {
		return compactActivityText(title, maxSubagentActivityBlockBytes)
	}
	lines := strings.Split(detail, "\n")
	var out strings.Builder
	out.WriteString(title)
	for index, line := range lines {
		if index == 0 {
			out.WriteString("\n  └ ")
		} else {
			out.WriteString("\n    ")
		}
		out.WriteString(line)
	}
	return truncateMiddleUTF8(out.String(), maxSubagentActivityBlockBytes)
}

func appendBoundedActivity(current string, delta string) string {
	if delta == "" {
		return current
	}
	if parts := strings.SplitN(current, subagentPreviewTruncationMarker, 2); len(parts) == 2 {
		tailBudget := max(0, maxSubagentNarrativeBufferBytes-len(parts[0])-len(subagentPreviewTruncationMarker))
		return parts[0] + subagentPreviewTruncationMarker + utf8Suffix(parts[1]+delta, tailBudget)
	}
	combined := current + delta
	if len(combined) <= maxSubagentNarrativeBufferBytes {
		return combined
	}
	return truncateMiddleUTF8(combined, maxSubagentNarrativeBufferBytes)
}

func activityKey(kind string, id string) string {
	kind = strings.TrimSpace(kind)
	id = strings.TrimSpace(id)
	if id == "" {
		return kind
	}
	return kind + ":" + id
}

func compactActivityLabel(text string) string {
	text = normalizeActivityText(text)
	text = strings.ReplaceAll(text, "\n", " · ")
	return truncateMiddleUTF8(text, maxSubagentActivityBlockBytes)
}

func compactActivityText(text string, maxBytes int) string {
	return truncateMiddleUTF8(normalizeActivityText(text), maxBytes)
}

func normalizeActivityText(text string) string {
	text = ansi.Strip(text)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func compactToolOutput(text string) string {
	text = normalizeActivityText(text)
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) <= 2 {
		return strings.Join(lines, "\n")
	}
	return strings.Join([]string{lines[0], fmt.Sprintf("... +%d lines", len(lines)-2), lines[len(lines)-1]}, "\n")
}

func toolContentActivity(content []client.ToolCallContent) string {
	output := ""
	for _, item := range content {
		if text := strings.TrimSpace(session.ExtractProtocolText(item.Content)); text != "" {
			if output != "" {
				text = "\n" + text
			}
			output = appendBoundedActivity(output, truncateMiddleUTF8(text, maxSubagentNarrativeBufferBytes))
		}
	}
	return output
}

func truncateMiddleUTF8(text string, maxBytes int) string {
	text = strings.TrimSpace(text)
	if text == "" || maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	if maxBytes <= len(subagentPreviewTruncationMarker)+2 {
		return utf8Prefix(text, maxBytes)
	}
	remaining := maxBytes - len(subagentPreviewTruncationMarker)
	headBudget := remaining * 3 / 5
	tailBudget := remaining - headBudget
	head := strings.TrimSpace(utf8Prefix(text, headBudget))
	tail := strings.TrimSpace(utf8Suffix(text, tailBudget))
	return head + subagentPreviewTruncationMarker + tail
}

func utf8Prefix(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}

func utf8Suffix(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	start := len(text) - maxBytes
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}

func planActivity(entries []client.PlanEntry) string {
	for _, entry := range entries {
		if strings.EqualFold(strings.TrimSpace(entry.Status), "in_progress") {
			if content := compactActivityLabel(entry.Content); content != "" {
				return "Plan: " + content
			}
		}
	}
	for _, entry := range entries {
		if strings.EqualFold(strings.TrimSpace(entry.Status), "pending") {
			if content := compactActivityLabel(entry.Content); content != "" {
				return "Next: " + content
			}
		}
	}
	for index := len(entries) - 1; index >= 0; index-- {
		if content := compactActivityLabel(entries[index].Content); content != "" {
			return "Plan: " + content
		}
	}
	return "Updating plan"
}
