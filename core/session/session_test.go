package session

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/core/model"
)

func TestCloneEventPreservesCanonicalMessageIndependently(t *testing.T) {
	event := Event{
		Type: EventAssistant,
		Message: &model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("hello")},
			Meta:  map[string]any{"provider": "test"},
		},
		Tool: &ToolEvent{
			ID:     "call-1",
			Name:   "READ",
			Input:  map[string]any{"path": "a.txt"},
			Output: map[string]any{"text": "hello"},
		},
		Meta: map[string]any{"surface": "test"},
	}

	cloned := CloneEvent(event)
	event.Message.Parts[0].Text.Text = "mutated"
	event.Message.Meta["provider"] = "mutated"
	event.Tool.Input["path"] = "b.txt"
	event.Meta["surface"] = "mutated"

	if got := EventText(cloned); got != "hello" {
		t.Fatalf("EventText(cloned) = %q, want hello", got)
	}
	if got := cloned.Message.Meta["provider"]; got != "test" {
		t.Fatalf("cloned message meta = %v, want test", got)
	}
	if got := cloned.Tool.Input["path"]; got != "a.txt" {
		t.Fatalf("cloned tool input path = %v, want a.txt", got)
	}
	if got := cloned.Meta["surface"]; got != "test" {
		t.Fatalf("cloned event meta = %v, want test", got)
	}
}

func TestSessionMatchesListQuerySearchesMetadata(t *testing.T) {
	active := Session{
		Ref: Ref{
			AppName:      "caelis",
			UserID:       "tester",
			SessionID:    "sess-1",
			WorkspaceKey: "repo",
		},
		Workspace: Workspace{Key: "repo", CWD: "/tmp/repo"},
		Title:     "ordinary notes",
		Meta: map[string]any{
			"project": "Phoenix migration",
			"labels":  []any{"roadmap", "canonical-store"},
		},
	}
	if !SessionMatchesListQuery(active, ListQuery{
		Ref:    Ref{AppName: "caelis", UserID: "tester", WorkspaceKey: "repo"},
		Search: "phoenix",
	}) {
		t.Fatal("metadata search did not match session")
	}
	if SessionMatchesListQuery(active, ListQuery{
		Ref:    Ref{AppName: "caelis", UserID: "tester", WorkspaceKey: "repo"},
		Search: "missing",
	}) {
		t.Fatal("unrelated search matched session metadata")
	}
}

func TestRuntimeControllerMetaRoundTripsUnderCanonicalNamespace(t *testing.T) {
	meta := WithRuntimeControllerMeta(map[string]any{"surface": "test"}, map[string]any{
		"run_id": "run-1",
		"phase":  "remote_session",
	})
	controller := RuntimeControllerMeta(meta)
	if controller["schema"] != RuntimeControllerMetaName || controller["schema_version"] != RuntimeControllerMetaVersion {
		t.Fatalf("controller meta = %#v, want schema marker", controller)
	}
	if controller["run_id"] != "run-1" || controller["phase"] != "remote_session" {
		t.Fatalf("controller meta = %#v, want lifecycle fields", controller)
	}
	if meta["surface"] != "test" {
		t.Fatalf("meta = %#v, want existing metadata preserved", meta)
	}
}
