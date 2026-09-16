package subagent

import (
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/control/history"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func TestHistoryProjectionStreamsLongReplayAndPreservesInputSources(t *testing.T) {
	c := newHistoryCollector(&Runner{clock: time.Now}, delegation.Anchor{SessionID: "child-1", TaskID: "task"}, "helper")
	c.openProjection()
	defer c.closeProjection()
	// Exceeds the former event limit without needing a full disk projection copy.
	for range 9000 {
		c.observe(contentUpdate(t, client.UpdateAgentMessage, "output "))
		if len(c.events) > 1 {
			t.Fatal("completed output accumulated in the input group")
		}
	}
	c.observe(contentUpdate(t, client.UpdateUserMessage, "review "))
	c.observe(contentUpdate(t, client.UpdateUserMessage, "the change"))
	c.observe(contentUpdate(t, client.UpdateUserMessage, "\n\nFrom: reviewer"))
	c.observe(contentUpdate(t, client.UpdateAgentMessage, "done"))
	c.observe(contentUpdate(t, client.UpdateUserMessage, "human guidance"))
	var output, mail, human strings.Builder
	seen := 0
	err := c.streamEvents(t.Context(), func(event *session.Event) error {
		seen++
		switch {
		case event.Actor.Name == "reviewer":
			mail.WriteString(session.EventText(event))
		case event.Actor.Kind == session.ActorKindUser:
			human.WriteString(session.EventText(event))
		default:
			output.WriteString(session.EventText(event))
		}
		return nil
	})
	if err != nil || seen > history.TranscriptEvents {
		t.Fatalf("history=%d error=%v", seen, err)
	}
	if output.String() != strings.Repeat("output ", 9000)+"done" || mail.String() != "review the change" || human.String() != "human guidance" {
		t.Fatalf("bounded replay lost text or sender provenance: output bytes=%d, want=%d, mail=%q human=%q", output.Len(), 9000*len("output ")+4, mail.String(), human.String())
	}
}
