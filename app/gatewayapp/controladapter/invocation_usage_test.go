package controladapter

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

func TestUsageReceiptsKeepEqualTokensInSeparateAgentLanes(t *testing.T) {
	var events []*session.Event
	for _, category := range []string{"main", "subagent", "auto_review"} {
		receipt := session.NewModelInvocationReceipt(model.Invocation{ID: category, Provider: "p", Model: "m", Outcome: "completed", Usage: model.Usage{PromptTokens: 10, CachedInputTokens: 3, CompletionTokens: 2, TotalTokens: 12}}, "chat")
		receipt.SessionID = "s"
		receipt.Scope = &session.EventScope{TurnID: category, Executor: session.ActorRef{Kind: session.ActorKindController, ID: category}}
		receipt.Meta["usage_category"] = category
		response := session.CloneEvent(receipt)
		response.ID = "response" + category
		response.Type = session.EventTypeToolCall
		response.Visibility = session.VisibilityCanonical
		response.IdempotencyKey = ""
		response.Lifecycle = nil
		response.MessageID = category
		sdk := response.Meta["caelis"].(map[string]any)["sdk"].(map[string]any)
		sdk["usage_receipt_id"] = category
		events = append(events, receipt, response)
	}
	got := sessionTokenUsageBreakdownFromEvents(events, "main")
	if got.Total.TotalTokens != 36 || got.Main.TotalTokens != 12 || got.Subagents.TotalTokens != 12 || got.AutoReview.TotalTokens != 12 || got.Total.CachedInputTokens != 9 {
		t.Fatalf("breakdown=%#v", got)
	}
}

func TestSessionUsageReaderIncludesDurableReceipts(t *testing.T) {
	store := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
	active, err := store.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "user", Workspace: session.WorkspaceRef{Key: "w", CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	receipt := session.NewModelInvocationReceipt(model.Invocation{ID: "attempt", Provider: "p", Model: "m", Outcome: "failed", Usage: model.Usage{TotalTokens: 12}}, "chat")
	if _, err := store.AppendEvent(context.Background(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: receipt}); err != nil {
		t.Fatal(err)
	}
	events, err := sessionUsageEvents(context.Background(), store, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionTokenUsageBreakdownFromEvents(events, "main"); got.Total.TotalTokens != 12 {
		t.Fatalf("journal usage missing: %#v", got)
	}
}
