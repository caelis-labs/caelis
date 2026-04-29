package tuiapp

import (
	"slices"
	"strings"
	"unicode"
)

func cloneInputAttachments(items []inputAttachment) []inputAttachment {
	if len(items) == 0 {
		return nil
	}
	out := make([]inputAttachment, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		offset := max(item.Offset, 0)
		out = append(out, inputAttachment{Name: name, Offset: offset})
	}
	if len(out) == 0 {
		return nil
	}
	return sortInputAttachments(out)
}

func sortInputAttachments(items []inputAttachment) []inputAttachment {
	if len(items) <= 1 {
		return items
	}
	slices.SortStableFunc(items, func(left inputAttachment, right inputAttachment) int {
		switch {
		case left.Offset < right.Offset:
			return -1
		case left.Offset > right.Offset:
			return 1
		default:
			return 0
		}
	})
	return items
}

func attachmentNamesFromTokens(items []inputAttachment) []string {
	if len(items) == 0 {
		return nil
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil
	}
	return names
}

func (m *Model) syncAttachmentSummary() {
	names := attachmentNamesFromTokens(m.inputAttachments)
	m.attachmentNames = append([]string(nil), names...)
	m.attachmentCount = len(names)
}

func (m *Model) setInputAttachments(items []inputAttachment) {
	valueLen := len([]rune(m.textarea.Value()))
	cloned := cloneInputAttachments(items)
	for i := range cloned {
		if cloned[i].Offset > valueLen {
			cloned[i].Offset = valueLen
		}
	}
	m.inputAttachments = cloned
	m.syncAttachmentSummary()
}

func (m *Model) clearInputAttachments() {
	m.inputAttachments = nil
	m.syncAttachmentSummary()
}

func (m *Model) insertAttachmentsAtCursor(names []string) {
	if len(names) == 0 {
		return
	}
	offset := m.textareaCursorIndex()
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		m.inputAttachments = append(m.inputAttachments, inputAttachment{
			Name:   name,
			Offset: offset,
		})
	}
	m.syncAttachmentSummary()
}

func (m *Model) removeAttachmentAtCursor() bool {
	if len(m.inputAttachments) == 0 {
		return false
	}
	cursor := m.textareaCursorIndex()
	items := cloneInputAttachments(m.inputAttachments)
	removeIdx := -1
	for i := range items {
		if items[i].Offset == cursor {
			removeIdx = i
			continue
		}
		if items[i].Offset > cursor {
			break
		}
	}
	if removeIdx < 0 {
		return false
	}
	items = append(items[:removeIdx], items[removeIdx+1:]...)
	m.setInputAttachments(items)
	m.syncBackendAttachments()
	m.syncTextareaChrome()
	return true
}

func composeInputDisplay(value string, cursor int, attachments []inputAttachment) (string, int) {
	valueRunes := []rune(value)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(valueRunes) {
		cursor = len(valueRunes)
	}

	items := cloneInputAttachments(attachments)
	var out strings.Builder
	displayCursor := 0
	displayCount := 0
	textPos := 0
	cursorAssigned := false

	for _, item := range items {
		offset := min(max(item.Offset, 0), len(valueRunes))
		if offset > textPos {
			segment := valueRunes[textPos:offset]
			out.WriteString(string(segment))
			displayCount += len(segment)
			if !cursorAssigned && cursor <= offset {
				displayCursor = displayCount - (offset - cursor)
				cursorAssigned = true
			}
			textPos = offset
		}
		token := "[" + item.Name + "] "
		out.WriteString(token)
		displayCount += len([]rune(token))
		if cursor == offset {
			displayCursor = displayCount
			cursorAssigned = true
		}
	}

	if textPos < len(valueRunes) {
		segment := valueRunes[textPos:]
		out.WriteString(string(segment))
		displayCount += len(segment)
		if !cursorAssigned {
			displayCursor = displayCount - (len(valueRunes) - cursor)
		}
	} else if !cursorAssigned {
		displayCursor = displayCount
	}

	return out.String(), displayCursor
}

func composeDisplayWithToken(value string, attachments []inputAttachment, token func(string) string) string {
	valueRunes := []rune(value)
	items := cloneInputAttachments(attachments)
	var out strings.Builder
	textPos := 0
	for _, item := range items {
		offset := min(max(item.Offset, 0), len(valueRunes))
		if offset > textPos {
			appendDisplaySegment(&out, string(valueRunes[textPos:offset]))
			textPos = offset
		}
		appendDisplaySegment(&out, token(item.Name))
	}
	if textPos < len(valueRunes) {
		appendDisplaySegment(&out, string(valueRunes[textPos:]))
	}
	return strings.TrimSpace(out.String())
}

func appendDisplaySegment(out *strings.Builder, segment string) {
	if out == nil {
		return
	}
	segment = strings.TrimSpace(segment)
	if segment == "" {
		return
	}
	current := out.String()
	if current != "" {
		last, _ := lastRune(current)
		first, _ := firstRune(segment)
		if !unicode.IsSpace(last) && !unicode.IsSpace(first) {
			out.WriteByte(' ')
		}
	}
	out.WriteString(segment)
}

func submissionInput(value string, attachments []inputAttachment) (string, []inputAttachment) {
	valueRunes := []rune(value)
	start := 0
	for start < len(valueRunes) && unicode.IsSpace(valueRunes[start]) {
		start++
	}
	end := len(valueRunes)
	for end > start && unicode.IsSpace(valueRunes[end-1]) {
		end--
	}
	trimmed := string(valueRunes[start:end])
	if len(attachments) == 0 {
		return trimmed, nil
	}
	out := cloneInputAttachments(attachments)
	limit := end - start
	for i := range out {
		switch {
		case out[i].Offset <= start:
			out[i].Offset = 0
		case out[i].Offset >= end:
			out[i].Offset = limit
		default:
			out[i].Offset -= start
		}
		if out[i].Offset < 0 {
			out[i].Offset = 0
		}
		if out[i].Offset > limit {
			out[i].Offset = limit
		}
	}
	return trimmed, out
}

func inputAttachmentsToSubmission(items []inputAttachment) []Attachment {
	if len(items) == 0 {
		return nil
	}
	out := make([]Attachment, 0, len(items))
	for _, item := range cloneInputAttachments(items) {
		out = append(out, Attachment(item))
	}
	return out
}

func attachmentsToInputAttachments(items []Attachment) []inputAttachment {
	if len(items) == 0 {
		return nil
	}
	out := make([]inputAttachment, 0, len(items))
	for _, item := range cloneAttachments(items) {
		out = append(out, inputAttachment(item))
	}
	return out
}

func adjustAttachmentOffsetsForTextEdit(items []inputAttachment, before string, after string) []inputAttachment {
	if len(items) == 0 || before == after {
		return cloneInputAttachments(items)
	}
	beforeRunes := []rune(before)
	afterRunes := []rune(after)

	prefix := 0
	for prefix < len(beforeRunes) && prefix < len(afterRunes) && beforeRunes[prefix] == afterRunes[prefix] {
		prefix++
	}

	beforeSuffix := len(beforeRunes)
	afterSuffix := len(afterRunes)
	for beforeSuffix > prefix && afterSuffix > prefix && beforeRunes[beforeSuffix-1] == afterRunes[afterSuffix-1] {
		beforeSuffix--
		afterSuffix--
	}

	removed := beforeSuffix - prefix
	added := afterSuffix - prefix
	delta := added - removed

	out := cloneInputAttachments(items)
	for i := range out {
		switch {
		case out[i].Offset <= prefix:
		case out[i].Offset >= prefix+removed:
			out[i].Offset += delta
		default:
			out[i].Offset = prefix
		}
		if out[i].Offset < 0 {
			out[i].Offset = 0
		}
		if out[i].Offset > len(afterRunes) {
			out[i].Offset = len(afterRunes)
		}
	}
	return out
}

func (m *Model) syncBackendAttachments() {
	if m.cfg.SetAttachments == nil {
		m.syncAttachmentSummary()
		return
	}
	names := m.cfg.SetAttachments(attachmentNamesFromTokens(m.inputAttachments))
	m.attachmentNames = append([]string(nil), names...)
	m.attachmentCount = len(names)
}

func (m *Model) restoreHistoryEntry(text string, attachments []inputAttachment) {
	m.textarea.SetValue(text)
	m.textarea.CursorEnd()
	m.input = []rune(text)
	m.cursor = len(m.input)
	m.setInputAttachments(attachments)
	m.syncBackendAttachments()
	m.adjustTextareaHeight()
}

func (m *Model) readClipboardText() (string, error) {
	if m.cfg.ReadClipboardText != nil {
		return m.cfg.ReadClipboardText()
	}
	return defaultReadClipboardText()
}

func (m *Model) writeClipboardText(text string) error {
	if m.cfg.WriteClipboardText != nil {
		return m.cfg.WriteClipboardText(text)
	}
	return defaultWriteClipboardText(text)
}

func normalizeClipboardText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

func (m *Model) pasteClipboardText() (bool, error) {
	text, err := m.readClipboardText()
	if err != nil {
		return false, err
	}
	text = normalizeClipboardText(text)
	if text == "" {
		return false, nil
	}
	before := m.textarea.Value()
	m.textarea.InsertString(text)
	m.inputAttachments = adjustAttachmentOffsetsForTextEdit(m.inputAttachments, before, m.textarea.Value())
	m.syncAttachmentSummary()
	m.syncInputFromTextarea()
	return true, nil
}
