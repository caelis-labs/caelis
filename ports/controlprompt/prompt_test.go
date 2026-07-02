package controlprompt

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
)

func TestParseSlashAndAttachmentRange(t *testing.T) {
	cmd, args, start, ok := ParseSlash("  /review check this  ")
	if !ok || cmd != "review" || args != "check this" || start != len([]rune("/review ")) {
		t.Fatalf("ParseSlash() = %q %q %d %v", cmd, args, start, ok)
	}
	attachments := AttachmentsForPromptRange([]control.Attachment{
		{Name: "before", Offset: 1},
		{Name: "inside", Offset: start + 2},
	}, start, len([]rune("/review check this")))
	if len(attachments) != 1 || attachments[0].Name != "inside" || attachments[0].Offset != 2 {
		t.Fatalf("AttachmentsForPromptRange() = %#v", attachments)
	}
}
