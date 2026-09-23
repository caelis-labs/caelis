package gatewayapp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
)

// backgroundBarrier is a real provider HTTP transport behind the production
// Host. It holds only one named worker's first model invocation so cancellation
// can be checked while two other Sessions continue independently.
type backgroundBarrier struct {
	model   *applicationHTTPModel
	mu      sync.Mutex
	armed   bool
	reached chan struct{}
	release chan struct{}
}

func (b *backgroundBarrier) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(raw))
	b.mu.Lock()
	block := b.armed && strings.Contains(string(raw), "B11_BLOCK_WORKER_SENTINEL")
	if block {
		b.armed = false
		close(b.reached)
	}
	b.mu.Unlock()
	if block {
		select {
		case <-b.release:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	return b.model.RoundTrip(req)
}

func backgroundHostModel(t *testing.T, ctx context.Context, host *applicationHTTPHost) {
	t.Helper()
	status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := host.host.ConnectModel(ctx, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "connect-background-model", ExpectedRevision: &status.Configuration.Revision},
		Config:    appserver.ConnectConfig{Provider: "openai-compatible", Model: "gpt-4.1", BaseURL: "https://provider.invalid/v1", APIKey: "BACKGROUND_MODEL_SECRET_SENTINEL"},
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel = %+v %v", result, err)
	}
}

func backgroundPrompt(t *testing.T, ctx context.Context, client *httpclient.Client, sessionID, op, input, kind, grantID string) appserver.CommandResult {
	t.Helper()
	result, err := client.PromptApplication(ctx, appserver.ApplicationPromptRequest{
		PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: sessionID, OperationID: op}, Input: input},
		SourceKind:    kind, GrantID: grantID,
	})
	if err != nil || (result.Outcome != appserver.OutcomeCommitted && result.Outcome != appserver.OutcomeAccepted) {
		t.Fatalf("PromptApplication(%s/%s) = %+v %v", sessionID, op, result, err)
	}
	return result
}

func backgroundGrant(t *testing.T, ctx context.Context, client *httpclient.Client, sessionID, op, source string) application.BackgroundGrant {
	t.Helper()
	grant, err := client.CreateApplicationBackgroundGrant(ctx, sessionID, application.BackgroundGrantRequest{
		OperationID: op, Source: source, AuthorizationOperationID: "user-consent-" + op,
	})
	if err != nil || grant.ID == "" || grant.SessionID != sessionID || grant.Revoked {
		t.Fatalf("CreateApplicationBackgroundGrant = %+v %v", grant, err)
	}
	return grant
}

func backgroundCall(t *testing.T, ctx context.Context, client *httpclient.Client, sessionID string) application.Call {
	t.Helper()
	calls, err := client.WaitApplicationCalls(ctx, sessionID)
	if err != nil || len(calls) != 1 {
		t.Fatalf("WaitApplicationCalls(%s) = %+v %v", sessionID, calls, err)
	}
	return calls[0]
}

func completeBackgroundCall(t *testing.T, ctx context.Context, client *httpclient.Client, call application.Call) {
	t.Helper()
	if _, err := client.ClaimApplicationCall(ctx, call.SessionID, call.ID); err != nil {
		t.Fatal(err)
	}
	if err := client.CompleteApplicationCall(ctx, call.SessionID, call.ID, application.CallResult{Outcome: "succeeded", Content: json.RawMessage(`{"value":"BACKGROUND_RESULT_SENTINEL"}`)}); err != nil {
		t.Fatal(err)
	}
	waitApplicationHTTPIdle(t, ctx, client, call.SessionID)
}

func backgroundModelMessages(t *testing.T, raw []byte) []json.RawMessage {
	t.Helper()
	var request struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &request); err != nil || len(request.Messages) == 0 {
		t.Fatalf("model invocation messages = %s, %v", raw, err)
	}
	return request.Messages
}

func checkBackgroundHistory(t *testing.T, ctx context.Context, client *httpclient.Client, sessionID string, grant application.BackgroundGrant, input string) {
	t.Helper()
	for _, event := range applicationHTTPHistory(t, ctx, client, sessionID) {
		chunk, ok := event.Update.(eventstream.ContentChunk)
		if !ok || !strings.Contains(fmt.Sprint(chunk.Content), input) {
			continue
		}
		if chunk.SessionUpdate != eventstream.UpdateUserMessage || event.AgentCommunicationSource == nil ||
			event.AgentCommunicationSource.Kind != "system" || event.AgentCommunicationSource.ID != grant.ID {
			t.Fatalf("authorized background became user/summary input in canonical replay: %#v", event)
		}
		return
	}
	t.Fatalf("background input %q missing from canonical replay", input)
}

// TestApplicationBackgroundHostB08B09B11 exercises grant lifecycle, canonical
// model-context replay and three independently controllable Sessions through the
// public HTTP client and real Host/Runtime. No test-only Control executor is used.
func TestApplicationBackgroundHostB08B09B11(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 85*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	model := &applicationHTTPModel{}
	provider := &backgroundBarrier{model: model, reached: make(chan struct{}), release: make(chan struct{})}
	store := filepath.Join(root, "store")
	host := startApplicationHTTPHost(t, store, workspace, provider)
	defer func() {
		if host != nil {
			host.close(t)
		}
	}()
	backgroundHostModel(t, ctx, host)
	secretPath := filepath.Join(root, "app.credential")
	client, connection := registerApplicationHTTP(t, ctx, host, "background", secretPath)
	other, _ := registerApplicationHTTP(t, ctx, host, "other-background", filepath.Join(root, "other.credential"))
	assistant := createApplicationHTTPSession(t, ctx, client, "assistant")
	workerA := createApplicationHTTPSession(t, ctx, client, "worker-a")
	workerB := createApplicationHTTPSession(t, ctx, client, "worker-b")
	otherSession := createApplicationHTTPSession(t, ctx, other, "other-worker")

	// B08: grant requires an exact owned Session and explicit prior application
	// authorization operation; a peer's credential or a different worker's grant
	// cannot authorize prompts into this Session.
	grantA := backgroundGrant(t, ctx, client, workerA, "grant-worker-a", "schedule/worker-a")
	grantB := backgroundGrant(t, ctx, client, workerB, "grant-worker-b", "schedule/worker-b")
	if grantA.Scope != connection.Scope || grantA.AuthorizationOperationID != "user-consent-grant-worker-a" {
		t.Fatalf("grant ownership/attestation = %+v", grantA)
	}
	list, err := client.ListApplicationBackgroundGrants(ctx, workerA)
	if err != nil || len(list) != 1 || list[0] != grantA {
		t.Fatalf("grant list = %+v %v", list, err)
	}
	if _, err := other.ApplicationBackgroundGrant(ctx, workerA, grantA.ID); err == nil {
		t.Fatal("second connection read grant")
	}
	if _, err := other.CreateApplicationBackgroundGrant(ctx, workerA, application.BackgroundGrantRequest{OperationID: "forged-grant", Source: "forged", AuthorizationOperationID: "forged-consent"}); err == nil {
		t.Fatal("second connection granted first connection's Session")
	}
	if _, err := client.CreateApplicationBackgroundGrant(ctx, otherSession, application.BackgroundGrantRequest{OperationID: "forged-grant-2", Source: "forged", AuthorizationOperationID: "forged-consent"}); err == nil {
		t.Fatal("application created grant for another connection")
	}
	for _, bad := range []appserver.ApplicationPromptRequest{
		{PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: workerA, OperationID: "no-grant"}, Input: "not authorized"}, SourceKind: "authorized_background"},
		{PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: workerB, OperationID: "wrong-grant"}, Input: "not authorized"}, SourceKind: "authorized_background", GrantID: grantA.ID},
		{PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: workerA, OperationID: "summary-with-grant"}, Input: "not authorized"}, SourceKind: "application_summary", GrantID: grantA.ID},
	} {
		if _, err := client.PromptApplication(ctx, bad); err == nil {
			t.Fatalf("accepted unauthorized prompt %+v", bad)
		}
	}

	// B09: a background Turn produces an actual callback and a distinct canonical
	// source actor. The following model invocation and an actual Host reopen both
	// rebuild from committed native context, not a projection-only snapshot.
	backgroundInput := "B09_BACKGROUND_WORK_SENTINEL"
	receipt := backgroundPrompt(t, ctx, client, workerA, "background-first", backgroundInput, "authorized_background", grantA.ID)
	call := backgroundCall(t, ctx, client, workerA)
	if call.Source != (application.Source{Kind: "authorized_background", OperationID: "background-first", GrantID: grantA.ID, AuthorizedSource: grantA.Source}) {
		t.Fatalf("callback source = %+v", call.Source)
	}
	observer, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: workerA})
	if err != nil || !observer.State.Run.Active {
		t.Fatalf("detached observer precondition: %+v %v", observer.State, err)
	}
	if err := observer.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	if active, err := client.ApplicationCall(ctx, workerA, call.ID); err != nil || active.State != "pending" {
		t.Fatalf("UI detach stopped background callback: %+v %v", active, err)
	}
	completeBackgroundCall(t, ctx, client, call)
	checkBackgroundHistory(t, ctx, client, workerA, grantA, backgroundInput)
	before := model.snapshotFrom(0)
	if !strings.Contains(before, backgroundInput) || !strings.Contains(before, "BACKGROUND_RESULT_SENTINEL") {
		t.Fatalf("live model lacked background input/result: %s", before)
	}
	if !strings.Contains(before, "From: Authorized background: "+grantA.Source) {
		t.Fatalf("live model context lost descriptive authorized actor: %s", before)
	}
	preRestartCount := model.requestCount()
	liveMessages := backgroundModelMessages(t, model.requestAt(preRestartCount-1))
	host.close(t)
	host = startApplicationHTTPHost(t, store, workspace, provider)
	secret, err := os.ReadFile(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	client = host.app(string(secret))
	persistedGrant, err := client.ApplicationBackgroundGrant(ctx, workerA, grantA.ID)
	if err != nil || persistedGrant != grantA {
		t.Fatalf("grant reopen = %+v %v", persistedGrant, err)
	}
	checkBackgroundHistory(t, ctx, client, workerA, grantA, backgroundInput)
	backgroundPrompt(t, ctx, client, workerA, "background-after-restart", "B09_RESUME_SENTINEL", "authorized_background", grantA.ID)
	resume := backgroundCall(t, ctx, client, workerA)
	completeBackgroundCall(t, ctx, client, resume)
	modelContext := model.snapshotFrom(preRestartCount)
	rebuiltMessages := backgroundModelMessages(t, model.requestAt(preRestartCount))
	if len(rebuiltMessages) < len(liveMessages) || !reflect.DeepEqual(rebuiltMessages[:len(liveMessages)], liveMessages) {
		t.Fatalf("reopened model prefix differs from live native context: live=%s rebuilt=%s", liveMessages, rebuiltMessages)
	}
	for _, sentinel := range []string{backgroundInput, "BACKGROUND_RESULT_SENTINEL", "B09_RESUME_SENTINEL", "From: Authorized background: " + grantA.Source} {
		if !strings.Contains(modelContext, sentinel) {
			t.Fatalf("reopened model context missing %s: %s", sentinel, modelContext)
		}
	}
	if strings.Contains(modelContext, "BACKGROUND_MODEL_SECRET_SENTINEL") {
		t.Fatalf("model context exposed provider credentials: %s", modelContext)
	}
	// Reusing an accepted operation after revocation returns the original native
	// receipt without a second provider call; a new operation fails closed.
	revoked, err := client.RevokeApplicationBackgroundGrant(ctx, workerA, grantA.ID)
	if err != nil || !revoked.Revoked {
		t.Fatalf("revoke = %+v %v", revoked, err)
	}
	count := model.requestCount()
	replay := backgroundPrompt(t, ctx, client, workerA, "background-first", backgroundInput, "authorized_background", grantA.ID)
	if !reflect.DeepEqual(replay, receipt) || model.requestCount() != count {
		t.Fatalf("idempotent receipt reran after revoke: first=%+v retry=%+v count=%d/%d", receipt, replay, model.requestCount(), count)
	}
	if _, err := client.PromptApplication(ctx, appserver.ApplicationPromptRequest{PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: workerA, OperationID: "after-revoke"}, Input: "must not start"}, SourceKind: "authorized_background", GrantID: grantA.ID}); err == nil {
		t.Fatal("revoked grant admitted new prompt")
	}

	// B11: the assistant and two independent workers are distinct native Sessions.
	// Holding one worker at the provider barrier cannot block the other worker or
	// assistant. Cancel addresses only that worker's exact native target; the
	// surviving worker completes and the cancelled worker may later resume under
	// a new (explicitly attested) grant.
	grantA2 := backgroundGrant(t, ctx, client, workerA, "grant-worker-a-resume", "schedule/worker-a/resumed")
	provider.mu.Lock()
	provider.armed = true
	provider.mu.Unlock()
	blocked := backgroundPrompt(t, ctx, client, workerA, "worker-a-block", "B11_BLOCK_WORKER_SENTINEL", "authorized_background", grantA2.ID)
	select {
	case <-provider.reached:
	case <-ctx.Done():
		t.Fatalf("worker provider barrier never reached: %v", ctx.Err())
	}
	backgroundPrompt(t, ctx, client, workerB, "worker-b-parallel", "B11_WORKER_B_SENTINEL", "authorized_background", grantB.ID)
	workerBCall := backgroundCall(t, ctx, client, workerB)
	// Revoking a grant stops new admissions, not this already accepted worker
	// callback (or the unrelated assistant and worker A).
	grantB, err = client.RevokeApplicationBackgroundGrant(ctx, workerB, grantB.ID)
	if err != nil || !grantB.Revoked {
		t.Fatalf("revoke in-flight worker B = %+v %v", grantB, err)
	}
	backgroundPrompt(t, ctx, client, assistant, "assistant-parallel", "B11_ASSISTANT_SENTINEL", "user", "")
	assistantCall := backgroundCall(t, ctx, client, assistant)
	if blocked.Target.HandleID == "" || blocked.Target.RunID == "" || blocked.Target.TurnID == "" {
		t.Fatalf("worker cancel target missing: %+v", blocked)
	}
	cancelled, err := client.Cancel(ctx, appserver.CancelRequest{WriteBase: appserver.WriteBase{SessionID: workerA, OperationID: "cancel-worker-a"}, Target: blocked.Target, Reason: "cancel exact worker"})
	if err != nil || (cancelled.Outcome != appserver.OutcomeCommitted && cancelled.Outcome != appserver.OutcomeAccepted) {
		t.Fatalf("Cancel(worker A) = %+v %v", cancelled, err)
	}
	close(provider.release)
	completeBackgroundCall(t, ctx, client, workerBCall)
	completeBackgroundCall(t, ctx, client, assistantCall)
	waitApplicationHTTPIdle(t, ctx, client, workerA)
	if persisted, err := client.ApplicationBackgroundGrant(ctx, workerB, grantB.ID); err != nil || persisted != grantB {
		t.Fatalf("worker B revoked grant lost on worker A cancel: %+v %v", persisted, err)
	}
	backgroundPrompt(t, ctx, client, workerA, "worker-a-resume", "B11_WORKER_A_RESUMED_SENTINEL", "authorized_background", grantA2.ID)
	resumedCall := backgroundCall(t, ctx, client, workerA)
	completeBackgroundCall(t, ctx, client, resumedCall)
	checkBackgroundHistory(t, ctx, client, workerB, grantB, "B11_WORKER_B_SENTINEL")
	checkBackgroundHistory(t, ctx, client, workerA, grantA2, "B11_WORKER_A_RESUMED_SENTINEL")
	if !strings.Contains(model.snapshotFrom(preRestartCount), "B11_ASSISTANT_SENTINEL") {
		t.Fatal("assistant's independent Session did not reach model")
	}
	// A second actual Host replacement must retain each independent result and
	// revocation; no old blocked worker is redispatched on restart.
	beforeRestart := model.requestCount()
	host.close(t)
	host = startApplicationHTTPHost(t, store, workspace, provider)
	client = host.app(string(secret))
	for _, item := range []struct {
		session string
		grant   application.BackgroundGrant
		input   string
	}{
		{workerA, grantA2, "B11_WORKER_A_RESUMED_SENTINEL"},
		{workerB, grantB, "B11_WORKER_B_SENTINEL"},
	} {
		checkBackgroundHistory(t, ctx, client, item.session, item.grant, item.input)
		persisted, err := client.ApplicationBackgroundGrant(ctx, item.session, item.grant.ID)
		if err != nil || persisted != item.grant {
			t.Fatalf("worker grant lost across Host restart: %+v %v", persisted, err)
		}
	}
	if got, err := client.ApplicationBackgroundGrant(ctx, workerA, grantA.ID); err != nil || !got.Revoked {
		t.Fatalf("revoked authority recovered as live: %+v %v", got, err)
	}
	if model.requestCount() != beforeRestart {
		t.Fatalf("Host restart redispatched old worker: %d -> %d", beforeRestart, model.requestCount())
	}
	foundAssistant := false
	for _, event := range applicationHTTPHistory(t, ctx, client, assistant) {
		if chunk, ok := event.Update.(eventstream.ContentChunk); ok && strings.Contains(fmt.Sprint(chunk.Content), "B11_ASSISTANT_SENTINEL") {
			foundAssistant = event.Actor == "user" && event.AgentCommunicationSource == nil
		}
	}
	if !foundAssistant {
		t.Fatal("assistant user Turn missing or misclassified after restart")
	}
}
