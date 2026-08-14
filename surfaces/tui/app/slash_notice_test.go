package tuiapp

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestClassifyControlSlashNotice(t *testing.T) {
	t.Parallel()

	cases := []struct {
		text string
		want SlashNoticePlacement
	}{
		{text: "usage: /status", want: SlashNoticeHint},
		{text: "Unknown command: /foo", want: SlashNoticeHint},
		{text: "Unknown skill: lint", want: SlashNoticeHint},
		{text: "available sessions:\n  s-1", want: SlashNoticeContent},
		{text: "Switched to gpt-5.6", want: SlashNoticeFeedback},
		{text: "Context compacted", want: SlashNoticeFeedback},
	}
	for _, tc := range cases {
		if got := classifyControlSlashNotice(tc.text); got != tc.want {
			t.Fatalf("classifyControlSlashNotice(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestSlashNoticeHintDoesNotEnterTranscript(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	next, cmd := model.handleSlashNoticeMsg(SlashNoticeMsg{
		Text:      "usage: /status",
		Placement: SlashNoticeHint,
	})
	model = next.(*Model)
	if cmd == nil {
		t.Fatal("hint notice returned no clear command")
	}
	if strings.Contains(ansi.Strip(model.hint), "usage: /status") == false {
		t.Fatalf("hint = %q, want usage text", model.hint)
	}
	if model.doc != nil && model.doc.Len() != 0 {
		t.Fatalf("document blocks = %d, want usage to stay out of transcript", model.doc.Len())
	}
}
