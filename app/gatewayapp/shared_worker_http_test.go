package gatewayapp_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/taskstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
)

type sharedWorkerProvider struct {
	nativeModelScript
	mu      sync.Mutex
	count   int
	blockAt int
	blocked chan struct{}
	release chan struct{}
}

func (p *sharedWorkerProvider) RoundTrip(req *http.Request) (*http.Response, error) {
	p.mu.Lock()
	p.count++
	n := p.count
	p.mu.Unlock()
	blockAt := p.blockAt
	if blockAt == 0 {
		blockAt = 2
	}
	if n == blockAt {
		close(p.blocked)
		select {
		case <-p.release:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	return p.nativeModelScript.RoundTrip(req)
}

func TestApplicationMainHTTPSteeringContinuesCurrentTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	p := &sharedWorkerProvider{blockAt: 1, blocked: make(chan struct{}), release: make(chan struct{})}
	h := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, p)
	defer h.close(t)
	defer cancel()
	status, err := h.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.host.ConnectModel(ctx, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "model", ExpectedRevision: &status.Configuration.Revision}, Config: appserver.ConnectConfig{Provider: "openai-compatible", Model: "gpt-4.1", BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC"}}); err != nil {
		t.Fatal(err)
	}
	bot, _ := registerApplicationHTTP(t, ctx, h, "main-steering", filepath.Join(root, "bot.token"))
	sid := createApplicationHTTPSession(t, ctx, bot, "main")
	feed, err := bot.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Subscription.Close()
	started, err := bot.PromptApplication(ctx, appserver.ApplicationPromptRequest{PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "main-start", SessionID: sid}, Input: "MAIN_INPUT_SENTINEL"}, SourceKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.blocked:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	steer := appserver.SteerRequest{WriteBase: appserver.WriteBase{OperationID: "main-steer", SessionID: sid}, Target: started.Target, Input: "MAIN_STEERING_SENTINEL"}
	for range 2 {
		if receipt, err := bot.Steer(ctx, steer); err != nil || receipt.InputStatus != "accepted" {
			t.Fatalf("main steering: %+v %v", receipt, err)
		}
	}
	close(p.release)
	applied := 0
	for done := false; !done; {
		select {
		case delivery, ok := <-feed.Subscription.Deliveries():
			if !ok {
				t.Fatal("main subscription closed")
			}
			for _, e := range delivery.Events {
				if e.InputOperationID == steer.OperationID && e.InputStatus == "applied" {
					applied++
				}
				if eventstream.IsTurnTerminalLifecycle(e) && e.TurnID == started.Target.TurnID {
					if e.Lifecycle.State != "completed" {
						t.Fatalf("main terminal: %+v", e)
					}
					done = true
				}
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	p.nativeModelScript.mu.Lock()
	defer p.nativeModelScript.mu.Unlock()
	if applied != 1 || len(p.seen) != 2 || !strings.Contains(string(p.seen[1]), "MAIN_STEERING_SENTINEL") || !strings.Contains(string(p.seen[1]), "MAIN_INPUT_SENTINEL") {
		t.Fatalf("main continuation: applied=%d model steps=%d", applied, len(p.seen))
	}
}

func TestSharedWorkerHTTPAttachSteerDetachAndReconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace := filepath.Join(root, "work")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "input.txt"), []byte("COMPLETED_TOOL_SENTINEL"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &sharedWorkerProvider{blocked: make(chan struct{}), release: make(chan struct{})}
	p.set(nativeModelTool{"Read", `{"path":"input.txt"}`})
	h := startApplicationHTTPHost(t, filepath.Join(root, "store"), workspace, p)
	defer h.close(t)
	defer cancel()
	status, err := h.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.host.ConnectModel(ctx, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "model", ExpectedRevision: &status.Configuration.Revision}, Config: appserver.ConnectConfig{Provider: "openai-compatible", Model: "gpt-4.1", BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY"}})
	if err != nil {
		t.Fatal(err)
	}
	bot, _ := registerApplicationHTTP(t, ctx, h, "bot", filepath.Join(root, "bot.token"))
	other, _ := registerApplicationHTTP(t, ctx, h, "other", filepath.Join(root, "other.token"))
	req := appserver.CreateWorkerRequest{WriteBase: appserver.WriteBase{OperationID: "create-worker"}, CWD: workspace, Title: "Shared work", Model: "openai-compatible/gpt-4.1"}
	created, err := bot.CreateWorker(ctx, req)
	if err != nil || created.SessionID == "" || created.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("create: %+v %v", created, err)
	}
	sid := created.SessionID
	duplicate, err := bot.CreateWorker(ctx, req)
	if err != nil || duplicate.SessionID != sid {
		t.Fatalf("duplicate create: %+v %v", duplicate, err)
	}
	if _, err := other.InspectSession(ctx, appserver.StateRequest{SessionID: sid}); err == nil {
		t.Fatal("another application acquired worker")
	}
	if _, err := h.host.InspectSession(ctx, appserver.StateRequest{SessionID: sid}); err != nil {
		t.Fatal(err)
	}
	botClients, err := httpclient.AppServerClients(bot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := botClients.Tasks.List(ctx, taskstream.ListRequest{SessionID: sid}); err != nil {
		t.Fatalf("owned task directory: %v", err)
	}
	otherClients, err := httpclient.AppServerClients(other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := otherClients.Tasks.List(ctx, taskstream.ListRequest{SessionID: sid}); err == nil {
		t.Fatal("another application acquired task directory")
	}
	feed, err := bot.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Subscription.Close()
	clients, err := httpclient.AppServerClients(h.host)
	if err != nil {
		t.Fatal(err)
	}
	tui, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{Surface: "cli-tui", WorkspaceDir: workspace, Sessions: clients.Sessions, Participants: clients.Participants, Status: clients.Status, Configuration: clients.Configuration, Agents: clients.Agents, Completion: clients.Completion, Plugins: clients.Plugins})
	if err != nil {
		t.Fatal(err)
	}
	defer tui.Close()
	view, err := tui.ResumeSession(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	started, err := bot.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "start", SessionID: sid}, Input: "Read input.txt once, then finish."})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.blocked:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	steer := appserver.SteerRequest{WriteBase: appserver.WriteBase{OperationID: "bot-steer", SessionID: sid}, Target: started.Target, Input: "BOT_STEERING_SENTINEL"}
	receipt, err := bot.Steer(ctx, steer)
	if err != nil || receipt.InputStatus != "accepted" {
		t.Fatalf("steer: %+v %v", receipt, err)
	}
	repeated, err := bot.Steer(ctx, steer)
	if err != nil || repeated.InputStatus != "accepted" {
		t.Fatalf("repeat: %+v %v", repeated, err)
	}
	steer.Input = "conflicting payload"
	if _, err := bot.Steer(ctx, steer); err == nil {
		t.Fatal("same operation accepted different input")
	}
	// Refresh the selected TUI snapshot to the exact running Turn, then submit
	// through the same adapter used by the terminal, without a second prompt.
	if _, err := tui.ResumeSession(ctx, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := tui.Submit(ctx, controlprompt.Submission{Text: "USER_STEERING_SENTINEL", Mode: controlprompt.SubmissionModeActiveTurn}); err != nil {
		t.Fatal(err)
	}
	_ = view.Reconnect.Close()
	if err := tui.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := bot.InspectSession(ctx, appserver.StateRequest{SessionID: sid})
	if err != nil || !state.Run.Active {
		t.Fatalf("detach stopped Worker: %+v %v", state.Run, err)
	}
	close(p.release)
	var cursor string
	var applied int
	for done := false; !done; {
		select {
		case delivery, ok := <-feed.Subscription.Deliveries():
			if !ok {
				t.Fatalf("Bot subscription closed: %v", feed.Subscription.Err())
			}
			for _, e := range delivery.Events {
				if e.InputOperationID == "bot-steer" && e.InputStatus == "applied" {
					applied++
				}
				if eventstream.IsTurnTerminalLifecycle(e) && e.TurnID == started.Target.TurnID {
					if e.Lifecycle.State != "completed" {
						t.Fatalf("worker terminal: %+v", e)
					}
					done = true
				}
			}
			if delivery.NextCursor != "" {
				cursor = delivery.NextCursor
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if applied != 1 {
		t.Fatalf("applied receipts=%d", applied)
	}
	steer.OperationID, steer.Input = "late-steer", "must not start another Turn"
	if _, err := bot.Steer(ctx, steer); err == nil {
		t.Fatal("completed Turn accepted stale steering")
	}
	history := applicationHTTPHistory(t, ctx, bot, sid)
	inputs, toolCalls := 0, 0
	for _, e := range history {
		if e.InputOperationID == "bot-steer" {
			inputs++
		}
		if eventstream.UpdateType(e.Update) == eventstream.UpdateToolCall {
			toolCalls++
		}
	}
	if inputs != 1 || toolCalls != 1 {
		t.Fatalf("replay inputs=%d tool calls=%d", inputs, toolCalls)
	}
	p.nativeModelScript.mu.Lock()
	seen := append([][]byte(nil), p.seen...)
	p.nativeModelScript.mu.Unlock()
	if len(seen) != 3 || !strings.Contains(string(seen[2]), "BOT_STEERING_SENTINEL") || !strings.Contains(string(seen[2]), "USER_STEERING_SENTINEL") || !strings.Contains(string(seen[2]), "COMPLETED_TOOL_SENTINEL") {
		t.Fatalf("safe-point context lost completed work or inputs: requests=%d", len(seen))
	}
	_ = feed.Subscription.Close()
	missed, err := h.host.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "user-while-disconnected", SessionID: sid}, Input: "A user Turn while Bot is disconnected"})
	if err != nil {
		t.Fatal(err)
	}
	waitApplicationHTTPIdle(t, ctx, h.host, sid)
	resumed, err := bot.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sid, Cursor: cursor})
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Subscription.Close()
	for caughtUp := false; !caughtUp; {
		select {
		case d, ok := <-resumed.Subscription.Deliveries():
			if !ok {
				t.Fatal("reconnect closed before recovering missed Turn")
			}
			for _, e := range d.Events {
				if eventstream.IsTurnTerminalLifecycle(e) && e.TurnID == missed.Target.TurnID {
					caughtUp = true
				}
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	// A later user Turn is delivered on the Bot's same Session subscription.
	next, err := h.host.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "user-next", SessionID: sid}, Input: "A user initiated new Turn"})
	if err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case d, ok := <-resumed.Subscription.Deliveries():
			if !ok {
				t.Fatal(io.EOF)
			}
			for _, e := range d.Events {
				if eventstream.IsTurnTerminalLifecycle(e) && e.TurnID == next.Target.TurnID {
					return
				}
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestSharedWorkerHTTPApprovalCompetition(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	root := t.TempDir()
	p := &nativeModelScript{}
	p.set(nativeModelTool{"RunCommand", `{"command":"printf approval-fixture","sandbox_permissions":"require_escalated","justification":"Synthetic approval race"}`})
	h := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, p)
	defer h.close(t)
	defer cancel()
	status, err := h.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.host.ConnectModel(ctx, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "model", ExpectedRevision: &status.Configuration.Revision}, Config: appserver.ConnectConfig{Provider: "openai-compatible", Model: "gpt-4.1", BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC"}}); err != nil {
		t.Fatal(err)
	}
	bot, _ := registerApplicationHTTP(t, ctx, h, "bot", filepath.Join(root, "bot.token"))
	created, err := bot.CreateWorker(ctx, appserver.CreateWorkerRequest{WriteBase: appserver.WriteBase{OperationID: "create"}, CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	sid := created.SessionID
	if _, err = h.host.ConfigureSessionMode(ctx, appserver.SessionModeRequest{WriteBase: appserver.WriteBase{OperationID: "manual", SessionID: sid, ExpectedRevision: &created.Revision}, Mode: "manual"}); err != nil {
		t.Fatal(err)
	}
	feed, err := bot.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Subscription.Close()
	if _, err = bot.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "start", SessionID: sid}, Input: "Request the synthetic command, respecting the approval decision."}); err != nil {
		t.Fatal(err)
	}
	var approval *appserver.ActiveApproval
	for approval == nil {
		select {
		case d, ok := <-feed.Subscription.Deliveries():
			if !ok {
				t.Fatal(feed.Subscription.Err())
			}
			for _, e := range d.Events {
				if e.Kind == eventstream.KindRequestPermission {
					state, e := bot.InspectSession(ctx, appserver.StateRequest{SessionID: sid})
					if e != nil {
						t.Fatal(e)
					}
					approval = state.Approval.Active
				}
				if eventstream.IsTurnTerminalLifecycle(e) {
					t.Fatalf("completed without approval: %+v", e)
				}
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	option := ""
	for _, o := range approval.Permission.Options {
		if strings.HasPrefix(o.Kind, "reject") {
			option = o.ID
			break
		}
	}
	if option == "" {
		t.Fatalf("no reject choice: %+v", approval.Permission)
	}
	request := appserver.ResolveApprovalRequest{WriteBase: appserver.WriteBase{OperationID: "bot-decision", SessionID: sid}, Target: approval.Target, ApprovalRequestID: string(approval.RequestID), Outcome: "selected", OptionID: option}
	results := make(chan appserver.CommandResult, 2)
	start := make(chan struct{})
	for i, client := range []*httpclient.Client{bot, h.host} {
		go func(i int, c *httpclient.Client) {
			<-start
			r := request
			if i == 1 {
				r.OperationID = "user-decision"
			}
			v, _ := c.ResolveApproval(ctx, r)
			results <- v
		}(i, client)
	}
	close(start)
	successes := 0
	for range 2 {
		select {
		case r := <-results:
			if r.Outcome == appserver.OutcomeCommitted {
				successes++
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if successes != 1 {
		t.Fatalf("approval winners=%d", successes)
	}
	request.OperationID = "stale-decision"
	if r, e := bot.ResolveApproval(ctx, request); e == nil && r.Outcome == appserver.OutcomeCommitted {
		t.Fatal("stale approval applied")
	}
}
