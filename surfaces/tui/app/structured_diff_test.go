package tuiapp

import (
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/surfaces/tui/acpprojector"
)

func TestStructuredDiffDisplaySurvivesPublicWire(t *testing.T) {
	t.Parallel()
	old := " old.txt "
	changes := []eventstream.ToolDiffChange{{Operation: "move", Path: " new.txt ", OldPath: &old}}
	patch := "diff --git a/old.txt b/new.txt\r\n-old\r\n+new\r\n"
	env := eventstream.Envelope{Kind: eventstream.KindSessionUpdate, Update: eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "edit", Content: []eventstream.ToolCallContent{{Type: "diff", Changes: &changes, Patch: &eventstream.ToolDiffPatch{Format: "git_patch", Text: patch}}}}}
	raw, err := wirev1.MarshalEnvelope(env)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := wirev1.UnmarshalEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []eventstream.Envelope{env, restored} {
		got := acpprojector.FormatToolContent(acpToolContentToDisplay(source.Update.(eventstream.ToolCallUpdate).Content))
		if got != "move  new.txt  (from  old.txt )\n"+patch {
			t.Fatalf("display changed exact paths or patch bytes: %q", got)
		}
	}
	empty := []eventstream.ToolDiffChange{}
	if got := acpprojector.FormatToolContent(acpToolContentToDisplay([]eventstream.ToolCallContent{{Type: "diff", Changes: &empty}})); got != "" {
		t.Fatalf("empty diff = %q", got)
	}
}
