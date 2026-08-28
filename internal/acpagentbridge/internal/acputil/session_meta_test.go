package acputil

import (
	"reflect"
	"testing"
)

func TestSubagentSessionMetaRoundTrip(t *testing.T) {
	t.Parallel()

	meta := NewSubagentSessionMeta(" parent-session ", " task-1 ", " token-1 ")
	wantMeta := map[string]any{
		"caelis": map[string]any{
			"version": 1,
			"runtime": map[string]any{
				"session": map[string]any{
					"kind":              "subagent",
					"parent_session_id": "parent-session",
					"task_id":           "task-1",
					"history_token":     "token-1",
				},
			},
		},
	}
	if !reflect.DeepEqual(meta, wantMeta) {
		t.Fatalf("NewSubagentSessionMeta() = %#v, want %#v", meta, wantMeta)
	}

	got, ok := ParseSubagentSessionMeta(meta)
	if !ok {
		t.Fatal("ParseSubagentSessionMeta() did not recognize managed subagent metadata")
	}
	want := SubagentSessionMetadata{
		ParentSessionID: "parent-session",
		TaskID:          "task-1",
		HistoryToken:    "token-1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseSubagentSessionMeta() = %#v, want %#v", got, want)
	}
}

func TestSubagentSessionMetaOmitsEmptyOptionalValues(t *testing.T) {
	t.Parallel()

	meta := NewSubagentSessionMeta("", " task-1 ", " ")
	sessionMeta := nestedMap(meta, "caelis", "runtime", "session")
	want := map[string]any{"kind": "subagent", "task_id": "task-1"}
	if !reflect.DeepEqual(sessionMeta, want) {
		t.Fatalf("session metadata = %#v, want %#v", sessionMeta, want)
	}
}

func TestParseSubagentSessionMetaRejectsOtherClassification(t *testing.T) {
	t.Parallel()

	for name, meta := range map[string]map[string]any{
		"missing": nil,
		"other": {
			"caelis": map[string]any{
				"runtime": map[string]any{
					"session": map[string]any{"kind": "guardian"},
				},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got, ok := ParseSubagentSessionMeta(meta); ok || got != (SubagentSessionMetadata{}) {
				t.Fatalf("ParseSubagentSessionMeta() = %#v, %v; want zero, false", got, ok)
			}
		})
	}
}
