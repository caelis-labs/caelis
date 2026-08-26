package codex

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSteeringWithoutActiveTurnRequiresPrompt(t *testing.T) {
	a := &agent{sessions: map[string]*sessionState{
		"thread-1": {threadID: "thread-1"},
	}}
	response, err := a.HandleExtensionMethod(context.Background(), sessionSteeringMethod, json.RawMessage(`{
		"sessionId":"thread-1","prompt":[{"type":"text","text":"continue"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"outcome":"promptRequired","reason":"noRunningTurn"}` {
		t.Fatalf("steering response = %s", encoded)
	}
}
