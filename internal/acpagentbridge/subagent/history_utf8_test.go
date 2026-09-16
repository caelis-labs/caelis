package subagent

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func TestLoadedLongInputRetainsUTF8AtBothDisplayCuts(t *testing.T) {
	for _, char := range []string{"中", "🙂"} {
		for _, fragmented := range []bool{false, true} {
			// Put each cut inside a rune, independently of the envelope prefix size.
			head := userPromptOpen + strings.Repeat("h", (64<<10)-len(userPromptOpen)-1)
			tail := strings.Repeat("t", (64<<10)-len(char)+1)
			text := head + char + strings.Repeat("m", 200<<10) + char + tail
			c := newHistoryCollector(&Runner{clock: time.Now}, delegation.Anchor{TaskID: "task", SessionID: "child-1"}, "helper")
			chunks := []string{text}
			if fragmented {
				chunks = []string{text[:len(head)+len(char)], text[len(head)+len(char):]}
			}
			for _, chunk := range chunks {
				c.observe(contentUpdate(t, client.UpdateUserMessage, chunk))
			}
			events := c.eventsSnapshot()
			if len(events) != 1 {
				t.Fatalf("events=%d", len(events))
			}
			got := session.EventText(events[0])
			want := head + "\n[Earlier input display omitted]\n" + tail
			if !utf8.ValidString(got) || got != want {
				t.Fatalf("invalid/truncated rune for %s (fragmented=%v): valid=%v got bytes=%d want=%d", char, fragmented, utf8.ValidString(got), len(got), len(want))
			}
			if events[0].Actor.Kind != session.ActorKindUser || events[0].Message.TextContent() != got || session.ExtractProtocolText(session.ProtocolUpdateOf(events[0]).Content) != got {
				t.Fatal("input projections diverged")
			}
		}
	}
}
