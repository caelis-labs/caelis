package eventstream

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestEnvelopeMarshalIncludesACPUpdate(t *testing.T) {
	env := Envelope{
		Kind:      KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "hello"},
		},
	}

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal(Envelope) error = %v", err)
	}
	text := string(data)
	for _, want := range []string{
		`"kind":"session/update"`,
		`"update":`,
		`"sessionUpdate":"agent_message_chunk"`,
		`"text":"hello"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("json = %s, want %s", text, want)
		}
	}
}

func TestEnvelopeMarshalIncludesContentChunkFinalFalse(t *testing.T) {
	final := false
	env := Envelope{
		Kind:      KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "hel"},
			Final:         &final,
		},
	}

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal(Envelope) error = %v", err)
	}
	if !strings.Contains(string(data), `"final":false`) {
		t.Fatalf("json = %s, want content chunk final=false", data)
	}
}
