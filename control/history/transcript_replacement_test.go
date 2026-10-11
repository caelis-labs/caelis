package history

import (
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
)

func TestTranscriptReplacementReplayRetainsOnlyCurrentMessage(t *testing.T) {
	for _, kind := range []string{eventstream.UpdateUserMessage, eventstream.UpdateAgentMessage} {
		t.Run(kind, func(t *testing.T) {
			var transcript Transcript
			for _, frame := range []struct {
				text    string
				replace bool
			}{{"stale", false}, {"current", true}, {"", true}, {"restored", false}} {
				e := displayEvent("turn", kind, frame.text)
				e.Visibility, e.MessageID = session.VisibilityUIOnly, "message"
				e.Protocol.Update.MessageID, e.Protocol.Update.Replacement = "message", frame.replace
				e.Protocol.Update.Content = map[string]any{"type": "text", "text": frame.text}
				transcript.Append(e)
			}
			events := transcript.Events()
			if len(events) != 1 || session.EventText(events[0]) != "restored" {
				t.Fatalf("replay retained stale bytes: %#v", events)
			}
			updates, err := projection.ProjectEvent(events[0])
			if err != nil || len(updates) != 1 {
				t.Fatalf("project replay: %v %#v", err, updates)
			}
			chunk, ok := updates[0].(eventstream.ContentChunk)
			if !ok || !chunk.Replacement || chunk.MessageID != "message" {
				t.Fatalf("replay lost replacement: %#v", updates)
			}
		})
	}
}

func TestTranscriptMetadataOnlyPatchClearsWithoutChangingBody(t *testing.T) {
	var transcript Transcript
	appendFrame := func(text string, meta map[string]any, present bool) {
		e := displayEvent("turn", eventstream.UpdateAgentMessage, text)
		e.Visibility, e.MessageID = session.VisibilityUIOnly, "message"
		e.Protocol.Update.MessageID = "message"
		e.Protocol.Update.Content = map[string]any{"type": "text", "text": text}
		e.Meta, e.Protocol.Update.Meta = meta, meta
		if present {
			e.Protocol.Update.ReplacedFields = []string{"_meta"}
		}
		transcript.Append(e)
	}
	appendFrame("body", map[string]any{"label": "old"}, false)
	appendFrame("", nil, true)
	stored := transcript.Events()
	if len(stored) != 1 || session.EventText(stored[0]) != "body" || stored[0].Meta != nil || stored[0].Protocol.Update.Meta != nil || !slices.Contains(stored[0].Protocol.Update.ReplacedFields, "_meta") {
		t.Fatalf("metadata clear changed body or retained stale metadata: %#v", stored)
	}
	appendFrame("", map[string]any{"label": "current"}, true)
	if len(transcript.Events()) != 1 || session.EventText(stored[0]) != "body" || stored[0].Protocol.Update.Meta["label"] != "current" {
		t.Fatalf("metadata update changed body or was dropped: %#v", stored)
	}
}
