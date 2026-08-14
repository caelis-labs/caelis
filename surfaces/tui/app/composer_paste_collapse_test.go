package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func multiLinePaste(prefix string, n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("%s-%02d", prefix, i)
	}
	return strings.Join(lines, "\n")
}

func TestLargePasteCollapsesToPastedToken(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)

	text := multiLinePaste("big-paste-line", pasteCollapseMinLines)
	updated, _ = m.Update(tea.PasteMsg{Content: text})
	m = updated.(*Model)

	// Value holds one sentinel rune, not the full paste body.
	if runes := []rune(m.textarea.Value()); len(runes) != 1 || !isAttachmentSentinel(runes[0]) {
		t.Fatalf("textarea value = %q, want single attachment sentinel", m.textarea.Value())
	}
	if len(m.inputAttachments) != 1 || !isPasteAttachment(m.inputAttachments[0]) {
		t.Fatalf("attachments = %#v, want one paste attachment", m.inputAttachments)
	}
	if m.inputAttachments[0].Content != text {
		t.Fatalf("paste content mismatch")
	}
	assertComposerRenderContains(t, m, fmt.Sprintf("[Pasted: %d lines]", pasteCollapseMinLines))
	layout := m.buildComposeInputLayout()
	if layout.layout.totalRows > maxInputBarRows {
		t.Fatalf("composer totalRows=%d, want <= %d after collapse", layout.layout.totalRows, maxInputBarRows)
	}
}

func TestMediumPasteBelowThresholdStaysRaw(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)

	// Just under the line threshold should remain editable inline.
	text := multiLinePaste("medium", pasteCollapseMinLines-1)
	m.insertComposerTextOrCollapse(text)
	if got := m.textarea.Value(); got != text {
		t.Fatalf("textarea value = %q, want raw multi-line paste", got)
	}
	if len(m.inputAttachments) != 0 {
		t.Fatalf("attachments = %#v, want none below collapse threshold", m.inputAttachments)
	}
}

func TestSmallPasteStillInsertsRawText(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)

	updated, _ = m.Update(tea.PasteMsg{Content: "hello paste"})
	m = updated.(*Model)
	if got := m.textarea.Value(); got != "hello paste" {
		t.Fatalf("textarea value = %q, want raw small paste", got)
	}
	if len(m.inputAttachments) != 0 {
		t.Fatalf("attachments = %#v, want none for small paste", m.inputAttachments)
	}
}

func TestCollapsedPasteSubmitsExpandedTextToTranscript(t *testing.T) {
	t.Parallel()
	var got Submission
	model := NewModel(Config{
		NoAnimation: true,
		ExecuteLine: func(submission Submission) TaskResultMsg {
			got = submission
			return TaskResultMsg{}
		},
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)

	text := multiLinePaste("submit-line", pasteCollapseMinLines)
	m.textarea.SetValue("see: ")
	m.textarea.MoveToEnd()
	m.syncInputFromTextarea()
	m.insertComposerTextOrCollapse(text)

	assertComposerRenderContains(t, m, fmt.Sprintf("[Pasted: %d lines]", pasteCollapseMinLines))

	execLine, displayLine, fileAttachments := m.prepareComposerSubmission()
	_, cmd := m.submitLineWithDisplayAndAttachmentsOptions(execLine, displayLine, fileAttachments, submitLineOptions{
		recordHistory: true,
	})
	if cmd == nil {
		t.Fatal("expected execute command")
	}
	runTeaCmds(t, cmd)

	last := fmt.Sprintf("submit-line-%02d", pasteCollapseMinLines-1)
	if !strings.Contains(got.Text, "submit-line-00") || !strings.Contains(got.Text, last) {
		t.Fatalf("submission.Text = %q, want expanded paste body", got.Text)
	}
	if !strings.Contains(got.Text, "see:") {
		t.Fatalf("submission.Text = %q, want surrounding typed text preserved", got.Text)
	}
	if strings.Contains(got.DisplayText, "[Pasted:") {
		t.Fatalf("submission.DisplayText = %q, want expanded text without paste token", got.DisplayText)
	}
	if !strings.Contains(got.DisplayText, "submit-line-00") {
		t.Fatalf("submission.DisplayText = %q, want original paste lines", got.DisplayText)
	}
	if len(got.Attachments) != 0 {
		t.Fatalf("submission.Attachments = %#v, want no file attachments for paste-only", got.Attachments)
	}
	if len(m.history) == 0 || !strings.Contains(m.history[len(m.history)-1], last) {
		t.Fatalf("history = %#v, want expanded paste entry", m.history)
	}
}

func TestRebindDropsMiddleTokenIdentity(t *testing.T) {
	t.Parallel()
	// Three pastes with unique sentinel IDs; delete the middle rune — survivors must be A+C.
	a := inputAttachment{ID: 10, Kind: attachmentKindPaste, Offset: 0, Content: "AAA\nAAA\nAAA\nAAA\nAAA"}
	b := inputAttachment{ID: 11, Kind: attachmentKindPaste, Offset: 1, Content: "BBB\nBBB\nBBB\nBBB\nBBB"}
	c := inputAttachment{ID: 12, Kind: attachmentKindPaste, Offset: 2, Content: "CCC\nCCC\nCCC\nCCC\nCCC"}
	before := string([]rune{a.sentinelRune(), b.sentinelRune(), c.sentinelRune()})
	after := string([]rune{a.sentinelRune(), c.sentinelRune()}) // middle B removed
	got := rebindAttachmentsByID(after, []inputAttachment{a, b, c})
	if len(got) != 2 {
		t.Fatalf("rebind = %#v, want 2 survivors", got)
	}
	if got[0].Content != a.Content || got[1].Content != c.Content {
		t.Fatalf("rebind contents = %q, %q; want A then C (not A then B)", got[0].Content, got[1].Content)
	}
	if got[0].Offset != 0 || got[1].Offset != 1 {
		t.Fatalf("offsets = %d,%d want 0,1", got[0].Offset, got[1].Offset)
	}
	_ = before
}

func TestCollapsedPasteRemovedByBackspaceLikeImage(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)

	m.insertComposerTextOrCollapse(multiLinePaste("L", pasteCollapseMinLines))
	if len(m.inputAttachments) != 1 {
		t.Fatalf("attachments = %#v", m.inputAttachments)
	}
	// Caret after sentinel; Backspace deletes the atom in one step.
	m.textarea.MoveToEnd()
	m.syncInputFromTextarea()
	updated, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	m = updated.(*Model)
	if len(m.inputAttachments) != 0 {
		t.Fatalf("attachments after Backspace = %#v, want none", m.inputAttachments)
	}
	for _, r := range m.textarea.Value() {
		if isAttachmentSentinel(r) {
			t.Fatalf("textarea still contains sentinel: %q", m.textarea.Value())
		}
	}
}

func TestPastedTokenNativeCaretSteps(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)

	// "测试" + collapsed paste sentinel.
	m.textarea.SetValue("测试")
	m.textarea.MoveToEnd()
	m.syncInputFromTextarea()
	m.insertComposerTextOrCollapse(multiLinePaste("L", pasteCollapseMinLines))

	// Value is 测试 + sentinel (3 runes). Start at far left.
	m.moveTextareaCursorToIndex(0)

	// 测|
	updated, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	m = updated.(*Model)
	if m.textareaCursorIndex() != 1 {
		t.Fatalf("after 1st Right: cursor=%d, want 1", m.textareaCursorIndex())
	}
	// 测试| (before sentinel/token)
	updated, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	m = updated.(*Model)
	if m.textareaCursorIndex() != 2 {
		t.Fatalf("after 2nd Right: cursor=%d, want 2 (before token)", m.textareaCursorIndex())
	}
	// 测试TOKEN| (after sentinel)
	updated, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	m = updated.(*Model)
	if m.textareaCursorIndex() != 3 {
		t.Fatalf("after 3rd Right: cursor=%d, want 3 (after token)", m.textareaCursorIndex())
	}
	// Backspace deletes whole token.
	updated, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	m = updated.(*Model)
	if len(m.inputAttachments) != 0 {
		t.Fatalf("attachments = %#v, want none after backspace on token", m.inputAttachments)
	}
	if got := m.textarea.Value(); got != "测试" {
		t.Fatalf("textarea = %q, want 测试", got)
	}
}

func TestBackspaceBeforePastedTokenDeletesPrecedingRune(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)

	m.textarea.SetValue("测试")
	m.textarea.MoveToEnd()
	m.syncInputFromTextarea()
	m.insertComposerTextOrCollapse(multiLinePaste("L", pasteCollapseMinLines))
	// Caret before sentinel: index 2.
	m.moveTextareaCursorToIndex(2)

	updated, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	m = updated.(*Model)
	if runes := []rune(m.textarea.Value()); len(runes) != 2 || runes[0] != '测' || !isAttachmentSentinel(runes[1]) {
		t.Fatalf("textarea = %q, want 测+sentinel", m.textarea.Value())
	}
	if len(m.inputAttachments) != 1 || !isPasteAttachment(m.inputAttachments[0]) {
		t.Fatalf("attachments = %#v, want paste atom preserved", m.inputAttachments)
	}
	if m.inputAttachments[0].Offset != 1 {
		t.Fatalf("paste offset = %d, want 1", m.inputAttachments[0].Offset)
	}
}

func TestClipboardLargePasteCollapses(t *testing.T) {
	t.Parallel()
	big := multiLinePaste("clip", pasteCollapseMinLines)
	model := NewModel(Config{
		NoAnimation: true,
		ReadClipboardText: func() (string, error) {
			return big, nil
		},
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)
	ok, err := m.pasteClipboardText()
	if err != nil || !ok {
		t.Fatalf("pasteClipboardText() = %v, %v", ok, err)
	}
	if runes := []rune(m.textarea.Value()); len(runes) != 1 || !isAttachmentSentinel(runes[0]) {
		t.Fatalf("textarea = %q, want sentinel", m.textarea.Value())
	}
	assertComposerRenderContains(t, m, "[Pasted:")
}

func TestLongSingleLinePasteCollapsesByRuneBudget(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)

	text := strings.Repeat("x", pasteCollapseMinRunes)
	m.insertComposerTextOrCollapse(text)
	if runes := []rune(m.textarea.Value()); len(runes) != 1 || !isAttachmentSentinel(runes[0]) {
		t.Fatalf("textarea = %q, want sentinel", m.textarea.Value())
	}
	if len(m.inputAttachments) != 1 || !isPasteAttachment(m.inputAttachments[0]) {
		t.Fatalf("attachments = %#v", m.inputAttachments)
	}
	assertComposerRenderContains(t, m, "[Pasted: 1 line]")
}

func runTeaCmds(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	msg := cmd()
	switch typed := msg.(type) {
	case tea.BatchMsg:
		for _, child := range typed {
			runTeaCmds(t, child)
		}
	default:
		_ = typed
	}
}
