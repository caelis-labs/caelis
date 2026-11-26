package session_test

import (
	"context"
	"encoding/json"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"testing"
)

// Compile-time contracts prove external adapters can implement and request
// only the session capabilities they consume.
var _ session.Reader = externalReader{}
var _ session.EventAppender = externalAppender{}
var _ session.StateReader = externalStateReader{}

type externalReader struct{}

func (externalReader) Session(context.Context, session.SessionRef) (session.Session, error) {
	return session.Session{}, nil
}

func (externalReader) Events(context.Context, session.EventsRequest) ([]*session.Event, error) {
	return nil, nil
}

type externalAppender struct{}

func (externalAppender) AppendEvent(context.Context, session.AppendEventRequest) (*session.Event, error) {
	return nil, nil
}

type externalStateReader struct{}

func (externalStateReader) SnapshotState(context.Context, session.SessionRef) (map[string]any, error) {
	return nil, nil
}

func TestProtocolMetadataNumbersSurviveEventRoundTrip(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"agent_message_chunk", "agent_thought_chunk", "user_message_chunk", "tool_call_update"} {
		t.Run(kind, func(t *testing.T) {
			var event session.Event
			raw := []byte(`{"_meta":{"overflow":1e400},"protocol":{"method":"session/update","update":{"sessionUpdate":"` + kind + `","_meta":{"overflow":1e400,"large":9007199254740993,"nested":{"decimal":1.2500}}}}}`)
			for range 2 {
				if err := json.Unmarshal(raw, &event); err != nil {
					t.Fatal(err)
				}
				if event.Meta["overflow"] != json.Number("1e400") {
					t.Fatalf("event metadata overflow token lost: %#v", event.Meta)
				}
				meta := session.ProtocolUpdateOf(session.CloneEvent(&event)).Meta
				if meta["overflow"] != json.Number("1e400") || meta["large"] != json.Number("9007199254740993") || meta["nested"].(map[string]any)["decimal"] != json.Number("1.2500") {
					t.Fatalf("protocol metadata lost numeric tokens: %#v", meta)
				}
				var err error
				raw, err = json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
