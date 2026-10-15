package gatewayapp_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

type sessionProfileProvider struct{ requests atomic.Int32 }

func (p *sessionProfileProvider) RoundTrip(req *http.Request) (*http.Response, error) {
	p.requests.Add(1)
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Request: req,
		Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"id\":\"answer\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"SHARED_SESSION_COMPLETE\"}]}]}}\n\ndata: [DONE]\n\n"))}, nil
}

func TestSessionProfileUsesOrdinaryControlPrincipalAndCommands(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	provider := &sessionProfileProvider{}
	host := startApplicationHTTPHostWithSavedModels(t, filepath.Join(root, "store"), t.TempDir(), provider)
	t.Cleanup(func() { host.close(t) })
	profile := sessionprofile.Profile{Version: "test/1", Model: "openai/gpt-5.4-mini", ToolsVersion: "empty/1", Execution: "tools-only", Instructions: "Return one short answer."}
	create := appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "create-shared"}, Profile: &profile}
	created, err := host.host.CreateSession(ctx, create)
	if err != nil || created.Outcome != appserver.OutcomeCommitted || created.SessionID == "" {
		t.Fatalf("ordinary create with explicit profile = %+v, %v", created, err)
	}
	retry, err := host.host.CreateSession(ctx, create)
	if err != nil || retry != created {
		t.Fatalf("create receipt changed: %+v / %+v, %v", created, retry, err)
	}
	profile.Instructions = "Different immutable creation"
	if _, err := host.host.CreateSession(ctx, create); errorcode.CodeOf(err) != errorcode.Conflict {
		t.Fatalf("changed create intent = %v", err)
	}
	state, err := host.host.InspectSession(ctx, appserver.StateRequest{SessionID: created.SessionID})
	if err != nil || state.SessionID != created.SessionID {
		t.Fatalf("ordinary observer = %+v, %v", state, err)
	}
	prompt := appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: created.SessionID, OperationID: "shared-prompt"}, Input: "Hello"}
	started, err := host.host.Prompt(ctx, prompt)
	if err != nil || started.Target.TurnID == "" {
		t.Fatalf("ordinary prompt = %+v, %v", started, err)
	}
	waitApplicationHTTPIdle(t, ctx, host.host, created.SessionID)
	repeated, err := host.host.Prompt(ctx, prompt)
	if err != nil || repeated != started || provider.requests.Load() != 1 {
		t.Fatalf("prompt replay = %+v, %v, requests=%d", repeated, err, provider.requests.Load())
	}
	// The same command namespace fences cross-entry reuse, regardless of whether
	// the Session used Host defaults or explicit client assembly.
	_, err = host.host.Cancel(ctx, appserver.CancelRequest{WriteBase: prompt.WriteBase, Target: started.Target})
	if errorcode.CodeOf(err) != errorcode.Conflict {
		t.Fatalf("prompt/cancel ID collision = %v", err)
	}
}

func TestSessionConfigurationCommitsOnlyTheCommonOperationReceipt(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	storeDir := filepath.Join(t.TempDir(), "store")
	host := startApplicationHTTPHostWithSavedModels(t, storeDir, t.TempDir(), &sessionProfileProvider{})
	defer host.close(t)
	profile := sessionprofile.Profile{Version: "test/1", Model: "openai/gpt-5.4-mini", ToolsVersion: "empty/1", Execution: "tools-only", Instructions: "original"}
	created, err := host.host.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "config-session"}, Profile: &profile})
	if err != nil {
		t.Fatal(err)
	}
	instructions := "updated"
	req := appserver.UpdateSessionConfigurationRequest{WriteBase: appserver.WriteBase{OperationID: "configuration-1", SessionID: created.SessionID}, ExpectedConfigurationRevision: 1, Patch: sessionprofile.ConfigurationPatch{Instructions: &instructions}}
	first, err := host.host.UpdateSessionConfiguration(ctx, req)
	if err != nil || first.Configuration == nil || first.Configuration.Revision != 2 || first.Configuration.Profile.Instructions != instructions {
		t.Fatalf("configuration commit = %+v, %v", first, err)
	}
	original := req
	nextInstructions := "later"
	req.OperationID = "configuration-2"
	req.ExpectedConfigurationRevision = 2
	req.Patch.Instructions = &nextInstructions
	if _, err := host.host.UpdateSessionConfiguration(ctx, req); err != nil {
		t.Fatal(err)
	}
	observed, err := host.host.Operation(ctx, original.OperationID)
	if err != nil || !reflect.DeepEqual(observed, first) {
		t.Fatalf("read-only receipt = %+v, %v", observed, err)
	}
	repeated, err := host.host.UpdateSessionConfiguration(ctx, original)
	if err != nil || !reflect.DeepEqual(first, repeated) {
		t.Fatalf("original receipt = %+v, %v", repeated, err)
	}
	// A native model-selection write must not report success against a value
	// that explicit request assembly ignores. Both surfaces use the same patch.
	_, err = host.host.UseSessionModel(ctx, appserver.SessionModelRequest{WriteBase: appserver.WriteBase{OperationID: "ignored-model", SessionID: created.SessionID, ExpectedRevision: &created.Revision}, Model: "openai/gpt-4.1"})
	if errorcode.CodeOf(err) != errorcode.Unsupported {
		t.Fatalf("ignored model source accepted: %v", err)
	}
	current, err := host.host.SessionConfiguration(ctx, created.SessionID)
	if err != nil || current.Revision != 3 || current.Profile.Instructions != "later" {
		t.Fatalf("latest = %+v, %v", current, err)
	}
	// A configuration ID cannot admit a prompt in the shared namespace.
	_, err = host.host.Prompt(ctx, appserver.PromptRequest{WriteBase: original.WriteBase, Input: "must not dispatch"})
	if errorcode.CodeOf(err) != errorcode.Conflict {
		t.Fatalf("cross-action collision = %v", err)
	}
	// Query the real Host database independently of the transport projection.
	db, err := sql.Open("sqlite", filepath.Join(storeDir, "control", "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var common, legacy int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM control_operations WHERE operation_id LIKE 'configuration-%'`).Scan(&common); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='app_operations'`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if common != 2 || legacy != 0 {
		t.Fatalf("operation rows: common=%d application=%d", common, legacy)
	}
}

func TestSessionResourceReceiptSurvivesHostRestartAndExpiry(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	storeDir, cwd := filepath.Join(t.TempDir(), "store"), t.TempDir()
	host := startApplicationHTTPHostWithSavedModels(t, storeDir, cwd, &sessionProfileProvider{})
	defer func() { host.close(t) }()
	profile := sessionprofile.Profile{Version: "test/1", Model: "openai/gpt-5.4-mini", ToolsVersion: "empty/1", Execution: "tools-only"}
	created, err := host.host.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "resource-session"}, Profile: &profile})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("immutable uploaded content")
	digest := sha256.Sum256(data)
	deadline := time.Now().UTC().Add(2 * time.Second)
	req := appserver.SessionResourceRequest{WriteBase: appserver.WriteBase{OperationID: "original-upload", SessionID: created.SessionID}, Name: "input.txt", MediaType: "text/plain", Data: data, SHA256: hex.EncodeToString(digest[:]), ExpiresAt: &deadline}
	uploaded, err := host.host.CreateSessionResource(ctx, req)
	if err != nil || uploaded.SessionResource == nil {
		t.Fatalf("upload = %+v, %v", uploaded, err)
	}
	content, err := host.host.SessionResourceContent(ctx, created.SessionID, uploaded.SessionResource.ID)
	if err != nil || string(content.Data) != string(data) {
		t.Fatalf("bytes = %+v, %v", content, err)
	}
	changed := req
	changed.Name = "other.txt"
	if _, err := host.host.CreateSessionResource(ctx, changed); errorcode.CodeOf(err) != errorcode.Conflict {
		t.Fatalf("changed retry = %v", err)
	}
	if _, err := host.host.Prompt(ctx, appserver.PromptRequest{WriteBase: req.WriteBase, Input: "must not dispatch"}); errorcode.CodeOf(err) != errorcode.Conflict {
		t.Fatalf("cross-action reuse = %v", err)
	}
	host.close(t)
	host = startApplicationHTTPHostWithSavedModels(t, storeDir, cwd, &sessionProfileProvider{})
	timer := time.NewTimer(time.Until(deadline) + 10*time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-timer.C:
	}
	recovered, err := host.host.Operation(ctx, req.OperationID)
	if err != nil || !reflect.DeepEqual(recovered, uploaded) {
		t.Fatalf("recovered original = %+v, %v", recovered, err)
	}
	descriptor, err := host.host.SessionResource(ctx, created.SessionID, uploaded.SessionResource.ID)
	if err != nil || !reflect.DeepEqual(descriptor, *uploaded.SessionResource) {
		t.Fatalf("expired descriptor = %+v, %v", descriptor, err)
	}
	if _, err := host.host.SessionResourceContent(ctx, created.SessionID, descriptor.ID); errorcode.CodeOf(err) != errorcode.FailedPrecondition {
		t.Fatalf("expired bytes = %v", err)
	}
	repeated, err := host.host.CreateSessionResource(ctx, req)
	if err != nil || !reflect.DeepEqual(repeated, uploaded) {
		t.Fatalf("retry renewed receipt = %+v, %v", repeated, err)
	}
}

func TestSessionProfileCreationRecoveryKeepsOriginalRevision(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	storeDir := filepath.Join(t.TempDir(), "store")
	provider := &sessionProfileProvider{}
	host := startApplicationHTTPHostWithSavedModels(t, storeDir, t.TempDir(), provider)
	defer host.close(t)
	profile := sessionprofile.Profile{Version: "test/1", Model: "openai/gpt-5.4-mini", ToolsVersion: "empty/1", Execution: "tools-only"}
	created, err := host.host.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "recover-create"}, Profile: &profile})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.host.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "advance-history", SessionID: created.SessionID}, Input: "Hello"}); err != nil {
		t.Fatal(err)
	}
	waitApplicationHTTPIdle(t, ctx, host.host, created.SessionID)
	current, err := host.host.InspectSession(ctx, appserver.StateRequest{SessionID: created.SessionID})
	if err != nil || current.Revision <= created.Revision {
		t.Fatalf("history did not advance: %+v, %v", current, err)
	}
	// Reproduce the durable crash window after immutable creation evidence but
	// before receipt completion; no command is re-executed to recover it.
	db, err := sql.Open("sqlite", filepath.Join(storeDir, "control", "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT record_json FROM control_operations WHERE operation_id='recover-create'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var record appserver.OperationRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	record.Result = nil
	record.RetainUntil = time.Time{}
	raw, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE control_operations SET record_json=?, outcome='', retain_until_ns=0 WHERE operation_id='recover-create'`, raw); err != nil {
		t.Fatal(err)
	}
	recovered, err := host.host.Operation(ctx, "recover-create")
	if err != nil || recovered != created {
		t.Fatalf("recovery changed original creation: got=%+v want=%+v err=%v", recovered, created, err)
	}
	if provider.requests.Load() != 1 {
		t.Fatalf("recovery dispatched model: %d requests", provider.requests.Load())
	}
}

func TestSessionBackgroundAndToolCallsUseCommonOwnerAndReceipts(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	provider := &applicationHTTPModel{}
	storeDir := filepath.Join(t.TempDir(), "store")
	host := startApplicationHTTPHost(t, storeDir, t.TempDir(), provider)
	defer host.close(t)
	status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.host.ConnectModel(ctx, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "model", ExpectedRevision: &status.Configuration.Revision}, Config: appserver.ConnectConfig{Provider: "custom-chat", Model: "gpt-4.1", BaseURL: "https://provider.invalid/v1", APIKey: "fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	profile := applicationHTTPProfile()
	created, err := host.host.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "background-session"}, Profile: &profile})
	if err != nil {
		t.Fatal(err)
	}
	grantRequest := appserver.CreateSessionBackgroundGrantRequest{WriteBase: appserver.WriteBase{OperationID: "background-consent", SessionID: created.SessionID}, Source: "approved-timer", AuthorizationOperationID: "user-consent"}
	grant, err := host.host.CreateSessionBackgroundGrant(ctx, grantRequest)
	if err != nil || grant.BackgroundGrant == nil || grant.BackgroundGrant.Revoked {
		t.Fatalf("grant = %+v, %v", grant, err)
	}
	prompt := appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "timer-turn", SessionID: created.SessionID}, Input: "Perform the authorized lookup.", SourceKind: "authorized_background", GrantID: grant.BackgroundGrant.ID}
	started, err := host.host.Prompt(ctx, prompt)
	if err != nil {
		t.Fatal(err)
	}
	calls, err := host.host.WaitSessionToolCalls(ctx, created.SessionID)
	if err != nil || len(calls) != 1 {
		t.Fatalf("tool calls = %+v, %v", calls, err)
	}
	call := calls[0]
	if call.Source.Kind != "authorized_background" || call.Source.GrantID != grant.BackgroundGrant.ID || call.Source.AuthorizedSource != grantRequest.Source || call.TurnID != started.Target.TurnID || call.ToolsVersion != profile.ToolsVersion {
		t.Fatalf("tool identity/source changed: %+v", call)
	}
	nextVersion := "lookup-tools/2"
	_, err = host.host.UpdateSessionConfiguration(ctx, appserver.UpdateSessionConfigurationRequest{WriteBase: appserver.WriteBase{OperationID: "new-tools", SessionID: created.SessionID}, ExpectedConfigurationRevision: 1, Patch: sessionprofile.ConfigurationPatch{ToolsVersion: &nextVersion}})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := host.host.ClaimSessionToolCall(ctx, created.SessionID, call.ID)
	if err != nil || claimed.State != "claimed" || claimed.ToolsVersion != profile.ToolsVersion {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if _, err := host.host.ClaimSessionToolCall(ctx, created.SessionID, call.ID); errorcode.CodeOf(err) != errorcode.Conflict {
		t.Fatalf("second claim = %v", err)
	}
	result := sessionprofile.CallResult{Outcome: "succeeded", Content: json.RawMessage(`{"value":"original result"}`)}
	if err := host.host.CompleteSessionToolCall(ctx, created.SessionID, call.ID, result); err != nil {
		t.Fatal(err)
	}
	if err := host.host.CompleteSessionToolCall(ctx, created.SessionID, call.ID, result); err != nil {
		t.Fatalf("exact result retry = %v", err)
	}
	waitApplicationHTTPIdle(t, ctx, host.host, created.SessionID)
	revoked, err := host.host.RevokeSessionBackgroundGrant(ctx, appserver.RevokeSessionBackgroundGrantRequest{WriteBase: appserver.WriteBase{OperationID: "revoke-consent", SessionID: created.SessionID}, GrantID: grant.BackgroundGrant.ID})
	if err != nil || revoked.BackgroundGrant == nil || !revoked.BackgroundGrant.Revoked {
		t.Fatalf("revoke = %+v, %v", revoked, err)
	}
	original, err := host.host.Operation(ctx, grant.OperationID)
	if err != nil || !reflect.DeepEqual(original, grant) || original.BackgroundGrant.Revoked {
		t.Fatalf("grant receipt changed after revoke: %+v, %v", original, err)
	}
	observed, err := host.host.SessionBackgroundGrant(ctx, created.SessionID, grant.BackgroundGrant.ID)
	if err != nil || !observed.Revoked {
		t.Fatalf("live grant = %+v, %v", observed, err)
	}
	prompt.OperationID = "after-revoke"
	if _, err := host.host.Prompt(ctx, prompt); err == nil {
		t.Fatal("revoked background admitted")
	}
	// All client-originated commands share one ID owner; grants do not open an
	// independent operation namespace alongside ordinary Session commands.
	grantRequest.OperationID = "timer-turn"
	if _, err := host.host.CreateSessionBackgroundGrant(ctx, grantRequest); errorcode.CodeOf(err) != errorcode.Conflict {
		t.Fatalf("prompt/grant collision = %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(storeDir, "control", "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var legacy int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='app_operations'`).Scan(&legacy); err != nil || legacy != 0 {
		t.Fatalf("legacy operation writes = %d, %v", legacy, err)
	}
}
