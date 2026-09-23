package gatewayapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/bot"
	"github.com/caelis-labs/caelis/internal/testenv"
)

type botHostProvider func(*http.Request) (*http.Response, error)

func (f botHostProvider) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func botHostResponse(r *http.Request, body string) *http.Response {
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		panic(err)
	}
	for _, raw := range payload["choices"].([]any) {
		choice := raw.(map[string]any)
		choice["index"] = 0
		message := choice["message"].(map[string]any)
		if calls, ok := message["tool_calls"].([]any); ok {
			for i, raw := range calls {
				raw.(map[string]any)["index"] = i
			}
		}
		choice["delta"] = message
		delete(choice, "message")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(data) + "\n\ndata: [DONE]\n\n")), Request: r}
}

func TestBotNativeHostHTTPWorkApprovalReportAndRecovery(t *testing.T) {
	if os.Getenv("CAELIS_TEST_BOT_NATIVE") != "1" {
		t.Skip("set CAELIS_TEST_BOT_NATIVE=1 for native managed work Host integration")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	var workerCalls, reports atomic.Int64
	var liveContext, replayContext atomic.Value
	provider := &http.Client{Transport: botHostProvider(func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if strings.Contains(string(raw), "professional agent in an isolated managed work session") {
			call := workerCalls.Add(1)
			switch call {
			case 2:
				liveContext.Store(raw)
			case 3:
				replayContext.Store(raw)
			}
			if call == 1 {
				return botHostResponse(r, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"work-command","type":"function","function":{"name":"RunCommand","arguments":"{\"command\":\"printf WORK_RESULT > result.txt; cat result.txt\",\"sandbox_permissions\":\"require_escalated\",\"justification\":\"Verify the assigned report in its owned directory\"}"}}]},"finish_reason":"tool_calls"}]}`), nil
			}
			return botHostResponse(r, `{"choices":[{"message":{"role":"assistant","content":"WORK_RESULT"},"finish_reason":"stop"}]}`), nil
		}
		if strings.Contains(string(raw), "Report this completed work to the user once.") {
			reports.Add(1)
		}
		return botHostResponse(r, `{"choices":[{"message":{"role":"assistant","content":"BOT_REPLY"},"finish_reason":"stop"}]}`), nil
	})}
	root := t.TempDir()
	workspace := t.TempDir()
	cfg := gatewayapp.Config{AppName: "caelis-test", UserID: "owner", StoreDir: filepath.Join(root, "store"), WorkspaceCWD: workspace, SkillDirs: []string{}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"}, ResolveProviderHTTPClient: func(context.Context, gatewayapp.ModelConfig) (*http.Client, error) { return provider, nil }}
	start := func() (*gatewayapp.Stack, *httptest.Server, *httpclient.Client) {
		t.Helper()
		host, err := gatewayapp.NewLocalStack(cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = host.Close() })
		localServer, err := local.NewAppServer(host)
		if err != nil {
			t.Fatal(err)
		}
		auth, err := controlserver.BearerTokenAuthenticator(strings.Repeat("a", 64), appserver.Principal{ID: "owner"})
		if err != nil {
			t.Fatal(err)
		}
		handler, err := controlserver.Handler(controlserver.Dependencies{Services: localServer.Services}, controlserver.Config{Authenticator: auth, AllowedHosts: []string{"127.0.0.1"}})
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		remote, err := httpclient.New(httpclient.Config{BaseURL: server.URL, BearerToken: strings.Repeat("a", 64), HTTPClient: server.Client(), Compatibility: appserver.CurrentCompatibility()})
		if err != nil {
			t.Fatal(err)
		}
		return host, server, remote
	}
	host, server, remote := start()
	status, err := remote.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.ConnectModel(ctx, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "connect-fixture", ExpectedRevision: &status.Configuration.Revision}, Config: appserver.ConnectConfig{Provider: "openai-compatible", Model: "gpt-4.1", BaseURL: "https://provider.invalid/v1", APIKey: "MODEL_CREDENTIAL_SENTINEL"}}); err != nil {
		t.Fatal(err)
	}
	created, err := remote.CreateBot(ctx, appserver.CreateBotRequest{WriteBase: appserver.WriteBase{OperationID: "create-bot"}, Config: bot.Config{Name: "Assistant", ManagedWork: true, DesktopActions: true}})
	if err != nil {
		t.Fatal(err)
	}
	botID := created.Resource.Ref
	botSession := created.SessionID
	waitState := func(c *httpclient.Client, id string, predicate func(appserver.SessionState) bool) appserver.SessionState {
		t.Helper()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			state, err := c.InspectSession(ctx, appserver.StateRequest{SessionID: id})
			if err != nil {
				t.Fatal(err)
			}
			if predicate(state) {
				return state
			}
			select {
			case <-ctx.Done():
				t.Fatal("timed out waiting for native state")
			case <-ticker.C:
			}
		}
	}
	_, err = remote.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: botSession, OperationID: "user-request"}, Input: "Produce the report, and verify its file."})
	if err != nil {
		t.Fatal(err)
	}
	waitState(remote, botSession, func(s appserver.SessionState) bool { return !s.Run.Active })
	source, err := remote.GetBotRequest(ctx, botID, "user-request")
	if err != nil {
		t.Fatal(err)
	}
	req := appserver.BotWorkRequest{WriteBase: appserver.WriteBase{SessionID: botSession, OperationID: "create-work"}, BotID: botID, SourceID: source.ID, Assignment: "Write and verify a report."}
	work, err := remote.CreateBotWork(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	state := waitState(remote, work.SessionID, func(s appserver.SessionState) bool { return s.Approval.Active != nil })
	approval := state.Approval.Active
	feed, err := remote.Reconnect(ctx, appserver.ReconnectRequest{SessionID: work.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if feed.State.Approval.Active == nil || feed.State.Approval.Active.Target != approval.Target {
		t.Fatal("atomic work bootstrap lost the approval target")
	}
	if err := feed.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := remote.Reconnect(ctx, appserver.ReconnectRequest{SessionID: work.SessionID, Cursor: feed.State.BoundaryCursor})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State.Approval.Active == nil || resumed.State.Approval.Active.RequestID != approval.RequestID || !resumed.State.Run.Active {
		t.Fatal("SSE disconnect cancelled work or changed its approval head")
	}
	if err := resumed.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	if approval.Permission == nil || len(approval.Permission.Options) == 0 {
		t.Fatal("native approval options missing")
	}
	option := ""
	for _, candidate := range approval.Permission.Options {
		if candidate.Kind == "allow_once" {
			option = candidate.ID
		}
	}
	if option == "" {
		t.Fatalf("allow_once unavailable: %+v", approval.Permission.Options)
	}
	decision := appserver.ResolveApprovalRequest{WriteBase: appserver.WriteBase{SessionID: work.SessionID, OperationID: "approval"}, Target: approval.Target, ApprovalRequestID: string(approval.RequestID), Outcome: "selected", OptionID: option, Approved: true}
	stale := decision
	stale.OperationID = "stale-approval"
	stale.Target.TurnID = "old-turn"
	if _, err := remote.ResolveApproval(ctx, stale); err == nil {
		t.Fatal("stale approval accepted")
	}
	if _, err := remote.ResolveApproval(ctx, decision); err != nil {
		t.Fatal(err)
	}

	waitState(remote, work.SessionID, func(s appserver.SessionState) bool { return !s.Run.Active })
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var notices []bot.Completion
	for {
		notices, err = remote.ListBotCompletions(ctx, botID)
		if err != nil {
			t.Fatal(err)
		}
		if len(notices) == 1 && notices[0].ReportState == "admitted" && reports.Load() == 1 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("completion/report: %+v calls=%d", notices, reports.Load())
		case <-ticker.C:
		}
	}
	waitState(remote, botSession, func(s appserver.SessionState) bool { return !s.Run.Active })
	item, err := remote.GetBotWork(ctx, botID, work.Resource.Ref)
	if err != nil || item.Result != "WORK_RESULT" {
		t.Fatalf("work result: %+v %v", item, err)
	}
	duplicate, err := remote.CreateBotWork(ctx, req)
	if err != nil || duplicate.Target != work.Target {
		t.Fatalf("duplicate changed: %+v %v", duplicate, err)
	}
	conflict := req
	conflict.Assignment = "changed"
	if _, err := remote.CreateBotWork(ctx, conflict); err == nil {
		t.Fatal("conflicting operation accepted")
	}
	ack := appserver.BotWorkRequest{WriteBase: appserver.WriteBase{SessionID: botSession, OperationID: "ack"}, BotID: botID, WorkID: notices[0].ID}
	if _, err := remote.AcknowledgeBotCompletion(ctx, ack); err != nil {
		t.Fatal(err)
	}
	before, err := remote.Initialize(ctx)
	if err != nil {
		t.Fatal(err)
	}
	server.Close()
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, restarted := start()
	after, err := restarted.Initialize(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.InstanceID == after.InstanceID || before.StoreID != after.StoreID {
		t.Fatal("Host/store identity did not survive correctly")
	}
	notices, err = restarted.ListBotCompletions(ctx, botID)
	if err != nil || len(notices) != 1 || !notices[0].Acknowledged || reports.Load() != 1 {
		t.Fatalf("restart duplicated report: %+v %d %v", notices, reports.Load(), err)
	}
	recovered, err := restarted.GetBotWorkOperation(ctx, botID, "create-work")
	if err != nil || recovered.Execution != item.Execution {
		t.Fatalf("operation recovery: %+v %v", recovered, err)
	}
	// A continued work handle receives another native execution after a fresh user request.
	_, err = restarted.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: botSession, OperationID: "continue-source"}, Input: "Continue the same report with an update."})
	if err != nil {
		t.Fatal(err)
	}
	waitState(restarted, botSession, func(s appserver.SessionState) bool { return !s.Run.Active })
	nextSource, err := restarted.GetBotRequest(ctx, botID, "continue-source")
	if err != nil {
		t.Fatal(err)
	}
	next, err := restarted.ContinueBotWork(ctx, appserver.BotWorkRequest{WriteBase: appserver.WriteBase{SessionID: botSession, OperationID: "continue-work"}, BotID: botID, WorkID: item.ID, SourceID: nextSource.ID, Assignment: "Update report."})
	if err != nil || next.Resource.Ref != item.ID || next.Target == work.Target {
		t.Fatalf("continue identity: %+v %v", next, err)
	}
	waitState(restarted, next.SessionID, func(s appserver.SessionState) bool { return !s.Run.Active })
	if liveContext.Load() == nil || replayContext.Load() == nil {
		t.Fatal("worker did not produce both live and recovered model requests")
	}
	var live, replay struct {
		Messages []json.RawMessage `json:"messages"`
		Tools    json.RawMessage   `json:"tools"`
	}
	if err := json.Unmarshal(liveContext.Load().([]byte), &live); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(replayContext.Load().([]byte), &replay); err != nil {
		t.Fatal(err)
	}
	if len(replay.Messages) <= len(live.Messages) || !reflect.DeepEqual(live.Messages, replay.Messages[:len(live.Messages)]) || !reflect.DeepEqual(live.Tools, replay.Tools) {
		t.Fatal("restarted work changed the runtime-produced model prefix or tools")
	}
}

func TestBotHostHTTPDesktopLeaseSSEAndExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var calls atomic.Int64
	var clientSecret, dispatchSecret atomic.Value
	clientSecret.Store("")
	dispatchSecret.Store("")
	provider := &http.Client{Transport: botHostProvider(func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		for _, secret := range []string{strings.Repeat("a", 64), "MODEL_CREDENTIAL_SENTINEL", clientSecret.Load().(string), dispatchSecret.Load().(string)} {
			if secret != "" && strings.Contains(string(raw), secret) {
				return nil, errors.New("native credentials reached model")
			}
		}
		if calls.Add(1) == 1 {
			return botHostResponse(r, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"clock-call","type":"function","function":{"name":"DesktopClock","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`), nil
		}
		return botHostResponse(r, `{"choices":[{"message":{"role":"assistant","content":"CLOCK_DONE"},"finish_reason":"stop"}]}`), nil
	})}
	host, err := gatewayapp.NewLocalStack(gatewayapp.Config{AppName: "caelis-test", UserID: "owner", StoreDir: t.TempDir(), WorkspaceCWD: t.TempDir(), SkillDirs: []string{}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"}, ResolveProviderHTTPClient: func(context.Context, gatewayapp.ModelConfig) (*http.Client, error) { return provider, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	assembly, err := local.NewAppServer(host)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 64)
	auth, err := controlserver.BearerTokenAuthenticator(token, appserver.Principal{ID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := controlserver.Handler(controlserver.Dependencies{Services: assembly.Services}, controlserver.Config{Authenticator: auth, AllowedHosts: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	server := testenv.NewHTTPServer(t, handler)
	clientFor := func(token string) *httpclient.Client {
		t.Helper()
		c, err := httpclient.New(httpclient.Config{BaseURL: server.URL, BearerToken: token, HTTPClient: server.Client(), Compatibility: appserver.CurrentCompatibility()})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	admin := clientFor(token)
	status, err := admin.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ConnectModel(ctx, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "connect", ExpectedRevision: &status.Configuration.Revision}, Config: appserver.ConnectConfig{Provider: "openai-compatible", Model: "gpt-4.1", BaseURL: "https://provider.invalid/v1", APIKey: "MODEL_CREDENTIAL_SENTINEL"}}); err != nil {
		t.Fatal(err)
	}
	created, err := admin.CreateBot(ctx, appserver.CreateBotRequest{WriteBase: appserver.WriteBase{OperationID: "create"}, Config: bot.Config{Name: "Secretary", DesktopActions: true}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := admin.CreateBot(ctx, appserver.CreateBotRequest{WriteBase: appserver.WriteBase{OperationID: "other"}, Config: bot.Config{Name: "Other"}})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := admin.RegisterBotClient(ctx, appserver.RegisterBotClientRequest{WriteBase: appserver.WriteBase{OperationID: "enroll"}, BotID: created.Resource.Ref, Actions: []string{"clock"}})
	if err != nil {
		t.Fatal(err)
	}
	client := clientFor(enrollment.Token)
	clientSecret.Store(enrollment.Token)
	active, err := client.ActivateBotClient(ctx, created.Resource.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetBot(ctx, other.Resource.Ref); err == nil {
		t.Fatal("client read another Bot")
	}
	if _, err := client.CreateBot(ctx, appserver.CreateBotRequest{WriteBase: appserver.WriteBase{OperationID: "escape"}, Config: bot.Config{Name: "Escape"}}); err == nil {
		t.Fatal("client created outside scope")
	}
	if _, err := client.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: created.SessionID, OperationID: "clock-source"}, Input: "What time is it?"}); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("snapshot consumed")
	var snapshot bot.DesktopSnapshot
	if err := client.WatchBotDesktop(ctx, created.Resource.Ref, "", func(s bot.DesktopSnapshot) error {
		if len(s.Calls) > 0 {
			snapshot = s
			return stop
		}
		return nil
	}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	action := snapshot.Calls[0]
	if action.Execution.InstanceID != host.BotWork().Store.InstanceID || action.ItemID != "clock-call" {
		t.Fatalf("action identity lost: %+v", action)
	}
	claim, err := client.ClaimBotDesktopCall(ctx, created.Resource.Ref, action.ID)
	if err != nil {
		t.Fatal(err)
	}
	dispatchSecret.Store(claim.Token)
	if _, err := client.ClaimBotDesktopCall(ctx, created.Resource.Ref, action.ID); err == nil {
		t.Fatal("unknown dispatch replay allowed")
	}
	receipt := bot.DesktopReceipt{Token: claim.Token, Result: json.RawMessage(`{"time":"2026-09-23T09:00:00+08:00"}`)}
	if _, err := client.CompleteBotDesktopCall(ctx, created.Resource.Ref, action.ID, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CompleteBotDesktopCall(ctx, created.Resource.Ref, action.ID, receipt); err != nil {
		t.Fatal(err)
	}
	done, err := client.GetBotDesktopCall(ctx, created.Resource.Ref, action.ID)
	if err != nil || done.State != "completed" {
		t.Fatalf("receipt recovery: %+v %v", done, err)
	}
	for {
		state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: created.SessionID})
		if err != nil {
			t.Fatal(err)
		}
		if !state.Run.Active {
			if calls.Load() != 2 {
				t.Fatalf("desktop receipt did not complete model continuation: %d calls", calls.Load())
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if err := client.WatchBotDesktop(ctx, created.Resource.Ref, snapshot.Cursor, func(s bot.DesktopSnapshot) error {
		if s.Cursor == snapshot.Cursor || len(s.Calls) != 0 {
			t.Fatal("SSE did not reconcile terminal action")
		}
		return stop
	}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	exit := appserver.BotClientExitRequest{WriteBase: appserver.WriteBase{SessionID: created.SessionID, OperationID: "exit"}, BotID: created.Resource.Ref, ActivationID: active.ActivationID, CancelOwnedWork: true}
	first, err := client.ExitBotClient(ctx, exit)
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.ExitBotClient(ctx, exit)
	if err != nil || first.Outcome != second.Outcome {
		t.Fatalf("exit receipt replay: %+v %v", second, err)
	}
	state, err := client.GetBotClient(ctx, created.Resource.Ref)
	if err != nil || state.Active {
		t.Fatalf("exit recovery: %+v %v", state, err)
	}
	if _, err := client.BotDesktopSnapshot(ctx, created.Resource.Ref); err == nil {
		t.Fatal("exited client retained tools")
	}
	if _, err := admin.Initialize(ctx); err != nil {
		t.Fatal("client exit stopped shared Host")
	}
	next, err := client.ActivateBotClient(ctx, created.Resource.Ref)
	if err != nil || next.ActivationID == active.ActivationID {
		t.Fatalf("reactivation: %+v %v", next, err)
	}
	exit.OperationID = "stale-exit"
	if _, err := client.ExitBotClient(ctx, exit); err == nil {
		t.Fatal("stale activation exit accepted")
	}
}
