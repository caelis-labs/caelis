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
	"github.com/caelis-labs/caelis/control/collaboration"
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
	priorityCursor := page.NextCursor
	page, err = workbench.ListSessionWorkbench(ctx, appserver.SessionWorkbenchRequest{Limit: 1, Cursor: priorityCursor})
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

	// The first result was produced without any client observing Session A.
	// Its exact history carries streamed text plus the terminal read position.
	exact, terminal := workbenchReplayPositions(t, ctx, api, a)
	if terminal == 0 {
		t.Fatal("exact replay omitted the terminal result position")
	}
	if exact != 0 && exact != terminal {
		t.Fatalf("exact replay positions disagree: canonical=%d terminal=%d", exact, terminal)
	}
	// Reopening the Host preserves the result and its unread position.
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

	first, _ := workbenchReplayPositions(t, ctx, api, a)
	if first == 0 || first != terminal {
		t.Fatalf("canonical replay result position = %d, want %d", first, terminal)
	}
	receipt := appserver.SessionReadReceipt{ThroughSeq: first}
	if err := foreign.AcknowledgeSessionRead(ctx, a, receipt); errorcode.CodeOf(err) != errorcode.PermissionDenied {
		t.Fatalf("another principal acknowledged the result: %v", err)
	}
	if err := workbench.AcknowledgeSessionRead(ctx, a, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := workbench.ListSessionWorkbench(ctx, appserver.SessionWorkbenchRequest{Limit: 1, Cursor: priorityCursor}); !errorcode.Is(err, errorcode.Conflict) {
		t.Fatalf("HTTP continuation after priority membership changed = %v, want conflict", err)
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

func TestSessionWorkbenchHTTPTracksMailboxWake(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	provider := newHostedChildInputTestProvider(t, false)
	host := newHostedChildInputTestStack(t, provider)
	parent, _, _ := newHostedChildInputTestTopology(t, host, "workbench-mailbox")
	api := newWorkspaceRuntimeHTTPClient(t, host, parent.UserID)
	msg, err := host.CollaborationService().Send(ctx, collaboration.Identity{Session: parent.SessionID, Member: "orbit"}, "parent", "workbench mailbox result", "")
	if err != nil {
		t.Fatal(err)
	}
	waitHostedChildInputEvent(t, host, parent.SessionRef, msg.Text)
	waitWorkbenchRow(t, ctx, api.(appserver.SessionWorkbenchClient), parent.SessionID, func(row appserver.SessionWorkbenchRow) bool {
		return row.Unread && !row.Active && !row.Failed
	})
}

func TestSessionWorkbenchHTTPRecoversAfterIndexWriteFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	provider := newHostedChildInputTestProvider(t, false)
	host := newHostedChildInputTestStack(t, provider)
	active, err := startGatewayAppTestSession(ctx, host, "workbench-recover")
	if err != nil {
		t.Fatal(err)
	}
	api := newWorkspaceRuntimeHTTPClient(t, host, active.UserID)
	if _, err := host.workbenchDB.ExecContext(ctx, `
CREATE TRIGGER fail_workbench_write BEFORE INSERT ON session_workbench_results
BEGIN SELECT RAISE(FAIL, 'injected workbench index failure'); END
`); err != nil {
		t.Fatal(err)
	}
	prompt := func(client appserver.SessionClient, operation, input string) {
		t.Helper()
		if _, err := client.Prompt(ctx, appserver.PromptRequest{
			WriteBase: appserver.WriteBase{SessionID: active.SessionID, OperationID: operation}, Input: input,
		}); err != nil {
			t.Fatal(err)
		}
	}
	prompt(api, "before-workbench-recovery", "include this input in the resumed model context")
	waitHostedChildParentIdle(t, host, active.SessionID)
	page, err := api.(appserver.SessionWorkbenchClient).ListSessionWorkbench(ctx, appserver.SessionWorkbenchRequest{RefreshIDs: []string{active.SessionID}})
	if err != nil || len(page.Refreshed) != 1 || page.Refreshed[0].Unread {
		t.Fatalf("injected failure left an indexed result: %+v, %v", page.Refreshed, err)
	}
	storeDir, workspace := host.composition.authorities.storeDir, host.composition.workspace
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	db, _, err := openControlStoreDatabase(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	_, dropErr := db.ExecContext(ctx, `DROP TRIGGER fail_workbench_write`)
	if err := db.Close(); err != nil || dropErr != nil {
		t.Fatalf("clear database fault: drop=%v close=%v", dropErr, err)
	}
	reopened, err := newGatewayAppTestStack(t, Config{
		StoreDir: storeDir, WorkspaceKey: workspace.Key, WorkspaceCWD: workspace.CWD,
		ResolveProviderHTTPClient: func(context.Context, ModelConfig) (*http.Client, error) { return provider.Client(), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	api = newWorkspaceRuntimeHTTPClient(t, reopened, active.UserID)
	workbench := api.(appserver.SessionWorkbenchClient)
	page, err = workbench.ListSessionWorkbench(ctx, appserver.SessionWorkbenchRequest{RefreshIDs: []string{active.SessionID}})
	if err != nil || len(page.Refreshed) != 1 || !page.Refreshed[0].Unread || page.Refreshed[0].Failed {
		t.Fatalf("workbench did not recover the unread result: %+v, %v", page.Refreshed, err)
	}
	seq, _ := workbenchReplayPositions(t, ctx, api, active.SessionID)
	if seq == 0 {
		t.Fatal("canonical replay omitted the recovered result")
	}
	if err := workbench.AcknowledgeSessionRead(ctx, active.SessionID, appserver.SessionReadReceipt{ThroughSeq: seq}); err != nil {
		t.Fatal(err)
	}
	waitWorkbenchRow(t, ctx, workbench, active.SessionID, func(row appserver.SessionWorkbenchRow) bool { return !row.Unread })

	prompt(api, "after-workbench-recovery", "continue after Host restart")
	waitWorkbenchRow(t, ctx, workbench, active.SessionID, func(row appserver.SessionWorkbenchRow) bool {
		return row.Unread && !row.Active && !row.Failed
	})
	if messages := string(provider.LastMessages()); !strings.Contains(messages, "include this input in the resumed model context") || !strings.Contains(messages, "reply-1") {
		t.Fatalf("resumed model context lost the prior dialogue: %s", messages)
	}
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

func workbenchReplayPositions(t *testing.T, ctx context.Context, client appserver.SessionClient, id string) (durable, terminal uint64) {
	t.Helper()
	result, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: id, HistoryTurns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	for delivery := range result.Subscription.Deliveries() {
		for _, env := range delivery.Events {
			durable = max(durable, eventstream.SessionResultSequence(env))
			terminal = max(terminal, eventstream.TerminalSessionResultSequence(env))
		}
		if delivery.Kind == appserver.FeedDeliverySync {
			return durable, terminal
		}
	}
	t.Fatalf("replay ended before sync: %v", result.Subscription.Err())
	return 0, 0
}
