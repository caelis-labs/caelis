package codex

import (
	"encoding/json"
	"testing"

	acp "github.com/caelis-labs/acp-go-sdk"
)

func TestHistoryAndLiveProjectionShareStableIdentity(t *testing.T) {
	const threadID = "thread-1"
	history, err := historyItemUpdates(threadID, json.RawMessage(`{"id":"message-1","type":"agentMessage","text":"complete"}`))
	if err != nil {
		t.Fatal(err)
	}
	live, _, err := liveItemCompleted(threadID, json.RawMessage(`{"id":"message-1","type":"agentMessage","text":"complete"}`), false)
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
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(inProgress) != 1 || inProgress[0].ToolCall == nil ||
		inProgress[0].ToolCall.Status != acp.ToolCallStatusInProgress {
		t.Fatalf("in-progress projection = %#v", inProgress)
	}

	declined, err := historyItemUpdates("thread-1", json.RawMessage(`{
		"id":"command-2","type":"commandExecution","command":"rm file","cwd":"/workspace","status":"declined"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(declined) != 2 || declined[1].ToolCallUpdate == nil || declined[1].ToolCallUpdate.Status == nil ||
		*declined[1].ToolCallUpdate.Status != acp.ToolCallStatusFailed {
		t.Fatalf("declined projection = %#v", declined)
	}
}

func TestHistoryProjectionFailsClosedOnUnknownItem(t *testing.T) {
	if _, err := historyItemUpdates("thread-1", json.RawMessage(`{"id":"future-1","type":"futureItem"}`)); err == nil {
		t.Fatal("historyItemUpdates() accepted an unknown item")
	}
}
