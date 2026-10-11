package headless

import (
	"testing"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestRunSessionOnceAcceptsEmptyMessageReplacement(t *testing.T) {
	for _, resume := range []bool{false, true} {
		frames := []eventstream.Envelope{}
		for _, frame := range []struct {
			text    string
			replace bool
		}{{"stale", false}, {"snapshot", true}, {"", true}} {
			frames = append(frames, eventstream.Envelope{Kind: eventstream.KindSessionUpdate, Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "message", Content: eventstream.TextContent{Type: "text", Text: frame.text}, Replacement: frame.replace}})
		}
		want := ""
		if resume {
			want = "current"
			frames = append(frames, eventstream.Envelope{Kind: eventstream.KindSessionUpdate, Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "message", Content: eventstream.TextContent{Type: "text", Text: want}}})
		}
		result, err := RunSessionOnce(t.Context(), fakeStarter{turn: newFakeACPHandle(frames)}, appserver.SessionTurnStartRequest{SessionID: "session-1", Input: "hello"}, Options{})
		if err != nil || result.Output != want {
			t.Fatalf("resume=%v result=%q want=%q err=%v", resume, result.Output, want, err)
		}
	}
}
