package gatewayapp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/testenv"
)

func TestSessionWorkbenchHTTPBackgroundResultAndReadPosition(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	provider := testenv.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "fail this turn") {
			http.Error(w, `{"error":{"message":"fixture rejected"}}`, http.StatusBadRequest)
			return
		}
		if strings.Contains(string(raw), "hold first result") {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"result\",\"object\":\"chat.completion.chunk\",\"model\":\"workbench\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"A visible result\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"result\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	workspaceA, workspaceB := t.TempDir(), t.TempDir()
	cfg := Config{
		AppName: "caelis", UserID: "local-user", StoreDir: t.TempDir(),
		WorkspaceKey: "a", WorkspaceCWD: workspaceA,
		Model: ModelConfig{
			Provider: "custom-chat", Model: "workbench", BaseURL: provider.URL, Token: "test-token",
			ContextWindowTokens: 128000, MaxOutputTok: 1024, HTTPClient: provider.Client(),
		},
	}
	stack, err := newGatewayAppTestStack(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	api := newWorkspaceRuntimeHTTPClient(t, stack, "local-user")
	workbench := api.(appserver.SessionWorkbenchClient)
	a := createWorkspaceRuntimeTestSession(t, api, "create-a", "workbench-a", "a", workspaceA)
	prompt := func(id, operation, input string) {
		t.Helper()
		result, err := api.Prompt(ctx, appserver.PromptRequest{
			WriteBase: appserver.WriteBase{SessionID: id, OperationID: operation}, Input: input,
		})
		if err != nil || result.Target.TurnID == "" {
			t.Fatalf("prompt = %+v, %v", result, err)
		}
	}
	prompt(a, "prompt-a", "hold first result")
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	b := createWorkspaceRuntimeTestSession(t, api, "create-b", "workbench-b", "b", workspaceB)
	page, err := workbench.ListSessionWorkbench(ctx, appserver.SessionWorkbenchRequest{Limit: 1})
	if err != nil || len(page.Sessions) != 1 || page.Sessions[0].SessionID != a || !page.Sessions[0].Active || page.NextCursor == "" {
		t.Fatalf("priority page = %+v, %v", page, err)
	}
	page, err = workbench.ListSessionWorkbench(ctx, appserver.SessionWorkbenchRequest{Limit: 1, Cursor: page.NextCursor})
	if err != nil || len(page.Sessions) != 1 || page.Sessions[0].SessionID != b || page.Sessions[0].WorkspaceKey != "b" {
		t.Fatalf("recent page = %+v, %v", page, err)
	}
	other, err := api.Reconnect(ctx, appserver.ReconnectRequest{SessionID: b})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Subscription.Close()

	close(release)
	waitWorkbenchRow(t, ctx, workbench, a, func(row appserver.SessionWorkbenchRow) bool {
		return row.Unread && !row.Active && !row.Failed
	})

	// The first result was produced without any client ever observing Session A.
	// Reopening the Host preserves both the result and its unread position.
	if err := other.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	stack, err = newGatewayAppTestStack(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	api = newWorkspaceRuntimeHTTPClient(t, stack, "local-user")
	workbench = api.(appserver.SessionWorkbenchClient)
	foreign := newWorkspaceRuntimeHTTPClient(t, stack, "another-user").(appserver.SessionWorkbenchClient)
	page, err = foreign.ListSessionWorkbench(ctx, appserver.SessionWorkbenchRequest{Limit: 10, RefreshIDs: []string{a}})
	if err != nil || len(page.Sessions) != 0 || len(page.Refreshed) != 0 {
		t.Fatalf("unauthorized directory page = %+v, %v", page, err)
	}

	first := workbenchResultSequence(t, ctx, api, a)
	receipt := appserver.SessionReadReceipt{ThroughSeq: first}
	if err := foreign.AcknowledgeSessionRead(ctx, a, receipt); errorcode.CodeOf(err) != errorcode.PermissionDenied {
		t.Fatalf("another principal acknowledged the result: %v", err)
	}
	if err := workbench.AcknowledgeSessionRead(ctx, a, receipt); err != nil {
		t.Fatal(err)
	}
	secondReader := newWorkspaceRuntimeHTTPClient(t, stack, "local-user").(appserver.SessionWorkbenchClient)
	waitWorkbenchRow(t, ctx, secondReader, a, func(row appserver.SessionWorkbenchRow) bool {
		return !row.Unread && !row.Active
	})

	// A stale acknowledgment must not clear a later reply.
	prompt(a, "prompt-second", "hold first result")
	waitWorkbenchRow(t, ctx, workbench, a, func(row appserver.SessionWorkbenchRow) bool { return row.Unread && !row.Active })
	if err := workbench.AcknowledgeSessionRead(ctx, a, receipt); err != nil {
		t.Fatal(err)
	}
	waitWorkbenchRow(t, ctx, workbench, a, func(row appserver.SessionWorkbenchRow) bool { return row.Unread && !row.Active })

	prompt(b, "prompt-failed", "fail this turn")
	waitWorkbenchRow(t, ctx, workbench, b, func(row appserver.SessionWorkbenchRow) bool { return row.Failed && !row.Active })
}

func waitWorkbenchRow(t *testing.T, ctx context.Context, client appserver.SessionWorkbenchClient, id string, matches func(appserver.SessionWorkbenchRow) bool) {
	t.Helper()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		page, err := client.ListSessionWorkbench(ctx, appserver.SessionWorkbenchRequest{Limit: 10, RefreshIDs: []string{id}})
		if err != nil {
			t.Fatalf("list workbench: %v", err)
		}
		if len(page.Refreshed) == 1 && page.Refreshed[0].SessionID == id && matches(page.Refreshed[0]) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("workbench row %s did not settle: %+v", id, page.Refreshed)
		case <-tick.C:
		}
	}
}

func workbenchResultSequence(t *testing.T, ctx context.Context, client appserver.SessionClient, id string) uint64 {
	t.Helper()
	result, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: id, HistoryTurns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	var seq uint64
	for delivery := range result.Subscription.Deliveries() {
		for _, env := range delivery.Events {
			seq = max(seq, eventstream.SessionResultSequence(env))
		}
		if delivery.Kind == appserver.FeedDeliverySync {
			if seq == 0 {
				t.Fatal("replay contained no durable visible result")
			}
			return seq
		}
	}
	t.Fatalf("replay ended before sync: %v", result.Subscription.Err())
	return 0
}
