package model

import (
	"encoding/json"
	"testing"
)

func TestToolCallArgsMatchDurableJSONEncoding(t *testing.T) {
	input := " { \"id\" : 9007199254740993, \"ratio\" : 0.12345678901234567890, \"note\" : \"<user>&\u2028\", \"nested\": { \"n\": 2 } } "
	args, err := ParseToolCallArgsRaw(input)
	if err != nil {
		t.Fatal(err)
	}
	message := NewMessage(RoleAssistant, NewToolUsePart("call-1", "Write", args))
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var restored Message
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if got := restored.ToolCalls()[0].Args; got != string(args) {
		t.Fatalf("tool args changed across persistence: live=%s restored=%s", args, got)
	}
	want := `{"id":9007199254740993,"ratio":0.12345678901234567890,"note":"\u003cuser\u003e\u0026\u2028","nested":{"n":2}}`
	if string(args) != want {
		t.Fatalf("canonical args = %s, want %s (retain numeric lexemes and key order)", args, want)
	}
}
