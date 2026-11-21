package projection

import (
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
)

func TestSpawnFullDisplayArgsUsesAgentAndPrompt(t *testing.T) {
	raw := map[string]any{
		"agent":  "codex",
		"prompt": "inspect the transcript",
	}
	if got := SpawnFullDisplayArgs(raw); got != "codex: inspect the transcript" {
		t.Fatalf("SpawnFullDisplayArgs() = %q", got)
	}
}

func TestSpawnFullDisplayArgsUsesHandleWithAgentAnnotation(t *testing.T) {
	raw := map[string]any{
		"agent":   "self",
		"task_id": "jeff",
		"prompt":  "inspect the transcript",
	}
	if got := SpawnFullDisplayArgs(raw); got != "jeff[self]: inspect the transcript" {
		t.Fatalf("SpawnFullDisplayArgs() = %q", got)
	}
}

func TestSpawnDisplayInputForResultMergesPromptFromLifecycleOutput(t *testing.T) {
	input := map[string]any{"agent": "codex"}
	output := map[string]any{"text": `{"prompt":"inspect repo","task_id":"task-1"} running`}
	got := SpawnDisplayInputForResult(input, output)
	if got["agent"] != "codex" || got["prompt"] != "inspect repo" || got["task_id"] != "task-1" {
		t.Fatalf("SpawnDisplayInputForResult() = %#v", got)
	}
	normalized := normalizeSpawnDisplayRawMap(output)
	if normalized["text"] != "running" {
		t.Fatalf("text remainder = %#v", normalized["text"])
	}
}

func TestStartThreadPermissionPreservesStoredTitleAndBackfillsLegacyTitle(t *testing.T) {
	for _, fixture := range []struct {
		name, title, want string
		context           *session.PermissionContext
	}{
		{name: "stored title", title: "Remote request to start a thread", want: "Remote request to start a thread"},
		{name: "legacy title", want: "StartThread orbit[self]: inspect the transcript"},
		{name: "independent permission", context: &session.PermissionContext{}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			request := &session.Event{
				SessionID: "session-1",
				Type:      session.EventTypeLifecycle,
				Protocol: &session.EventProtocol{
					Method: session.ProtocolMethodRequestPermission,
					Permission: &session.ProtocolApproval{
						Context: fixture.context,
						ToolCall: session.ProtocolToolCall{
							ID: "call-1", Name: spawn.ToolName, Title: fixture.title, Status: "pending",
							RawInput: map[string]any{"text": `{"agent":"self","task_id":"orbit","prompt":"inspect the transcript"} pending`},
						},
						Options: []session.ProtocolApprovalOption{{ID: "allow_once", Name: "Allow once", Kind: "allow_once"}},
					},
				},
			}
			raw, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			var stored session.Event
			if err := json.Unmarshal(raw, &stored); err != nil {
				t.Fatal(err)
			}
			projected, ok, err := projectPermissionRequest(&stored)
			if err != nil || !ok {
				t.Fatalf("projectPermissionRequest() = %v, %t, %v", projected, ok, err)
			}
			if got := stringFromPtr(projected.ToolCall.Title); got != fixture.want {
				t.Fatalf("permission title = %q, want %q", got, fixture.want)
			}
			if projected.ToolCall.ToolCallID != "call-1" || len(projected.Options) != 1 || projected.Options[0].OptionId != "allow_once" {
				t.Fatalf("permission identity or options changed: %#v", projected)
			}
			if stored.Protocol.Permission.ToolCall.Title != fixture.title {
				t.Fatalf("stored request mutated: %#v", stored.Protocol.Permission.ToolCall)
			}
		})
	}
}
