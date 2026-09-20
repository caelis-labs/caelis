package gatewayapp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestGuardianScreenContextExcludesAllToolHistory(t *testing.T) {
	req := guardianWindowRequest(t, "screen")
	req.Approval.ToolName = "RunCommand"
	req.Approval.RawInput = map[string]any{"command": "go test ./parser", "sandbox_permissions": "require_escalated"}
	req.Approval.Reason = "Host execution requested"
	req.Approval.Justification = "The test needs a restricted local socket."
	events := []*session.Event{guardianProjectEvent(guardianSource(1, session.EventTypeUser, "Fix the parser. Do not push or delete files."))}
	first, err := guardianJudgmentRequest(req, events)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(first)
	// Even an exact matching call, a running command and its later Task failure,
	// or a new risk observation must not become classifier input.
	for i, name := range []string{"RunCommand", "RunCommand", "Task", "Read"} {
		event := guardianSource(uint64(i+2), session.EventTypeToolResult, "")
		event.Tool.Name = name
		event.Tool.Input = req.Approval.RawInput
		event.Tool.Output = map[string]any{"state": "running", "stderr": "sandbox: operation not permitted", "text": strings.Repeat("NEW_RISK", 5000)}
		if name == "Task" {
			event.Tool.Output["state"] = "completed"
			event.Tool.Output["exit_code"] = 1
		}
		events = append(events, guardianProjectEvent(event))
	}
	call := guardianSource(6, session.EventTypeToolCall, "go test ./parser")
	events = append(events, guardianProjectEvent(call))
	after, err := guardianJudgmentRequest(req, events)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(after)
	if string(raw) != string(before) {
		t.Fatal("tool history changed screening input")
	}
	state := after.State.(map[string]any)
	if len(state) != 2 || state["user_messages"] == nil || state["action"] == nil {
		t.Fatalf("unexpected classifier state: %v", state)
	}
	action := state["action"].(map[string]any)
	if !reflect.DeepEqual(action["arguments"], req.Approval.RawInput) || action["reason"] != req.Approval.Reason || action["justification"] != req.Approval.Justification {
		t.Fatal("current approval fields changed")
	}
	events = append(events, guardianProjectEvent(guardianSource(7, session.EventTypeUser, "Correction: do not run tests now.")))
	after, err = guardianJudgmentRequest(req, events)
	if err != nil {
		t.Fatal(err)
	}
	users := after.State.(map[string]any)["user_messages"].([]string)
	if len(users) != 2 || !strings.Contains(users[0], "Do not push or delete files") || !strings.Contains(users[1], "Correction: do not run tests now") {
		t.Fatalf("chronological user constraints lost: %v", users)
	}
}

func TestGuardianScreenContextPreservesOnlyOriginalOptions(t *testing.T) {
	req := guardianWindowRequest(t, "screen")
	req.Approval.Options = []approval.Option{{ID: "opaque-deny-name", Name: "Reject", Kind: "allow_once"}, {ID: "opaque-allow-name", Name: "Approve", Kind: "reject_once"}}
	request, err := guardianJudgmentRequest(req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Questions) != 3 {
		t.Fatal("classifier must ask the decision and two evidence questions")
	}
	for _, id := range []string{"material_unknown", "visible_violation"} {
		question := request.Questions[id]
		if question.Type != judgment.Noul || question.Instructions == nil {
			t.Fatalf("%s must be a Noul question with instructions", id)
		}
	}
	options := request.Questions["decision"].Criteria.(map[string]approval.Option)
	if len(options) != len(req.Approval.Options) {
		t.Fatal("synthetic option added")
	}
	for _, option := range req.Approval.Options {
		if options[option.ID] != option {
			t.Fatalf("option changed: %v", option)
		}
	}
}

func TestGuardianScreenContextNeverTruncatesUserOrAction(t *testing.T) {
	req := guardianWindowRequest(t, "screen")
	events := []*session.Event{guardianProjectEvent(guardianSource(1, session.EventTypeUser, strings.Repeat("x", 24000)+"Do not push."))}
	if _, err := guardianJudgmentRequest(req, events); err == nil {
		t.Fatal("oversized user source must defer")
	}
	req.Approval.RawInput = map[string]any{"command": strings.Repeat("x", 24000) + "; rm -rf important"}
	if _, err := guardianJudgmentRequest(req, nil); err == nil {
		t.Fatal("oversized action must defer")
	}
}
