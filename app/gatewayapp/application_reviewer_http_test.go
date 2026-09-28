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

	sessionapi "github.com/caelis-labs/caelis/agent-sdk/session"
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
	profile.Tools[0].InputSchema = map[string]any{"type": "object", "properties": map[string]any{
		"key":   map[string]any{"type": "string"},
		"id":    map[string]any{"type": "integer"},
		"range": map[string]any{"type": "object", "properties": map[string]any{"min": map[string]any{"type": "integer"}}, "additionalProperties": false},
	}, "required": []any{"key"}, "additionalProperties": false}
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

func reviewerHTTPJournal(t *testing.T, ctx context.Context, host *applicationHTTPHost, sessionID string) []*sessionapi.Event {
	t.Helper()
	active, err := host.stack.Sessions().Session(ctx, sessionapi.SessionRef{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	events, err := host.stack.Sessions().Events(ctx, sessionapi.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// reviewerHTTPExecutionStep reads the real Runtime's durable invocation journal,
// not a provider tool_call_id (which can be reused on later Turns).
func reviewerHTTPExecutionStep(t *testing.T, ctx context.Context, host *applicationHTTPHost, sessionID, turnID, toolName string) string {
	t.Helper()
	step := ""
	events := reviewerHTTPJournal(t, ctx, host, sessionID)
	for _, event := range events {
		if event.Journal == nil || event.Journal.ToolExecution == nil {
			continue
		}
		execution := event.Journal.ToolExecution
		key := execution.Key
		if key.TurnID != turnID || execution.ToolName != toolName || key.ToolCallID != "reviewed-provider-tool-id" {
			continue
		}
		if key.SessionID != sessionID || key.StepID == "" || (step != "" && step != key.StepID) {
			t.Fatalf("invalid invocation journal identity: %+v; prior step=%q", key, step)
		}
		step = key.StepID
	}
	if step == "" {
		t.Fatalf("no journal invocation for Session %s Turn %s tool %s", sessionID, turnID, toolName)
	}
	return step
}

func reviewerHTTPPauseItem(t *testing.T, ctx context.Context, host *applicationHTTPHost, sessionID, turnID, toolName string, status sessionapi.PauseTokenStatus) string {
	t.Helper()
	item := ""
	for _, event := range reviewerHTTPJournal(t, ctx, host, sessionID) {
		if event.Journal == nil || event.Journal.PauseToken == nil {
			continue
		}
		token := event.Journal.PauseToken
		if token.TurnID != turnID || token.ToolName != toolName || token.Status != status {
			continue
		}
		if token.SessionID != sessionID || token.ToolCallID != "reviewed-provider-tool-id" || token.ItemID == "" || (item != "" && item != token.ItemID) {
			t.Fatalf("invalid persisted approval identity: %+v", token)
		}
		item = token.ItemID
	}
	if item == "" {
		t.Fatalf("no %s approval token for Session %s Turn %s tool %s", status, sessionID, turnID, toolName)
	}
	return item
}

func reviewerHTTPReviewItem(t *testing.T, events []eventstream.Envelope, turnID, status, step string) {
	t.Helper()
	found := false
	for _, env := range events {
		if env.Kind != eventstream.KindApprovalReview || env.ApprovalReview == nil || env.TurnID != turnID || env.ApprovalReview.Status != status {
			continue
		}
		found = true
		if step == "" || env.ApprovalReview.ItemID != step || env.ApprovalReview.ToolCallID != "reviewed-provider-tool-id" {
			t.Fatalf("%s review must identify invocation step %q, not provider call: %+v", status, step, env)
		}
	}
	if !found {
		t.Fatalf("missing %s review of Turn %s: %+v", status, turnID, reviewerHTTPReviewSummary(events))
	}
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

func reviewerHTTPExactNumericReview(t *testing.T, events []eventstream.Envelope, status string) {
	t.Helper()
	const want = `{"id":9007199254740991,"key":"one","range":{"min":-9007199254740991}}`
	found := false
	for _, env := range events {
		if env.Kind != eventstream.KindApprovalReview || env.ApprovalReview == nil || env.ApprovalReview.Status != status {
			continue
		}
		found = true
		raw, err := json.Marshal(env.ApprovalReview.RawInput)
		if err != nil || string(raw) != want || env.ApprovalReview.ToolCallID != "reviewed-provider-tool-id" || env.TurnID == "" {
			t.Fatalf("%s review changed safe numeric arguments/identity: %+v; input=%s; err=%v", status, env, raw, err)
		}
	}
	if !found {
		t.Fatalf("missing %s review: %+v", status, reviewerHTTPReviewSummary(events))
	}
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
	const args = `{"key":"one","id":9007199254740991,"range":{"min":-9007199254740991}}`
	provider := &reviewerHTTPProvider{toolName: "ApplicationLookup", args: args, decision: `{"option_id":"allow_once"}`,
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
	prompted := promptReviewerHTTP(t, ctx, client, session, "prompt-reviewed-allow")
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
	step := reviewerHTTPPauseItem(t, ctx, host, session, prompted.Target.TurnID, "ApplicationLookup", sessionapi.PauseTokenPending)
	reviewerHTTPExactNumericReview(t, live, "in_progress")
	reviewerHTTPReviewItem(t, live, prompted.Target.TurnID, "in_progress", step)
	close(provider.release)
	calls, err := client.WaitApplicationCalls(ctx, session)
	if err != nil || len(calls) != 1 || calls[0].State != "pending" {
		t.Fatalf("approved callback intent: %+v %v; requests=%+v", calls, err, provider.snapshot())
	}
	call := calls[0]
	if journalStep := reviewerHTTPExecutionStep(t, ctx, host, session, call.TurnID, "ApplicationLookup"); journalStep != step {
		t.Fatalf("approved callback step %q differs from durable invocation step %q", step, journalStep)
	}
	if call.CallID != "reviewed-provider-tool-id" || call.ItemID != step || call.TurnID != prompted.Target.TurnID || call.ConfigurationRevision != 1 || call.ToolsVersion != applicationHTTPProfile().ToolsVersion || string(call.Arguments) != args {
		t.Fatalf("approved callback not pinned to submitted catalog/args: %+v", call)
	}
	claimed, err := client.ClaimApplicationCall(ctx, session, call.ID)
	if err != nil || claimed.State != "claimed" || claimed.ID != call.ID || claimed.CallID != call.CallID || claimed.TurnID != call.TurnID || string(claimed.Arguments) != args {
		t.Fatalf("claimed callback changed arguments/identity: %+v %v", claimed, err)
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
	reviewerHTTPExactNumericReview(t, live, "approved")
	reviewerHTTPReviewItem(t, live, call.TurnID, "approved", step)
	history := applicationHTTPHistory(t, ctx, client, session)
	reviewerHTTPExactNumericReview(t, history, "approved")
	reviewerHTTPReviewItem(t, history, call.TurnID, "approved", step)
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
	restartedHistory := applicationHTTPHistory(t, ctx, client, session)
	reviewerHTTPExactNumericReview(t, restartedHistory, "approved")
	reviewerHTTPReviewItem(t, restartedHistory, call.TurnID, "approved", step)
	if restartedStep := reviewerHTTPExecutionStep(t, ctx, host, session, call.TurnID, "ApplicationLookup"); restartedStep != step {
		t.Fatalf("Host restart changed invocation step: %q -> %q", step, restartedStep)
	}
	for _, env := range restartedHistory {
		if env.Kind == eventstream.KindApprovalReview && env.ApprovalReview != nil && mirrorIDs[env.EventID] {
			delete(mirrorIDs, env.EventID)
		}
	}
	if len(mirrorIDs) != 0 {
		t.Fatalf("review event IDs changed across Host restart: %+v", mirrorIDs)
	}
	replayed, err := client.ApplicationCall(ctx, session, call.ID)
	if err != nil || replayed.State != "completed" || replayed.ID != call.ID || replayed.CallID != call.CallID || replayed.TurnID != call.TurnID || replayed.ItemID != step || string(replayed.Arguments) != args {
		t.Fatalf("original callback receipt after restart changed arguments/identity: %+v %v", replayed, err)
	}
	if len(provider.snapshot()) != 3 {
		t.Fatalf("expected main/reviewer/main, got provider requests: %+v", provider.snapshot())
	}
	// The provider reuses its tool_call_id on a later Turn, even after Host
	// replacement. Session/Turn/item, not the provider ID or bare item,
	// identifies each invocation.
	second := promptReviewerHTTP(t, ctx, client, session, "prompt-reviewed-reused-id")
	secondCalls, err := client.WaitApplicationCalls(ctx, session)
	if err != nil || len(secondCalls) != 1 || secondCalls[0].State != "pending" {
		t.Fatalf("second reviewed callback: %+v %v", secondCalls, err)
	}
	secondCall := secondCalls[0]
	secondStep := reviewerHTTPExecutionStep(t, ctx, host, session, second.Target.TurnID, "ApplicationLookup")
	if secondCall.ItemID != secondStep || secondCall.CallID != call.CallID || secondCall.TurnID == call.TurnID || secondCall.ID == call.ID {
		t.Fatalf("provider ID reuse collapsed invocation identity: first=%+v second=%+v steps=%q/%q", call, secondCall, step, secondStep)
	}
	if _, err := client.ClaimApplicationCall(ctx, session, secondCall.ID); err != nil {
		t.Fatal(err)
	}
	if err := client.CompleteApplicationCall(ctx, session, secondCall.ID, result); err != nil {
		t.Fatal(err)
	}
	waitApplicationHTTPIdle(t, ctx, client, session)
	reusedHistory := applicationHTTPHistory(t, ctx, client, session)
	reviewerHTTPReviewItem(t, reusedHistory, call.TurnID, "approved", step)
	reviewerHTTPReviewItem(t, reusedHistory, secondCall.TurnID, "approved", secondStep)
}

// reviewerHTTPUnsafeTool checks only optional tool observation payloads; the
// original provider arguments remain unmodified even when v1 cannot display them.
func reviewerHTTPUnsafeTool(t *testing.T, events []eventstream.Envelope, turnID string) (proposed, failed bool, ids map[string]bool) {
	t.Helper()
	ids = make(map[string]bool)
	for _, env := range events {
		if env.TurnID != turnID {
			continue
		}
		if env.Kind == eventstream.KindApprovalReview {
			t.Fatalf("unsafe callback reached approval review: %+v", env)
		}
		var rawInput any
		switch update := env.Update.(type) {
		case eventstream.ToolCall:
			if update.ToolCallID != "reviewed-provider-tool-id" {
				continue
			}
			proposed = true
			rawInput = update.RawInput
		case eventstream.ToolCallUpdate:
			if update.ToolCallID != "reviewed-provider-tool-id" {
				continue
			}
			rawInput = update.RawInput
			if update.Status != nil && *update.Status == "failed" {
				failed = true
				raw, err := json.Marshal(update)
				if err != nil || !strings.Contains(string(raw), "exceeds the exact JavaScript range") {
					t.Fatalf("failed tool lacks explicit numeric rejection: %+v; err=%v", update, err)
				}
			}
		default:
			continue
		}
		if rawInput != nil {
			t.Fatalf("unsafe rawInput exposed on v1 tool observation: %+v", env)
		}
		if env.EventID != "" {
			ids[env.EventID] = true
		}
	}
	return proposed, failed, ids
}

func TestApplicationReviewerHTTPUnsafeNumberRejectedBeforeApproval(t *testing.T) {
	for _, tc := range []struct{ name, args string }{
		{"integer", `{"key":"one","id":9007199254740993}`},
		{"decimal-rounded-to-safe-bound", `{"key":"one","id":9007199254740991.1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			root := t.TempDir()
			provider := &reviewerHTTPProvider{toolName: "ApplicationLookup", args: tc.args, decision: `{"option_id":"allow_once"}`}
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
				case delivery, ok := <-feed.Subscription.Deliveries():
					if !ok {
						t.Fatalf("unsafe review feed closed before sync: %v", feed.Subscription.Err())
					}
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
			prompted := promptReviewerHTTP(t, ctx, client, session, "prompt-reviewed-unsafe-number")
			var live []eventstream.Envelope
			for {
				proposed, failed, _ := reviewerHTTPUnsafeTool(t, live, prompted.Target.TurnID)
				if proposed && failed {
					break
				}
				select {
				case delivery, ok := <-feed.Subscription.Deliveries():
					if !ok {
						t.Fatalf("unsafe tool live feed closed: %v", feed.Subscription.Err())
					}
					events, _, err := assembler.Accept(delivery)
					if err != nil {
						t.Fatal(err)
					}
					live = append(live, events...)
				case <-ctx.Done():
					t.Fatal("unsafe numeric tool rejection missing from live SSE")
				}
			}
			waitApplicationHTTPIdle(t, ctx, client, session)
			reviewerHTTPNoCallbacks(t, ctx, client, session)
			history := applicationHTTPHistory(t, ctx, client, session)
			proposed, failed, replayIDs := reviewerHTTPUnsafeTool(t, history, prompted.Target.TurnID)
			if !proposed || !failed || len(replayIDs) == 0 {
				t.Fatalf("unsafe numeric rejection missing from reconnect: proposed=%v failed=%v ids=%+v", proposed, failed, replayIDs)
			}
			requests := provider.snapshot()
			if len(requests) != 2 || requests[0]["model"] != "gpt-4.1" || requests[1]["model"] != "gpt-4.1" {
				t.Fatalf("unsafe call invoked Guardian or did not return tool error to model: %+v", requests)
			}
			messages, _ := requests[1]["messages"].([]any)
			if len(messages) == 0 {
				t.Fatalf("provider follow-up missing rejected tool result: %+v", requests[1])
			}
			last, _ := messages[len(messages)-1].(map[string]any)
			if last["role"] != "tool" || !strings.Contains(fmt.Sprint(last["content"]), "exceeds the exact JavaScript range") {
				t.Fatalf("provider did not receive explicit rejected tool result: %+v", last)
			}
			originalArguments := false
			for _, item := range messages {
				message, _ := item.(map[string]any)
				if message["role"] != "assistant" {
					continue
				}
				toolCalls, _ := message["tool_calls"].([]any)
				for _, toolCall := range toolCalls {
					call, _ := toolCall.(map[string]any)
					function, _ := call["function"].(map[string]any)
					originalArguments = originalArguments || function["arguments"] == tc.args
				}
			}
			if !originalArguments {
				t.Fatalf("model context lost original unsafe numeric argument: %+v", messages)
			}
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
			restarted := applicationHTTPHistory(t, ctx, client, session)
			proposed, failed, restartedIDs := reviewerHTTPUnsafeTool(t, restarted, prompted.Target.TurnID)
			if !proposed || !failed || len(replayIDs) != len(restartedIDs) {
				t.Fatalf("unsafe tool replay changed after restart: proposed=%v failed=%v before=%+v after=%+v", proposed, failed, replayIDs, restartedIDs)
			}
			for id := range replayIDs {
				if !restartedIDs[id] {
					t.Fatalf("unsafe tool event ID %s changed after restart: before=%+v after=%+v", id, replayIDs, restartedIDs)
				}
			}
			reviewerHTTPNoCallbacks(t, ctx, client, session)
			if len(provider.snapshot()) != 2 {
				t.Fatalf("restart repeated rejected call: %+v", provider.snapshot())
			}
		})
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
			defer func() { host.close(t) }()
			prompted := promptReviewerHTTP(t, ctx, client, session, "prompt-reviewed-"+tc.name)
			waitApplicationHTTPIdle(t, ctx, client, session)
			reviewerHTTPNoCallbacks(t, ctx, client, session)
			history := applicationHTTPHistory(t, ctx, client, session)
			if tc.name == "deny" && len(reviewerHTTPReviewEvents(t, history, tc.wantStatus)) != 1 ||
				tc.name == "failed" && !reviewerHTTPLiveStatus(history, tc.wantStatus) {
				t.Fatalf("terminal review not observed on %s: reviews=%+v; requests=%+v", tc.name, reviewerHTTPReviewSummary(history), provider.snapshot())
			}
			if tc.name == "deny" {
				step := reviewerHTTPPauseItem(t, ctx, host, session, prompted.Target.TurnID, "ApplicationLookup", sessionapi.PauseTokenResolved)
				reviewerHTTPReviewItem(t, history, prompted.Target.TurnID, "denied", step)
				for _, event := range reviewerHTTPJournal(t, ctx, host, session) {
					if event.Journal != nil && event.Journal.ToolExecution != nil && event.Journal.ToolExecution.Key.TurnID == prompted.Target.TurnID {
						t.Fatalf("denied callback wrote execution journal: %+v", event.Journal.ToolExecution)
					}
				}
				host.close(t)
				host = startApplicationHTTPHost(t, filepath.Join(root, "store"), filepath.Join(root, "workspace"), provider)
				secret, err := os.ReadFile(filepath.Join(root, "application.credential"))
				if err != nil {
					t.Fatal(err)
				}
				client = host.app(string(secret))
				reviewerHTTPNoCallbacks(t, ctx, client, session)
				reviewerHTTPReviewItem(t, applicationHTTPHistory(t, ctx, client, session), prompted.Target.TurnID, "denied", step)
				if reopened := reviewerHTTPPauseItem(t, ctx, host, session, prompted.Target.TurnID, "ApplicationLookup", sessionapi.PauseTokenResolved); reopened != step {
					t.Fatalf("Host restart changed denied invocation item: %q -> %q", step, reopened)
				}
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
