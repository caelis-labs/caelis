package gatewayapp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func screenPair(seq uint64, command, output string) []*session.Event {
	call := guardianSource(seq, session.EventTypeToolCall, command)
	result := guardianSource(seq+1, session.EventTypeToolResult, "")
	result.Tool.ID = call.Tool.ID
	result.Tool.Output = map[string]any{"stderr": output, "exit_code": 1}
	return []*session.Event{guardianProjectEvent(call), guardianProjectEvent(result)}
}

func TestGuardianScreenContextKeepsConstraintsAndMatchingObservation(t *testing.T) {
	req := guardianWindowRequest(t, "screen")
	req.Approval.ToolName = "RunCommand"
	req.Approval.RawInput = map[string]any{"command": "go test ./parser", "sandbox_permissions": "require_escalated"}
	events := []*session.Event{guardianProjectEvent(guardianSource(1, session.EventTypeUser, "Fix the parser. Do not push or delete files."))}
	events = append(events, screenPair(2, "go test ./parser", "operation not permitted")...)
	first, err := guardianJudgmentRequest(req, events)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(first)
	for i := range 500 {
		events = append(events, screenPair(uint64(4+i*2), fmt.Sprintf("rg unrelated-%d .", i), strings.Repeat("irrelevant-output", 300))...)
	}
	after, err := guardianJudgmentRequest(req, events)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(after)
	if string(raw) != string(before) {
		t.Fatal("unrelated history changed screening input")
	}
	if !strings.Contains(string(raw), "Do not push or delete files") || !strings.Contains(string(raw), "operation not permitted") || strings.Contains(string(raw), "irrelevant-output") {
		t.Fatal("screen lost user constraints or matching diagnostics")
	}
	// A correction after omitted observations keeps its original sequence number.
	events = append(events, guardianProjectEvent(guardianSource(1005, session.EventTypeUser, "Correction: do not run tests now.")))
	after, err = guardianJudgmentRequest(req, events)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(after)
	if !strings.Contains(string(raw), "Correction: do not run tests now") || !strings.Contains(string(raw), `"seq":1005`) {
		t.Fatal("later correction/sequence lost")
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

func TestGuardianScreenContextUsesCompletedPairsWithoutInventingMatches(t *testing.T) {
	req := guardianWindowRequest(t, "screen")
	events := screenPair(1, "rg prior .", "old diagnostic")
	events = append(events, screenPair(3, "rg latest .", "latest diagnostic")...)
	events = append(events, guardianProjectEvent(guardianSource(5, session.EventTypeToolCall, "pending command")))
	selected := guardianScreenEvidence(req, events)
	if len(selected) != 2 || !selected[2] || !selected[3] {
		t.Fatalf("selected=%v", selected)
	}
	if guardianScreenActionKey("RunCommand", map[string]any{"command": "test", "cwd": "/a"}) == guardianScreenActionKey("RunCommand", map[string]any{"command": "test", "cwd": "/b"}) {
		t.Fatal("cwd collapsed")
	}
}
