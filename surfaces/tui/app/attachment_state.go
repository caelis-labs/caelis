package tuiapp

import (
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Attachment tokens occupy one Private-Use rune in the textarea value so
// Left/Right/Backspace treat them as a single character. The rune encodes a
// stable ID so multi-token delete cannot rebind the wrong payload.
//
// Display expands each sentinel to "[image #N]" / "[Pasted: N lines]".
// Submit/history expand pastes to raw text and keep images as file attachments.
const (
	attachmentSentinelMin = 0xE000 // Private Use Area
	attachmentSentinelMax = 0xF8FF
	// Legacy anonymous sentinel (pre-ID model); treated as orphan if seen.
	attachmentSentinelLegacy = '\uFFFC'
)

type attachmentKind int

const (
	attachmentKindImage attachmentKind = iota
	attachmentKindPaste
)

const (
	// Collapse only larger pastes so short snippets stay editable inline.
	// ~15 lines or a long single-line blob (~2k runes) before folding.
	pasteCollapseMinLines = 15
	pasteCollapseMinRunes = 2000
)

// nextAttachmentID allocates stable token identities (not reset per model so
// tests that share a process still get unique IDs).
var nextAttachmentID uint32 = 1

func allocAttachmentID() uint32 {
	id := nextAttachmentID
	nextAttachmentID++
	if nextAttachmentID == 0 {
		nextAttachmentID = 1
	}
	return id
}

func sentinelRuneForID(id uint32) rune {
	if id == 0 {
		return attachmentSentinelLegacy
	}
	span := uint32(attachmentSentinelMax - attachmentSentinelMin + 1)
	return rune(uint32(attachmentSentinelMin) + ((id - 1) % span))
}

func isAttachmentSentinel(r rune) bool {
	if r == attachmentSentinelLegacy {
		return true
	}
	return r >= attachmentSentinelMin && r <= attachmentSentinelMax
}

func (item inputAttachment) sentinelRune() rune {
	return sentinelRuneForID(item.ID)
}

func cloneInputAttachments(items []inputAttachment) []inputAttachment {
	if len(items) == 0 {
		return nil
	}
	out := make([]inputAttachment, 0, len(items))
	for _, item := range items {
		offset := max(item.Offset, 0)
		switch item.Kind {
		case attachmentKindPaste:
			if strings.TrimSpace(item.Content) == "" {
				continue
			}
			out = append(out, inputAttachment{
				ID:      item.ID,
				Kind:    attachmentKindPaste,
				Name:    strings.TrimSpace(item.Name),
				Offset:  offset,
				Content: item.Content,
			})
		default:
			name := strings.TrimSpace(item.Name)
			if name == "" {
				continue
			}
			out = append(out, inputAttachment{
				ID:     item.ID,
				Kind:   attachmentKindImage,
				Name:   name,
				Offset: offset,
			})
		}
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

func isPasteAttachment(item inputAttachment) bool {
	return item.Kind == attachmentKindPaste
}

func isFileAttachment(item inputAttachment) bool {
	return item.Kind == attachmentKindImage && strings.TrimSpace(item.Name) != ""
}

func attachmentNamesFromTokens(items []inputAttachment) []string {
	if len(items) == 0 {
		return nil
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		if !isFileAttachment(item) {
			continue
		}
		names = append(names, strings.TrimSpace(item.Name))
	}
	if len(names) == 0 {
		return nil
	}
	return names
}

func (m *Model) syncAttachmentSummary() {
	items := cloneInputAttachments(m.inputAttachments)
	names := attachmentNamesFromTokens(items)
	m.attachmentNames = append([]string(nil), names...)
	m.attachmentCount = len(items)
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

func (m *Model) syncBackendAttachments() {
	if m.cfg.SetAttachments == nil {
		m.syncAttachmentSummary()
		return
	}
	names := m.cfg.SetAttachments(attachmentNamesFromTokens(m.inputAttachments))
	m.attachmentNames = append([]string(nil), names...)
	m.attachmentCount = len(cloneInputAttachments(m.inputAttachments))
}

// reconcileAttachmentsAfterEdit rebinds metadata by stable token ID encoded in
// each sentinel rune, then strips orphan sentinels. before is unused for
// identity (kept for call-site clarity / future diff hooks).
func (m *Model) reconcileAttachmentsAfterEdit(before, after string) {
	if m == nil {
		return
	}
	_ = before
	m.applyAttachmentValue(after, rebindAttachmentsByID(after, m.inputAttachments))
}

// reconcileAttachmentsWithValue rebinds against the current textarea value.
func (m *Model) reconcileAttachmentsWithValue() {
	if m == nil {
		return
	}
	m.applyAttachmentValue(m.textarea.Value(), rebindAttachmentsByID(m.textarea.Value(), m.inputAttachments))
}

func (m *Model) applyAttachmentValue(value string, items []inputAttachment) {
	cleaned, items, changed := stripOrphanSentinels(value, items)
	m.inputAttachments = items
	if changed {
		cursor := m.textareaCursorIndex()
		m.textarea.SetValue(cleaned)
		if n := len([]rune(cleaned)); n >= 0 {
			m.moveTextareaCursorToIndex(min(cursor, n))
		}
		m.inputAttachments = rebindAttachmentsByID(cleaned, items)
	}
	m.syncAttachmentSummary()
}

// rebindAttachmentsByID keeps only attachments whose sentinel rune still
// appears in value, updating Offset. Middle deletes cannot steal another
// token's payload because each token has a unique Private-Use rune.
func rebindAttachmentsByID(value string, items []inputAttachment) []inputAttachment {
	items = cloneInputAttachments(items)
	if len(items) == 0 {
		return nil
	}
	byRune := make(map[rune]inputAttachment, len(items))
	for _, item := range items {
		if item.ID == 0 {
			continue
		}
		byRune[item.sentinelRune()] = item
	}
	seen := make(map[uint32]struct{}, len(items))
	var out []inputAttachment
	for i, r := range []rune(value) {
		if !isAttachmentSentinel(r) {
			continue
		}
		item, ok := byRune[r]
		if !ok {
			continue
		}
		if _, dup := seen[item.ID]; dup {
			continue
		}
		seen[item.ID] = struct{}{}
		item.Offset = i
		out = append(out, item)
	}
	return sortInputAttachments(out)
}

// stripOrphanSentinels removes sentinel runes with no matching attachment ID.
func stripOrphanSentinels(value string, items []inputAttachment) (string, []inputAttachment, bool) {
	items = rebindAttachmentsByID(value, items)
	keepRune := make(map[rune]struct{}, len(items))
	for _, item := range items {
		keepRune[item.sentinelRune()] = struct{}{}
	}
	var b strings.Builder
	changed := false
	for _, r := range value {
		if isAttachmentSentinel(r) {
			if _, ok := keepRune[r]; !ok {
				changed = true
				continue
			}
		}
		b.WriteRune(r)
	}
	if !changed {
		return value, items, false
	}
	cleaned := b.String()
	return cleaned, rebindAttachmentsByID(cleaned, items), true
}

func (m *Model) nextAttachmentIdentity() (id uint32, sentinel rune) {
	used := make(map[rune]struct{})
	for _, item := range m.inputAttachments {
		if item.ID != 0 {
			used[item.sentinelRune()] = struct{}{}
		}
	}
	for _, r := range m.textarea.Value() {
		if isAttachmentSentinel(r) {
			used[r] = struct{}{}
		}
	}
	for tries := 0; tries < 10000; tries++ {
		id = allocAttachmentID()
		sentinel = sentinelRuneForID(id)
		if _, taken := used[sentinel]; !taken {
			return id, sentinel
		}
	}
	// Extremely unlikely: fall back to legacy FFFC (identity weak for that one).
	return allocAttachmentID(), attachmentSentinelLegacy
}

func (m *Model) insertAttachmentsAtCursor(names []string) {
	if len(names) == 0 {
		return
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		id, sentinel := m.nextAttachmentIdentity()
		offset := m.textareaCursorIndex()
		m.textarea.InsertString(string(sentinel))
		m.inputAttachments = append(m.inputAttachments, inputAttachment{
			ID:     id,
			Kind:   attachmentKindImage,
			Name:   name,
			Offset: offset,
		})
	}
	m.reconcileAttachmentsWithValue()
	m.syncBackendAttachments()
	m.syncTextareaChrome()
	m.syncInputFromTextareaAndFollow()
}

func (m *Model) removeAttachmentAtCursor() bool {
	// Deletes the sentinel immediately before the caret (same as Backspace on
	// a token). Prefer the normal textarea Backspace path in production.
	if m == nil || len(m.inputAttachments) == 0 {
		return false
	}
	cursor := m.textareaCursorIndex()
	if cursor <= 0 {
		return false
	}
	before := m.textarea.Value()
	runes := []rune(before)
	if cursor-1 >= len(runes) || !isAttachmentSentinel(runes[cursor-1]) {
		return false
	}
	after := string(append(append([]rune{}, runes[:cursor-1]...), runes[cursor:]...))
	m.textarea.SetValue(after)
	m.moveTextareaCursorToIndex(cursor - 1)
	m.reconcileAttachmentsAfterEdit(before, after)
	m.syncBackendAttachments()
	m.syncTextareaChrome()
	m.syncInputFromTextareaAndFollow()
	return true
}

func shouldCollapsePaste(text string) bool {
	text = normalizeClipboardText(text)
	if strings.TrimSpace(text) == "" {
		return false
	}
	if len(strings.Split(text, "\n")) >= pasteCollapseMinLines {
		return true
	}
	return len([]rune(text)) >= pasteCollapseMinRunes
}

func pasteLineCount(text string) int {
	if text == "" {
		return 0
	}
	return len(strings.Split(text, "\n"))
}

func pastedTextToken(lines int) string {
	if lines < 1 {
		lines = 1
	}
	if lines == 1 {
		return "[Pasted: 1 line] "
	}
	return "[Pasted: " + strconv.Itoa(lines) + " lines] "
}

func imageAttachmentToken(index int) string {
	if index < 1 {
		index = 1
	}
	return "[image #" + strconv.Itoa(index) + "] "
}

func attachmentDisplayToken(item inputAttachment, imageIndex int) string {
	if isPasteAttachment(item) {
		return pastedTextToken(pasteLineCount(item.Content))
	}
	return imageAttachmentToken(imageIndex)
}

// insertComposerTextOrCollapse inserts text into the composer. Large pastes
// become a single sentinel rune shown as [Pasted: N lines].
func (m *Model) insertComposerTextOrCollapse(text string) {
	if m == nil {
		return
	}
	text = normalizeClipboardText(text)
	if text == "" {
		return
	}
	if !shouldCollapsePaste(text) {
		m.insertComposerText(text)
		return
	}
	id, sentinel := m.nextAttachmentIdentity()
	offset := m.textareaCursorIndex()
	m.textarea.InsertString(string(sentinel))
	m.inputAttachments = append(m.inputAttachments, inputAttachment{
		ID:      id,
		Kind:    attachmentKindPaste,
		Offset:  offset,
		Content: text,
	})
	m.reconcileAttachmentsWithValue()
	m.syncInputFromTextareaAndFollow()
	m.syncTextareaChrome()
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
	m.insertComposerTextOrCollapse(text)
	return true, nil
}

func composeInputDisplay(value string, cursor int, attachments []inputAttachment) (string, int) {
	display, displayCursor, _ := composeInputDisplayWithMap(value, cursor, attachments)
	return display, displayCursor
}

// composeInputDisplayWithMap expands sentinel runes to display tokens. Each
// token is one value index (the sentinel), so caret movement is native.
func composeInputDisplayWithMap(value string, cursor int, attachments []inputAttachment) (string, int, []int) {
	valueRunes := []rune(value)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(valueRunes) {
		cursor = len(valueRunes)
	}

	items := sortInputAttachments(cloneInputAttachments(attachments))
	byRune := make(map[rune]inputAttachment, len(items))
	imageIndexAt := make(map[uint32]int, len(items))
	imageIndex := 0
	for _, item := range items {
		byRune[item.sentinelRune()] = item
		if isFileAttachment(item) {
			imageIndex++
			imageIndexAt[item.ID] = imageIndex
		}
	}

	var out strings.Builder
	displayToValue := []int{0}
	displayCursor := 0
	displayCount := 0
	cursorAssigned := false

	for i, r := range valueRunes {
		if !cursorAssigned && cursor == i {
			displayCursor = displayCount
			cursorAssigned = true
		}
		if isAttachmentSentinel(r) {
			item, ok := byRune[r]
			token := string(r)
			if ok {
				token = attachmentDisplayToken(item, imageIndexAt[item.ID])
			}
			for range []rune(token) {
				displayToValue = append(displayToValue, i)
			}
			out.WriteString(token)
			displayCount += len([]rune(token))
			continue
		}
		out.WriteRune(r)
		displayToValue = append(displayToValue, i+1)
		displayCount++
	}
	if !cursorAssigned {
		displayCursor = displayCount
	}
	return out.String(), displayCursor, displayToValue
}

func composeDisplayWithToken(value string, attachments []inputAttachment, token func(item inputAttachment, imageIndex int) string) string {
	// Transcript/display helper: value should already be sentinel-free for
	// expanded pastes; images are applied by offset on the expanded string.
	valueRunes := []rune(value)
	items := sortInputAttachments(cloneInputAttachments(attachments))
	var out strings.Builder
	textPos := 0
	imageIndex := 0
	for _, item := range items {
		offset := min(max(item.Offset, 0), len(valueRunes))
		if offset > textPos {
			appendDisplaySegment(&out, string(valueRunes[textPos:offset]))
			textPos = offset
		}
		if isFileAttachment(item) {
			imageIndex++
		}
		appendDisplaySegment(&out, token(item, imageIndex))
	}
	if textPos < len(valueRunes) {
		appendDisplaySegment(&out, string(valueRunes[textPos:]))
	}
	return strings.TrimSpace(out.String())
}

// expandComposerText inlines paste bodies and strips sentinels for gateway text.
func expandComposerText(value string, attachments []inputAttachment) string {
	expanded, _ := expandPastesRemapImages(value, attachments)
	return expanded
}

// expandPastesRemapImages walks the composer value: paste sentinels become
// original Content, image sentinels are removed and returned as file
// attachments with offsets in the expanded string. Collapse is composer-only.
func expandPastesRemapImages(value string, attachments []inputAttachment) (string, []inputAttachment) {
	items := sortInputAttachments(cloneInputAttachments(attachments))
	hasSentinel := false
	for _, r := range value {
		if isAttachmentSentinel(r) {
			hasSentinel = true
			break
		}
	}
	if len(items) == 0 && !hasSentinel {
		return value, nil
	}
	byRune := make(map[rune]inputAttachment, len(items))
	for _, item := range items {
		byRune[item.sentinelRune()] = item
	}
	var out strings.Builder
	var images []inputAttachment
	for _, r := range value {
		if !isAttachmentSentinel(r) {
			out.WriteRune(r)
			continue
		}
		item, ok := byRune[r]
		if !ok {
			continue
		}
		if isPasteAttachment(item) {
			out.WriteString(item.Content)
			continue
		}
		if isFileAttachment(item) {
			images = append(images, inputAttachment{
				ID:     item.ID,
				Kind:   attachmentKindImage,
				Name:   item.Name,
				Offset: len([]rune(out.String())),
			})
		}
	}
	return out.String(), images
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
	return trimmed, rebindAttachmentsByID(trimmed, attachments)
}

func inputAttachmentsToSubmission(items []inputAttachment) []Attachment {
	if len(items) == 0 {
		return nil
	}
	out := make([]Attachment, 0, len(items))
	for _, item := range cloneInputAttachments(items) {
		if !isFileAttachment(item) {
			continue
		}
		out = append(out, Attachment{Name: item.Name, Offset: item.Offset})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func attachmentsToInputAttachments(items []Attachment) []inputAttachment {
	if len(items) == 0 {
		return nil
	}
	out := make([]inputAttachment, 0, len(items))
	for _, item := range cloneAttachments(items) {
		out = append(out, inputAttachment{
			Kind:   attachmentKindImage,
			Name:   item.Name,
			Offset: item.Offset,
		})
	}
	return out
}

func adjustAttachmentOffsetsForTextEdit(items []inputAttachment, before string, after string) []inputAttachment {
	_ = before
	return rebindAttachmentsByID(after, items)
}

func (m *Model) restoreHistoryEntry(text string, attachments []inputAttachment) {
	// History stores expanded paste text (no paste tokens). Image attachments
	// may still carry offsets into that expanded string — re-insert sentinels.
	m.textarea.SetValue("")
	m.inputAttachments = nil
	m.input = nil
	m.cursor = 0

	expanded := text
	images := sortInputAttachments(cloneInputAttachments(attachments))
	if len(images) == 0 {
		m.textarea.SetValue(expanded)
		m.textarea.MoveToEnd()
		m.input = []rune(expanded)
		m.cursor = len(m.input)
		m.adjustTextareaHeight()
		m.followComposerCursor()
		return
	}

	// Rebuild value with image sentinels at recorded offsets into expanded text.
	runes := []rune(expanded)
	var b strings.Builder
	var rebuilt []inputAttachment
	imgIdx := 0
	pos := 0
	for i := 0; i <= len(runes); i++ {
		for imgIdx < len(images) && images[imgIdx].Offset == i {
			id, sentinel := m.nextAttachmentIdentity()
			b.WriteRune(sentinel)
			item := images[imgIdx]
			item.ID = id
			item.Kind = attachmentKindImage
			item.Offset = pos
			rebuilt = append(rebuilt, item)
			imgIdx++
			pos++
		}
		if i < len(runes) {
			b.WriteRune(runes[i])
			pos++
		}
	}
	for imgIdx < len(images) {
		id, sentinel := m.nextAttachmentIdentity()
		b.WriteRune(sentinel)
		item := images[imgIdx]
		item.ID = id
		item.Kind = attachmentKindImage
		item.Offset = pos
		rebuilt = append(rebuilt, item)
		imgIdx++
		pos++
	}
	m.textarea.SetValue(b.String())
	m.inputAttachments = rebuilt
	m.textarea.MoveToEnd()
	m.input = []rune(m.textarea.Value())
	m.cursor = len(m.input)
	m.syncBackendAttachments()
	m.adjustTextareaHeight()
	m.followComposerCursor()
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

// prepareComposerSubmission expands paste tokens and builds gateway/transcript
// payloads. Collapse remains composer-display-only.
func (m *Model) prepareComposerSubmission() (execLine, displayLine string, fileAttachments []Attachment) {
	line, attachments := submissionInput(m.textarea.Value(), m.inputAttachments)
	expanded, images := expandPastesRemapImages(line, attachments)
	execLine = strings.TrimSpace(expanded)
	displayLine = m.displayLineWithInputAttachments(expanded, images)
	fileAttachments = inputAttachmentsToSubmission(images)
	return execLine, displayLine, fileAttachments
}
