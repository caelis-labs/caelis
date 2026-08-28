package codex

import (
	"encoding/json"
	"strings"
	"testing"

	acp "github.com/caelis-labs/acp-go-sdk"
)

func TestCommandExecutionProjectionMatchesCodexACPShape(t *testing.T) {
	t.Parallel()

	item := threadItem{
		ID: "command-1", Type: "commandExecution",
		Command: `/bin/zsh -lc "printf 'ACP_TOOL_RESULT_42\n'"`, CWD: "/workspace",
		Status: "completed", AggregatedOutput: "ACP_TOOL_RESULT_42\n", ExitCode: acp.Ptr(0),
	}
	start := toolStart("thread-1", item)
	if start.ToolCall == nil || start.ToolCall.Title != `printf 'ACP_TOOL_RESULT_42\n'` ||
		start.ToolCall.Kind != acp.ToolKindExecute || len(start.ToolCall.Content) != 1 ||
		start.ToolCall.Content[0].Terminal == nil {
		t.Fatalf("command start = %#v", start)
	}
	terminalID := string(start.ToolCall.Content[0].Terminal.TerminalId)
	if terminalID == "" || !strings.Contains(string(start.ToolCall.Meta[terminalInfoMetaKey]), terminalID) {
		t.Fatalf("command terminal metadata = %#v", start.ToolCall.Meta)
	}

	complete := toolComplete("thread-1", item, true, false, terminalOutputLegacy)
	if complete.ToolCallUpdate == nil || complete.ToolCallUpdate.Status == nil ||
		*complete.ToolCallUpdate.Status != acp.ToolCallStatusCompleted {
		t.Fatalf("command completion = %#v", complete)
	}
	rawOutput, ok := complete.ToolCallUpdate.RawOutput.(map[string]any)
	if !ok || rawOutput["formatted_output"] != "ACP_TOOL_RESULT_42\n" {
		t.Fatalf("command rawOutput = %#v", complete.ToolCallUpdate.RawOutput)
	}
	if !strings.Contains(string(complete.ToolCallUpdate.Meta[terminalOutputDeltaMetaKey]), "ACP_TOOL_RESULT_42") ||
		!strings.Contains(string(complete.ToolCallUpdate.Meta[terminalExitMetaKey]), terminalID) {
		t.Fatalf("command completion metadata = %#v", complete.ToolCallUpdate.Meta)
	}

	streamed := toolComplete("thread-1", item, true, true, terminalOutputLegacy)
	if _, duplicated := streamed.ToolCallUpdate.Meta[terminalOutputDeltaMetaKey]; duplicated {
		t.Fatalf("streamed completion repeated terminal output: %#v", streamed.ToolCallUpdate.Meta)
	}

	item.Status = "failed"
	item.Error = map[string]any{"message": "process failed"}
	failed := toolComplete("thread-1", item, true, false, terminalOutputLegacy)
	if failed.ToolCallUpdate.Status == nil || *failed.ToolCallUpdate.Status != acp.ToolCallStatusFailed {
		t.Fatalf("failed command status = %#v", failed.ToolCallUpdate.Status)
	}
	if output, ok := failed.ToolCallUpdate.RawOutput.(map[string]any); !ok ||
		output["formatted_output"] != "ACP_TOOL_RESULT_42\n" {
		t.Fatalf("failed command lost structured output: %#v", failed.ToolCallUpdate.RawOutput)
	}
}

func TestCommandActionProjectionAvoidsTerminalPresentationForReads(t *testing.T) {
	t.Parallel()

	start := toolStart("thread-1", threadItem{
		ID: "command-1", Type: "commandExecution", CWD: "/workspace",
		CommandActions: []commandAction{{Type: "read", Path: "/workspace/main.go"}},
	})
	if start.ToolCall == nil || start.ToolCall.Kind != acp.ToolKindRead ||
		start.ToolCall.Title != "Read file '/workspace/main.go'" || len(start.ToolCall.Content) != 0 ||
		len(start.ToolCall.Meta) != 0 {
		t.Fatalf("read command action = %#v", start)
	}
}

func TestCollaborationWaitUsesStandardTaskWaitPresentationInput(t *testing.T) {
	t.Parallel()

	start := toolStart("thread-1", threadItem{
		ID: "wait-1", Type: "collabAgentToolCall", Tool: "wait",
		SenderThreadID: "parent-1", ReceiverThreadIDs: []string{"child-1"},
	})
	if start.ToolCall == nil || start.ToolCall.Kind != acp.ToolKindOther || start.ToolCall.Title != "wait" {
		t.Fatalf("collaboration wait = %#v", start)
	}
	input, ok := start.ToolCall.RawInput.(map[string]any)
	if !ok || input["action"] != "wait" || input["target_kind"] != "subagent" {
		t.Fatalf("collaboration wait input = %#v", start.ToolCall.RawInput)
	}
}

func TestWaitItemKindsRemainDistinct(t *testing.T) {
	t.Parallel()

	shell := toolStart("thread-1", threadItem{
		ID: "shell-wait-1", Type: "commandExecution", Command: "wait", CWD: "/workspace",
	})
	if shell.ToolCall == nil || shell.ToolCall.Kind != acp.ToolKindExecute ||
		shell.ToolCall.Title != "wait" || len(shell.ToolCall.Content) != 1 ||
		shell.ToolCall.Content[0].Terminal == nil {
		t.Fatalf("shell wait = %#v, want execute terminal presentation", shell)
	}
	if input, ok := shell.ToolCall.RawInput.(map[string]any); !ok ||
		input["command"] != "wait" || input["target_kind"] != nil {
		t.Fatalf("shell wait input = %#v, want command input without subagent target", shell.ToolCall.RawInput)
	}

	sleep := toolStart("thread-1", threadItem{
		ID: "sleep-1", Type: "sleep", Duration: 250,
	})
	if sleep.ToolCall == nil || sleep.ToolCall.Kind != acp.ToolKindOther || sleep.ToolCall.Title != "Wait" {
		t.Fatalf("sleep = %#v, want generic wait presentation", sleep)
	}
	if input, ok := sleep.ToolCall.RawInput.(map[string]any); !ok ||
		input["durationMs"] != int64(250) || input["target_kind"] != nil {
		t.Fatalf("sleep input = %#v, want duration without subagent target", sleep.ToolCall.RawInput)
	}
}

func TestReasoningProjectionPrefersSummaryAndSupportsStringContent(t *testing.T) {
	t.Parallel()

	if got := reasoningText(threadItem{
		Summary: []string{"summary one", "summary two"},
		Content: []json.RawMessage{json.RawMessage(`"private raw reasoning"`)},
	}); got != "summary one\n\nsummary two" {
		t.Fatalf("reasoning summary = %q", got)
	}
	if got := reasoningText(threadItem{
		Content: []json.RawMessage{json.RawMessage(`"fallback reasoning"`)},
	}); got != "fallback reasoning" {
		t.Fatalf("reasoning content fallback = %q", got)
	}
}

func TestHistoryAndLiveProjectionShareStableIdentity(t *testing.T) {
	const threadID = "thread-1"
	history, err := historyItemUpdates(threadID, json.RawMessage(`{"id":"message-1","type":"agentMessage","text":"complete"}`), terminalOutputLegacy)
	if err != nil {
		t.Fatal(err)
	}
	live, _, err := liveItemCompleted(threadID, json.RawMessage(`{"id":"message-1","type":"agentMessage","text":"complete"}`), false, false, false, terminalOutputLegacy)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || len(live) != 1 || history[0].AgentMessageChunk == nil || live[0].AgentMessageChunk == nil {
		t.Fatalf("history/live updates = %#v / %#v", history, live)
	}
	if history[0].AgentMessageChunk.MessageId == nil || live[0].AgentMessageChunk.MessageId == nil ||
		*history[0].AgentMessageChunk.MessageId != *live[0].AgentMessageChunk.MessageId {
		t.Fatalf("history/live message ids = %v / %v", history[0].AgentMessageChunk.MessageId, live[0].AgentMessageChunk.MessageId)
	}
}

func TestHistoryToolProjectionPreservesInProgressAndDeclinedStatus(t *testing.T) {
	inProgress, err := historyItemUpdates("thread-1", json.RawMessage(`{
		"id":"command-1","type":"commandExecution","command":"go test ./...","cwd":"/workspace","status":"inProgress"
	}`), terminalOutputLegacy)
	if err != nil {
		t.Fatal(err)
	}
	if len(inProgress) != 1 || inProgress[0].ToolCall == nil ||
		inProgress[0].ToolCall.Status != acp.ToolCallStatusInProgress {
		t.Fatalf("in-progress projection = %#v", inProgress)
	}

	declined, err := historyItemUpdates("thread-1", json.RawMessage(`{
		"id":"command-2","type":"commandExecution","command":"rm file","cwd":"/workspace","status":"declined"
	}`), terminalOutputLegacy)
	if err != nil {
		t.Fatal(err)
	}
	if len(declined) != 2 || declined[1].ToolCallUpdate == nil || declined[1].ToolCallUpdate.Status == nil ||
		*declined[1].ToolCallUpdate.Status != acp.ToolCallStatusFailed {
		t.Fatalf("declined projection = %#v", declined)
	}
}

func TestHistoryProjectionFailsClosedOnUnknownItem(t *testing.T) {
	if _, err := historyItemUpdates("thread-1", json.RawMessage(`{"id":"future-1","type":"futureItem"}`), terminalOutputLegacy); err == nil {
		t.Fatal("historyItemUpdates() accepted an unknown item")
	}
}
