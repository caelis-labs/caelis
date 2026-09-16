package taskstream

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	"github.com/caelis-labs/caelis/control/history"
)

func TestRetainedChildOutputBoundsMetadataAndPreservesCoalescedFinals(t *testing.T) {
	var window retainedChildOutput
	for n := range 100 {
		id := fmt.Sprint(n)
		for _, text := range []string{"hello ", "world"} {
			window.append(recordedTaskOutput{TerminalID: "child", ActivityID: id, Event: output.Event{Event: &session.Event{
				Type: session.EventTypeAssistant, Visibility: session.VisibilityUIOnly,
				Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{SessionUpdate: "agent_message_chunk", Content: session.ProtocolTextContent(text)}},
			}}})
		}
		// The terminal fact can share a frame with detail omitted in older Turns.
		window.append(recordedTaskOutput{TerminalID: "child", ActivityID: id, Event: output.Event{State: "completed", Closed: true, Event: &session.Event{
			Type: session.EventTypeToolResult, Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{SessionUpdate: "tool_call_update"}},
		}}})
		if len(window.frames) != len(window.transcript.Events()) || len(window.lifecycle) > history.MaxTurns {
			t.Fatalf("companion metadata retained evicted history: frames=%d events=%d lifecycle=%d", len(window.frames), len(window.transcript.Events()), len(window.lifecycle))
		}
	}
	records, err := window.records()
	if err != nil {
		t.Fatal(err)
	}
	answers, finals := 0, 0
	for _, raw := range records {
		var record recordedTaskOutput
		if err := json.Unmarshal(raw.Payload, &record); err != nil {
			t.Fatal(err)
		}
		if event := record.Event.Event; event != nil && event.Type == session.EventTypeAssistant {
			answers++
			if session.EventText(event) != "hello world" {
				t.Fatalf("coalesced dialogue lost: %+v", event)
			}
		}
		if record.Event.Closed {
			finals++
		}
	}
	if answers != history.MaxTurns || finals != history.MaxTurns {
		t.Fatalf("answers=%d finals=%d, want %d each", answers, finals, history.MaxTurns)
	}
}
