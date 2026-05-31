package model

import (
	"encoding/json"
	"testing"
)

func TestNormalizeToolInput(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "object", raw: `{"command":"echo hi"}`, want: `{"command":"echo hi"}`},
		{name: "empty", raw: ``, want: `{}`},
		{name: "null", raw: `null`, want: `{}`},
		{name: "quoted object", raw: `"{\"command\":\"echo hi\"}"`, want: `{"command":"echo hi"}`},
		{name: "fenced object", raw: "```json\n{\"command\":\"echo hi\"}\n```", want: `{"command":"echo hi"}`},
		{name: "quoted fenced object", raw: "\"```json\\n{\\\"command\\\":\\\"echo hi\\\"}\\n```\"", want: `{"command":"echo hi"}`},
		{name: "invalid", raw: `not-json`, want: `{"raw":"not-json"}`},
		{name: "array", raw: `["echo hi"]`, want: `{"raw":"[\"echo hi\"]"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := NormalizeToolInput(json.RawMessage(test.raw))
			if string(got) != test.want {
				t.Fatalf("NormalizeToolInput(%q) = %s, want %s", test.raw, string(got), test.want)
			}
			if !json.Valid(got) {
				t.Fatalf("NormalizeToolInput(%q) = invalid JSON %s", test.raw, string(got))
			}
		})
	}
}
