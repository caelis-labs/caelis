package gatewayapp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
)

// reviewerHTTPProvider is a deterministic real-provider-adapter transport. Main
// requests emit one configured tool call and then answer on the tool result;
// reviewer requests use a distinct configured model and emit only a decision.
// toolName/args are configurable for native as well as callback policy tests.
type reviewerHTTPProvider struct {
	mu       sync.Mutex
	requests []map[string]any
	toolName string
	args     string
	decision string
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (p *reviewerHTTPProvider) RoundTrip(req *http.Request) (*http.Response, error) {
	defer req.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.requests = append(p.requests, body)
	p.mu.Unlock()
	if body["model"] == "reviewer-model" {
		if p.entered != nil {
			p.once.Do(func() { close(p.entered) })
		}
		if p.release != nil {
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-p.release:
			}
		}
		return reviewerHTTPStream(req, map[string]any{"role": "assistant", "content": p.decision}, "stop")
	}
	if body["model"] != "gpt-4.1" {
		return nil, fmt.Errorf("unexpected provider model %v", body["model"])
	}
	messages, _ := body["messages"].([]any)
	lastRole := ""
	if len(messages) > 0 {
		last, _ := messages[len(messages)-1].(map[string]any)
		lastRole, _ = last["role"].(string)
	}
	if lastRole == "tool" {
		return reviewerHTTPStream(req, map[string]any{"role": "assistant", "content": "REVIEWED_CALLBACK_FINAL"}, "stop")
	}
	return reviewerHTTPStream(req, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
		"id": "reviewed-provider-tool-id", "index": 0, "type": "function",
		"function": map[string]any{"name": p.toolName, "arguments": p.args},
	}}}, "tool_calls")
}

func reviewerHTTPStream(req *http.Request, message map[string]any, finish string) (*http.Response, error) {
	raw, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": message, "finish_reason": finish}}})
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader("data: " + string(raw) + "\n\ndata: [DONE]\n\n")), Request: req}, nil
}

func (p *reviewerHTTPProvider) snapshot() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]map[string]any, len(p.requests))
	for i, body := range p.requests {
		raw, _ := json.Marshal(body)
		_ = json.Unmarshal(raw, &out[i])
	}
	return out
}

func setupReviewerHTTP(t *testing.T, ctx context.Context, root string, provider *reviewerHTTPProvider) (*applicationHTTPHost, *httpclient.Client, string) {
	t.Helper()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), workspace, provider)
	for _, model := range []string{"gpt-4.1", "reviewer-model"} {
		status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
		if err != nil {
			t.Fatal(err)
		}
		connected, err := host.host.ConnectModel(ctx, appserver.ConnectModelRequest{
			WriteBase: appserver.WriteBase{OperationID: "connect-" + model, ExpectedRevision: &status.Configuration.Revision},
			Config:    appserver.ConnectConfig{Provider: "openai-compatible", Model: model, BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY"},
		})
		if err != nil || connected.Outcome != appserver.OutcomeCommitted {
			t.Fatalf("connect %s: %+v %v", model, connected, err)
		}
	}
	client, _ := registerApplicationHTTP(t, ctx, host, "reviewed", filepath.Join(root, "application.credential"))
	profile := applicationHTTPProfile()
	profile.Permissions.ApprovalMode = "auto-review"
	profile.Reviewer = &application.Reviewer{Kind: "guardian", Model: "openai-compatible/reviewer-model"}
	profile.Tools[0].ApprovalPolicy = "required"
	profile.Tools[0].InputSchema = map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}}, "required": []any{"key"}, "additionalProperties": false}
	created, err := client.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{WriteBase: appserver.WriteBase{OperationID: "create-reviewed"}, Profile: profile})
	if err != nil || created.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("create: %+v %v", created, err)
	}
	state, err := client.ApplicationReviewerState(ctx, created.SessionID)
	if err != nil || state.Status != "ready" || state.ApprovalMode != "auto-review" || state.Reviewer == nil || state.Reviewer.Model != profile.Reviewer.Model {
		t.Fatalf("reviewer readiness: %+v %v", state, err)
	}
	return host, client, created.SessionID
}

func promptReviewerHTTP(t *testing.T, ctx context.Context, client *httpclient.Client, session, operation string) appserver.CommandResult {
	t.Helper()
	result, err := client.PromptApplication(ctx, appserver.ApplicationPromptRequest{
		PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: session, OperationID: operation}, Input: "Please look up key one."}, SourceKind: "user",
	})
	if err != nil || (result.Outcome != appserver.OutcomeAccepted && result.Outcome != appserver.OutcomeCommitted) {
		t.Fatalf("prompt %s: %+v %v", operation, result, err)
	}
	return result
}

func reviewerHTTPReviewEvents(t *testing.T, history []eventstream.Envelope, status string) []eventstream.Envelope {
	t.Helper()
	var matching []eventstream.Envelope
	for _, env := range history {
		if env.Kind != eventstream.KindApprovalReview || env.ApprovalReview == nil || env.ApprovalReview.ToolCallID != "reviewed-provider-tool-id" {
			continue
		}
		if env.Delivery == nil || env.Delivery.Mode != eventstream.DeliveryMirror {
			continue // Live progress can also be transient; replay must be mirrored.
		}
		if env.ApprovalReview.ToolName != "ApplicationLookup" || env.ApprovalReview.RawInput["key"] != "one" || env.TurnID == "" || env.EventID == "" {
			t.Fatalf("review mirror missing exact input/identity: review=%+v delivery=%+v event=%+v", env.ApprovalReview, env.Delivery, env)
		}
		if env.ApprovalReview.Status == status {
			matching = append(matching, env)
		}
	}
	return matching
}

func reviewerHTTPReviewSummary(history []eventstream.Envelope) []string {
	var out []string
	for _, env := range history {
		if env.Kind == eventstream.KindApprovalReview && env.ApprovalReview != nil {
			mode := eventstream.DeliveryMode("")
			if env.Delivery != nil {
				mode = env.Delivery.Mode
			}
			out = append(out, string(mode)+":"+env.ApprovalReview.Status)
		}
	}
	return out
}

func reviewerHTTPLiveStatus(history []eventstream.Envelope, status string) bool {
	for _, env := range history {
		if env.Kind == eventstream.KindApprovalReview && env.ApprovalReview != nil && env.ApprovalReview.Status == status && env.ApprovalReview.ToolCallID == "reviewed-provider-tool-id" && env.TurnID != "" {
			return true
		}
	}
	return false
}

func reviewerHTTPNoCallbacks(t *testing.T, ctx context.Context, client *httpclient.Client, session string) {
	t.Helper()
	calls, err := client.ApplicationCalls(ctx, session)
	if err != nil || len(calls) != 0 {
		t.Fatalf("review created claimable callback before authorization: %+v %v", calls, err)
	}
}

func TestApplicationReviewerHTTPAllowClaimReplayAndRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	provider := &reviewerHTTPProvider{toolName: "ApplicationLookup", args: `{"key":"one"}`, decision: `{"option_id":"allow_once"}`,
		entered: make(chan struct{}), release: make(chan struct{})}
	host, client, session := setupReviewerHTTP(t, ctx, root, provider)
	defer func() {
		if host != nil {
			host.close(t)
		}
	}()
	feed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: session})
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Subscription.Close()
	var assembler appserver.FeedDeliveryAssembler
	for {
		select {
		case delivery := <-feed.Subscription.Deliveries():
			if _, _, err := assembler.Accept(delivery); err != nil {
				t.Fatal(err)
			}
			if delivery.Kind == appserver.FeedDeliverySync {
				goto synced
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
synced:
	promptReviewerHTTP(t, ctx, client, session, "prompt-reviewed-allow")
	select {
	case <-provider.entered:
	case <-ctx.Done():
		t.Fatal("reviewer request never reached provider")
	}
	reviewerHTTPNoCallbacks(t, ctx, client, session)
	// The live subscription predates the prompt and must receive the same
	// mirrored in-progress review subsequently read via reconnect.
	var live []eventstream.Envelope
	for !reviewerHTTPLiveStatus(live, "in_progress") {
		select {
		case delivery, ok := <-feed.Subscription.Deliveries():
			if !ok {
				t.Fatalf("review live feed closed: %v", feed.Subscription.Err())
			}
			events, _, err := assembler.Accept(delivery)
			if err != nil {
				t.Fatal(err)
			}
			live = append(live, events...)
		case <-ctx.Done():
			t.Fatal("live approval review missing")
		}
	}
	close(provider.release)
	calls, err := client.WaitApplicationCalls(ctx, session)
	if err != nil || len(calls) != 1 || calls[0].State != "pending" {
		t.Fatalf("approved callback intent: %+v %v; requests=%+v", calls, err, provider.snapshot())
	}
	call := calls[0]
	if call.CallID != "reviewed-provider-tool-id" || call.ConfigurationRevision != 1 || call.ToolsVersion != applicationHTTPProfile().ToolsVersion || string(call.Arguments) != `{"key":"one"}` {
		t.Fatalf("approved callback not pinned to submitted catalog/args: %+v", call)
	}
	if _, err := client.ClaimApplicationCall(ctx, session, call.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ClaimApplicationCall(ctx, session, call.ID); err == nil {
		t.Fatal("duplicate effect claim authorized")
	}
	result := application.CallResult{Outcome: "succeeded", Content: json.RawMessage(`{"value":"approved"}`)}
	if err := client.CompleteApplicationCall(ctx, session, call.ID, result); err != nil {
		t.Fatal(err)
	}
	if err := client.CompleteApplicationCall(ctx, session, call.ID, result); err != nil {
		t.Fatalf("same result retry: %v", err)
	}
	waitApplicationHTTPIdle(t, ctx, client, session)
	for !reviewerHTTPLiveStatus(live, "approved") {
		select {
		case delivery, ok := <-feed.Subscription.Deliveries():
			if !ok {
				t.Fatalf("review live feed closed: %v", feed.Subscription.Err())
			}
			events, _, err := assembler.Accept(delivery)
			if err != nil {
				t.Fatal(err)
			}
			live = append(live, events...)
		case <-ctx.Done():
			t.Fatal("approved live review missing")
		}
	}
	history := applicationHTTPHistory(t, ctx, client, session)
	approved := reviewerHTTPReviewEvents(t, history, "approved")
	if len(approved) != 1 || approved[0].TurnID != call.TurnID {
		t.Fatalf("canonical terminal review replay incomplete: approved=%+v call=%+v", approved, call)
	}
	mirrorIDs := map[string]bool{approved[0].EventID: true}
	if err := feed.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	host.close(t)
	host = startApplicationHTTPHost(t, filepath.Join(root, "store"), filepath.Join(root, "workspace"), provider)
	secret, err := os.ReadFile(filepath.Join(root, "application.credential"))
	if err != nil {
		t.Fatal(err)
	}
	client = host.app(string(secret))
	for _, env := range applicationHTTPHistory(t, ctx, client, session) {
		if env.Kind == eventstream.KindApprovalReview && env.ApprovalReview != nil && mirrorIDs[env.EventID] {
			delete(mirrorIDs, env.EventID)
		}
	}
	if len(mirrorIDs) != 0 {
		t.Fatalf("review event IDs changed across Host restart: %+v", mirrorIDs)
	}
	replayed, err := client.ApplicationCall(ctx, session, call.ID)
	if err != nil || replayed.State != "completed" {
		t.Fatalf("original callback receipt after restart: %+v %v", replayed, err)
	}
	if len(provider.snapshot()) != 3 {
		t.Fatalf("expected main/reviewer/main, got provider requests: %+v", provider.snapshot())
	}
}

func TestApplicationReviewerHTTPDenyAndFailureNeverDispatch(t *testing.T) {
	for _, tc := range []struct{ name, decision, wantStatus string }{
		{"deny", `{"option_id":"reject_once","rationale":"not authorized"}`, "denied"},
		{"failed", `{"option_id":"permit_forever"}`, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 65*time.Second)
			defer cancel()
			root := t.TempDir()
			provider := &reviewerHTTPProvider{toolName: "ApplicationLookup", args: `{"key":"one"}`, decision: tc.decision}
			host, client, session := setupReviewerHTTP(t, ctx, root, provider)
			defer host.close(t)
			promptReviewerHTTP(t, ctx, client, session, "prompt-reviewed-"+tc.name)
			waitApplicationHTTPIdle(t, ctx, client, session)
			reviewerHTTPNoCallbacks(t, ctx, client, session)
			history := applicationHTTPHistory(t, ctx, client, session)
			if tc.name == "deny" && len(reviewerHTTPReviewEvents(t, history, tc.wantStatus)) != 1 ||
				tc.name == "failed" && !reviewerHTTPLiveStatus(history, tc.wantStatus) {
				t.Fatalf("terminal review not observed on %s: reviews=%+v; requests=%+v", tc.name, reviewerHTTPReviewSummary(history), provider.snapshot())
			}
			for _, req := range provider.snapshot() {
				if req["model"] == "reviewer-model" && req["tools"] != nil {
					t.Fatalf("callback reviewer received executable tools: %+v", req)
				}
			}
		})
	}
}

func TestApplicationReviewerHTTPManualDirectDoesNotInheritGuardian(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	root := t.TempDir()
	provider := &reviewerHTTPProvider{toolName: "ApplicationLookup", args: `{"key":"one"}`, decision: `{"option_id":"reject_once","rationale":"should not review direct"}`}
	host, client, _ := setupReviewerHTTP(t, ctx, root, provider)
	defer host.close(t)
	profile := applicationHTTPProfile() // omitted callback policy keeps direct dispatch.
	created, err := client.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{WriteBase: appserver.WriteBase{OperationID: "create-manual-direct"}, Profile: profile})
	if err != nil || created.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("create manual: %+v %v", created, err)
	}
	state, err := client.ApplicationReviewerState(ctx, created.SessionID)
	if err != nil || state.Status != "manual" || state.Reviewer != nil {
		t.Fatalf("manual reviewer state: %+v %v", state, err)
	}
	promptReviewerHTTP(t, ctx, client, created.SessionID, "prompt-manual-direct")
	calls, err := client.WaitApplicationCalls(ctx, created.SessionID)
	if err != nil || len(calls) != 1 || calls[0].State != "pending" {
		t.Fatalf("manual direct callback: %+v %v", calls, err)
	}
	if _, err := client.ClaimApplicationCall(ctx, created.SessionID, calls[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := client.CompleteApplicationCall(ctx, created.SessionID, calls[0].ID, application.CallResult{Outcome: "succeeded", Content: json.RawMessage(`{"value":"direct"}`)}); err != nil {
		t.Fatal(err)
	}
	waitApplicationHTTPIdle(t, ctx, client, created.SessionID)
	if reviewerHTTPLiveStatus(applicationHTTPHistory(t, ctx, client, created.SessionID), "in_progress") {
		t.Fatal("manual direct callback inherited automatic review")
	}
	for _, req := range provider.snapshot() {
		if req["model"] == "reviewer-model" {
			t.Fatalf("manual direct callback invoked Guardian: %+v", req)
		}
	}
}

func TestApplicationReviewerHTTPHotPolicyPinnedWhileReviewPending(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Second)
	defer cancel()
	root := t.TempDir()
	provider := &reviewerHTTPProvider{toolName: "ApplicationLookup", args: `{"key":"one"}`, decision: `{"option_id":"allow_once"}`,
		entered: make(chan struct{}), release: make(chan struct{})}
	host, client, session := setupReviewerHTTP(t, ctx, root, provider)
	defer host.close(t)
	prompted := promptReviewerHTTP(t, ctx, client, session, "prompt-reviewed-hot")
	select {
	case <-provider.entered:
	case <-ctx.Done():
		t.Fatal("reviewer never reached provider")
	}
	reviewerHTTPNoCallbacks(t, ctx, client, session)
	previous, err := client.ApplicationConfiguration(ctx, session)
	if err != nil || previous.Revision != 1 {
		t.Fatalf("initial configuration: %+v %v", previous, err)
	}
	changed := append([]application.ToolDefinition(nil), previous.Profile.Tools...)
	changed[0].ApprovalPolicy = "direct"
	changed[0].InputSchema = map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "integer"}}, "required": []any{"key"}}
	version := "lookup-tools/2"
	updated, err := client.UpdateApplicationConfiguration(ctx, session, application.UpdateConfigurationRequest{
		OperationID: "hot-direct", ExpectedConfigurationRevision: 1,
		Patch: application.ConfigurationPatch{ToolsVersion: &version, Tools: &changed},
	})
	if err != nil || updated.Revision != 2 {
		t.Fatalf("update while review pending: %+v %v", updated, err)
	}
	reviewerHTTPNoCallbacks(t, ctx, client, session)
	close(provider.release)
	calls, err := client.WaitApplicationCalls(ctx, session)
	if err != nil || len(calls) != 1 || calls[0].ConfigurationRevision != 1 || calls[0].ToolsVersion != previous.Profile.ToolsVersion || string(calls[0].Arguments) != `{"key":"one"}` || calls[0].TurnID != prompted.Target.TurnID {
		t.Fatalf("reviewed call lost pinned policy/schema/args: %+v %v", calls, err)
	}
	call := calls[0]
	if _, err := client.ClaimApplicationCall(ctx, session, call.ID); err != nil {
		t.Fatal(err)
	}
	if err := client.CompleteApplicationCall(ctx, session, call.ID, application.CallResult{Outcome: "succeeded", Content: json.RawMessage(`{"value":"old revision"}`)}); err != nil {
		t.Fatal(err)
	}
	waitApplicationHTTPIdle(t, ctx, client, session)
	if len(reviewerHTTPReviewEvents(t, applicationHTTPHistory(t, ctx, client, session), "approved")) != 1 {
		t.Fatal("hot policy replacement erased the original approval")
	}
}

func TestApplicationReviewerHTTPCancelBeforeApprovalNeverDispatches(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	root := t.TempDir()
	provider := &reviewerHTTPProvider{toolName: "ApplicationLookup", args: `{"key":"one"}`, decision: `{"option_id":"allow_once"}`,
		entered: make(chan struct{}), release: make(chan struct{})}
	host, client, session := setupReviewerHTTP(t, ctx, root, provider)
	defer host.close(t)
	prompted := promptReviewerHTTP(t, ctx, client, session, "prompt-reviewed-cancel")
	select {
	case <-provider.entered:
	case <-ctx.Done():
		t.Fatal("reviewer never reached provider")
	}
	reviewerHTTPNoCallbacks(t, ctx, client, session)
	if prompted.Target.HandleID == "" || prompted.Target.RunID == "" || prompted.Target.TurnID == "" {
		t.Fatalf("missing exact cancel target: %+v", prompted)
	}
	cancelled, err := client.Cancel(ctx, appserver.CancelRequest{WriteBase: appserver.WriteBase{SessionID: session, OperationID: "cancel-pending-review"}, Target: prompted.Target, Reason: "cancel before authorization"})
	if err != nil || (cancelled.Outcome != appserver.OutcomeAccepted && cancelled.Outcome != appserver.OutcomeCommitted) {
		t.Fatalf("cancel pending review: %+v %v", cancelled, err)
	}
	close(provider.release)
	waitApplicationHTTPIdle(t, ctx, client, session)
	reviewerHTTPNoCallbacks(t, ctx, client, session)
	for _, env := range applicationHTTPHistory(t, ctx, client, session) {
		if env.Kind == eventstream.KindApprovalReview && env.ApprovalReview != nil && env.TurnID != prompted.Target.TurnID {
			t.Fatalf("canceled approval changed review Turn: %+v", env)
		}
	}
}
