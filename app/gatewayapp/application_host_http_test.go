package gatewayapp_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
)

// applicationHTTPModel is an in-process provider transport, not a Control HTTP
// substitute: all Session work passes through the real Host, provider adapter,
// Runtime, and public HTTP client. It returns a callback for each user turn and
// a terminal answer only after the native tool result enters the model context.
type applicationHTTPModel struct {
	mu       sync.Mutex
	requests [][]byte
}

func (m *applicationHTTPModel) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.requests = append(m.requests, append([]byte(nil), raw...))
	m.mu.Unlock()
	var decoded struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	last := ""
	if len(decoded.Messages) > 0 {
		last = decoded.Messages[len(decoded.Messages)-1].Role
	}
	message := map[string]any{"role": "assistant", "content": "APPLICATION_FINAL_SENTINEL"}
	finish := "stop"
	if last != "tool" {
		message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
			"id": "provider-reused-id", "index": 0, "type": "function", "function": map[string]any{
				"name": "ApplicationLookup", "arguments": `{"key":"one","execution":{"session_id":"forged-session","turn_id":"forged-turn","item_id":"forged-item"}}`,
			},
		}}}
		finish = "tool_calls"
	}
	payload, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": message, "finish_reason": finish}}})
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader("data: " + string(payload) + "\n\ndata: [DONE]\n\n")), Request: req}, nil
}

func (m *applicationHTTPModel) requestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func (m *applicationHTTPModel) requestAt(index int) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if index < 0 || index >= len(m.requests) {
		return nil
	}
	return append([]byte(nil), m.requests[index]...)
}

func applicationHTTPPrefix(t *testing.T, raw []byte) (any, any) {
	t.Helper()
	var request struct {
		Messages []map[string]any `json:"messages"`
		Tools    any              `json:"tools"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	var system []map[string]any
	for _, message := range request.Messages {
		if message["role"] == "system" {
			system = append(system, message)
		}
	}
	if len(system) == 0 || request.Tools == nil {
		t.Fatal("controlled provider request lacks explicit system/tool prefix")
	}
	return system, request.Tools
}

func (m *applicationHTTPModel) snapshotFrom(first int) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result strings.Builder
	for _, request := range m.requests[first:] {
		result.Write(request)
		result.WriteByte('\n')
	}
	return result.String()
}

type applicationHTTPHost struct {
	stack  *gatewayapp.Stack
	server *httptest.Server
	host   *httpclient.Client
	app    func(string) *httpclient.Client
}

func startApplicationHTTPHost(t *testing.T, store, workspace string, provider *applicationHTTPModel) *applicationHTTPHost {
	t.Helper()
	stack, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName: "caelis-test", UserID: "owner", StoreDir: store, WorkspaceCWD: workspace,
		SkillDirs: []string{}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
		ResolveProviderHTTPClient: func(context.Context, gatewayapp.ModelConfig) (*http.Client, error) {
			return &http.Client{Transport: provider}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	services, err := local.NewAppServer(stack)
	if err != nil {
		_ = stack.Close()
		t.Fatal(err)
	}
	const hostToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	auth, err := controlserver.BearerTokenAuthenticator(hostToken, appserver.Principal{ID: "owner"})
	if err != nil {
		_ = stack.Close()
		t.Fatal(err)
	}
	handler, err := controlserver.Handler(controlserver.Dependencies{Services: services.Services}, controlserver.Config{
		Authenticator: auth, AllowedHosts: []string{"127.0.0.1"},
	})
	if err != nil {
		_ = stack.Close()
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	newClient := func(token string) *httpclient.Client {
		t.Helper()
		client, err := httpclient.New(httpclient.Config{
			BaseURL: server.URL, BearerToken: token, HTTPClient: server.Client(), Compatibility: appserver.CurrentCompatibility(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	return &applicationHTTPHost{stack: stack, server: server, host: newClient(hostToken), app: newClient}
}

func (h *applicationHTTPHost) close(t *testing.T) {
	t.Helper()
	h.server.Close()
	if err := h.stack.Close(); err != nil {
		t.Fatal(err)
	}
}

func registerApplicationHTTP(t *testing.T, ctx context.Context, host *applicationHTTPHost, name, credentialPath string) (*httpclient.Client, application.Connection) {
	t.Helper()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	credential := "app-client-" + hex.EncodeToString(secret)
	// The embedding persists its credential before enrollment; only its hash is
	// Host state. A fresh client reads these same private bytes after replacement.
	if err := os.WriteFile(credentialPath, []byte(credential), 0o600); err != nil {
		t.Fatal(err)
	}
	connection, err := host.host.RegisterApplication(ctx, application.Registration{
		OperationID: "enroll-" + name, Name: name, Credential: credential,
	})
	if err != nil || connection.ApplicationID == "" || connection.ConnectionID == "" {
		t.Fatalf("RegisterApplication(%s) = %#v, %v", name, connection, err)
	}
	client := host.app(credential)
	current, err := client.ApplicationConnection(ctx)
	if err != nil || current.ApplicationID != connection.ApplicationID || current.ConnectionID != connection.ConnectionID {
		t.Fatalf("ApplicationConnection(%s) = %#v, %v; want %#v", name, current, err, connection)
	}
	return client, connection
}

func applicationHTTPProfile() application.Profile {
	return application.Profile{
		Version: "lookup/1", Instructions: "APPLICATION_INSTRUCTION_SENTINEL: use only ApplicationLookup.",
		Model: "openai-compatible/gpt-4.1", ToolsVersion: "lookup-tools/1", Execution: "tools-only",
		Tools: []application.ToolDefinition{{
			Name: "ApplicationLookup", Description: "Return one application-owned lookup value.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}}},
		}},
	}
}

func createApplicationHTTPSession(t *testing.T, ctx context.Context, client *httpclient.Client, id string) string {
	t.Helper()
	result, err := client.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{
		WriteBase: appserver.WriteBase{OperationID: "create-" + id}, Profile: applicationHTTPProfile(),
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.SessionID == "" {
		t.Fatalf("CreateApplicationSession(%s) = %#v, %v", id, result, err)
	}
	return result.SessionID
}

func waitApplicationHTTPIdle(t *testing.T, ctx context.Context, client *httpclient.Client, sessionID string) {
	t.Helper()
	ticker := time.NewTicker(15 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: sessionID})
		if err != nil {
			t.Fatal(err)
		}
		if !state.Run.Active {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for Session %s completion: %v", sessionID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func applicationHTTPCanonicalIDs(history []eventstream.Envelope) map[string]bool {
	ids := make(map[string]bool)
	for _, envelope := range history {
		if envelope.EventID != "" && envelope.Delivery != nil && envelope.Delivery.Mode == eventstream.DeliveryCanonical {
			ids[envelope.EventID] = true
		}
	}
	return ids
}

func applicationHTTPHistory(t *testing.T, ctx context.Context, client *httpclient.Client, sessionID string) []eventstream.Envelope {
	t.Helper()
	feed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Subscription.Close()
	var assembler appserver.FeedDeliveryAssembler
	var history []eventstream.Envelope
	for {
		select {
		case delivery, ok := <-feed.Subscription.Deliveries():
			if !ok {
				t.Fatalf("Session %s feed closed: %v", sessionID, feed.Subscription.Err())
			}
			events, replace, err := assembler.Accept(delivery)
			if err != nil {
				t.Fatal(err)
			}
			if replace {
				history = events
			} else {
				history = append(history, events...)
			}
			if delivery.Kind == appserver.FeedDeliverySync {
				return history
			}
		case <-ctx.Done():
			t.Fatalf("Session %s feed unavailable: %v", sessionID, ctx.Err())
		}
	}
}

// TestApplicationHostHTTPA01A02A03A09A10 exercises the public application and
// native Session path, never a test-only Control executor or Store mutation.
func TestApplicationHostHTTPA01A02A03A09A10(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 70*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("WORKSPACE_INSTRUCTION_LEAK_SENTINEL"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &applicationHTTPModel{}
	store := filepath.Join(root, "store")
	host := startApplicationHTTPHost(t, store, workspace, provider)
	defer func() {
		if host != nil {
			host.close(t)
		}
	}()
	status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	connected, err := host.host.ConnectModel(ctx, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "connect-model", ExpectedRevision: &status.Configuration.Revision},
		Config:    appserver.ConnectConfig{Provider: "openai-compatible", Model: "gpt-4.1", BaseURL: "https://provider.invalid/v1", APIKey: "MODEL_CREDENTIAL_LEAK_SENTINEL"},
	})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel = %#v, %v", connected, err)
	}
	credentialA := filepath.Join(root, "first.credential")
	credentialB := filepath.Join(root, "second.credential")
	appA, connectionA := registerApplicationHTTP(t, ctx, host, "first", credentialA)
	appB, _ := registerApplicationHTTP(t, ctx, host, "second", credentialB)
	sessionA := createApplicationHTTPSession(t, ctx, appA, "first")
	sessionB := createApplicationHTTPSession(t, ctx, appB, "second")
	if sessionA == sessionB {
		t.Fatal("application Sessions share an ID")
	}
	if _, err := appB.ApplicationSession(ctx, sessionA); err == nil {
		t.Fatal("second application read first application's binding")
	}
	if _, err := appB.PromptApplication(ctx, appserver.ApplicationPromptRequest{
		PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: sessionA, OperationID: "cross-app-prompt"}, Input: "FORGED_ROUTE_SENTINEL"}, SourceKind: "user",
	}); err == nil {
		t.Fatal("second application prompted first application's Session")
	}
	forgedBody, err := json.Marshal(map[string]any{
		"operation_id": "forged-metadata-route", "session_id": sessionA, "source_kind": "user",
		"input": "FORGED_ROUTE_SENTINEL", "principal_id": connectionA.PrincipalID,
		"application_id": connectionA.ApplicationID, "connection_id": connectionA.ConnectionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	forgedRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, host.server.URL+"/api/control/v1/application/sessions/"+sessionA+"/prompt", strings.NewReader(string(forgedBody)))
	if err != nil {
		t.Fatal(err)
	}
	forgedRequest.Header.Set("Content-Type", "application/json")
	forgedRequest.Header.Set("Idempotency-Key", "forged-metadata-route")
	secondSecret, err := os.ReadFile(credentialB)
	if err != nil {
		t.Fatal(err)
	}
	forgedRequest.Header.Set("Authorization", "Bearer "+string(secondSecret))
	forgedResponse, err := host.server.Client().Do(forgedRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = forgedResponse.Body.Close()
	if forgedResponse.StatusCode < 400 {
		t.Fatalf("forged request metadata crossed application scope: HTTP %d", forgedResponse.StatusCode)
	}
	if _, err := appA.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "forbidden-ordinary-create"}}); err == nil {
		t.Fatal("application credential created an ordinary Session")
	}
	prompt := func(client *httpclient.Client, id, op string) {
		t.Helper()
		out, err := client.PromptApplication(ctx, appserver.ApplicationPromptRequest{
			PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: id, OperationID: op}, Input: "Look up one value."}, SourceKind: "user",
		})
		if err != nil || (out.Outcome != appserver.OutcomeCommitted && out.Outcome != appserver.OutcomeAccepted) {
			operation, operationErr := client.ApplicationOperation(ctx, op)
			if operation.Result != nil {
				t.Fatalf("PromptApplication(%s) = %#v, %v; operation=%#v result=%#v operationErr=%v; provider=%s", id, out, err, operation, *operation.Result, operationErr, provider.snapshotFrom(0))
			}
			t.Fatalf("PromptApplication(%s) = %#v, %v; operation=%#v operationErr=%v; provider=%s", id, out, err, operation, operationErr, provider.snapshotFrom(0))
		}
	}
	prompt(appA, sessionA, "prompt-first")
	evidence := []string{"source:user(admitted)"}
	calls, err := appA.WaitApplicationCalls(ctx, sessionA)
	if err != nil || len(calls) != 1 {
		t.Fatalf("WaitApplicationCalls(first) = %#v, %v", calls, err)
	}
	call := calls[0]
	evidence = append(evidence, "call:"+call.State)
	if call.ID == "" || call.CallID != "provider-reused-id" || call.SessionID != sessionA || call.TurnID == "" || call.ItemID == "" || call.ApplicationID != connectionA.ApplicationID || call.Source.OperationID != "prompt-first" {
		t.Fatalf("native callback context = %#v", call)
	}
	if strings.Contains(call.ItemID, "forged-item") || strings.Contains(call.SessionID, "forged-session") {
		t.Fatalf("provider arguments chose callback identity: %#v", call)
	}
	// Detaching a live UI observer while the model waits for the callback must
	// neither cancel that Turn nor revoke the tool-host connection.
	observer, err := appA.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sessionA})
	if err != nil || !observer.State.Run.Active {
		t.Fatalf("Reconnect(pending callback) = %#v, %v", observer.State, err)
	}
	if err := observer.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	stillPending, err := appA.ApplicationCall(ctx, sessionA, call.ID)
	if err != nil || stillPending.State != "pending" {
		t.Fatalf("detaching observer cancelled pending callback: %#v, %v", stillPending, err)
	}
	if _, err := appB.ClaimApplicationCall(ctx, sessionA, call.ID); err == nil {
		t.Fatal("second application claimed first application's callback")
	}
	claimed, err := appA.ClaimApplicationCall(ctx, sessionA, call.ID)
	if err != nil || claimed.State != "claimed" {
		t.Fatalf("ClaimApplicationCall = %#v, %v", claimed, err)
	}
	evidence = append(evidence, "claim:"+claimed.State)
	if _, err := appA.ClaimApplicationCall(ctx, sessionA, call.ID); err == nil {
		t.Fatal("claimed callback was granted twice")
	}
	result := application.CallResult{Outcome: "succeeded", Content: json.RawMessage(`{"value":"APPLICATION_RESULT_SENTINEL"}`)}
	if err := appA.CompleteApplicationCall(ctx, sessionA, call.ID, result); err != nil {
		t.Fatal(err)
	}
	completed, err := appA.ApplicationCall(ctx, sessionA, call.ID)
	if err != nil || completed.State != "completed" {
		t.Fatalf("ApplicationCall(after result) = %#v, %v", completed, err)
	}
	evidence = append(evidence, "result:"+completed.State)
	waitApplicationHTTPIdle(t, ctx, appA, sessionA)
	history := applicationHTTPHistory(t, ctx, appA, sessionA)
	if len(history) == 0 {
		t.Fatal("canonical reconnect had no Session history")
	}
	seenInput, seenTool, seenResult, seenTerminal := false, false, false, false
	for _, envelope := range history {
		if envelope.SessionID != "" && envelope.SessionID != sessionA {
			t.Fatalf("cross-Session canonical event: %#v", envelope)
		}
		if chunk, ok := envelope.Update.(eventstream.ContentChunk); ok && chunk.SessionUpdate == eventstream.UpdateUserMessage && envelope.Actor == "user" {
			seenInput = true
		}
		if envelope.Lifecycle != nil && envelope.Lifecycle.State == eventstream.LifecycleStateCompleted {
			seenTerminal = true
		}
		switch update := envelope.Update.(type) {
		case eventstream.ToolCall:
			if update.ToolCallID == call.CallID {
				if envelope.TurnID != call.TurnID {
					t.Fatalf("callback TurnID %q disagrees with canonical tool event TurnID %q", call.TurnID, envelope.TurnID)
				}
				seenTool = true
			}
		case eventstream.ToolCallUpdate:
			if update.ToolCallID == call.CallID && update.Status != nil && *update.Status == "completed" {
				if envelope.TurnID != call.TurnID {
					t.Fatalf("callback TurnID %q disagrees with canonical result TurnID %q", call.TurnID, envelope.TurnID)
				}
				seenResult = true
			}
		}
	}
	if !seenInput || !seenTool || !seenResult || !seenTerminal {
		t.Fatalf("canonical reconnect lacks source/tool/result/terminal: events=%d source=%v call=%v result=%v terminal=%v", len(history), seenInput, seenTool, seenResult, seenTerminal)
	}
	evidence = append(evidence, "native:completed", "reconnect:canonical")
	t.Logf("application HTTP sequence: %s", strings.Join(evidence, " -> "))
	// Upload and read immutable bytes through the same public scope. An upload
	// with a mismatched digest or another application's credential is rejected.
	data := []byte("RESOURCE_SNAPSHOT_SENTINEL")
	digest := sha256.Sum256(data)
	resource, err := appA.UploadApplicationResource(ctx, appserver.ApplicationResourceRequest{
		WriteBase: appserver.WriteBase{OperationID: "upload-first", SessionID: sessionA}, Name: "data.txt", MediaType: "text/plain", Data: data, SHA256: hex.EncodeToString(digest[:]),
	})
	if err != nil || resource.SessionID != sessionA || resource.Size != int64(len(data)) {
		t.Fatalf("UploadApplicationResource = %#v, %v", resource, err)
	}
	content, err := appA.ReadApplicationResource(ctx, sessionA, resource.ID)
	if err != nil || string(content.Data) != string(data) || content.Resource.SHA256 != resource.SHA256 {
		t.Fatalf("ReadApplicationResource = %#v, %v", content, err)
	}
	if _, err := appB.ReadApplicationResource(ctx, sessionA, resource.ID); err == nil {
		t.Fatal("second application read first application's resource")
	}
	if _, err := appA.UploadApplicationResource(ctx, appserver.ApplicationResourceRequest{
		WriteBase: appserver.WriteBase{OperationID: "bad-digest", SessionID: sessionA}, Name: "bad.txt", MediaType: "text/plain", Data: data, SHA256: strings.Repeat("0", 64),
	}); err == nil {
		t.Fatal("resource upload accepted forged digest")
	}
	before := provider.snapshotFrom(0)
	beforeCount := provider.requestCount()
	beforeSystem, beforeTools := applicationHTTPPrefix(t, provider.requestAt(0))
	for _, sentinel := range []string{"APPLICATION_INSTRUCTION_SENTINEL", "APPLICATION_RESULT_SENTINEL"} {
		if !strings.Contains(before, sentinel) {
			t.Fatalf("provider request missing %s: %s", sentinel, before)
		}
	}
	for _, sentinel := range []string{"WORKSPACE_INSTRUCTION_LEAK_SENTINEL", "MODEL_CREDENTIAL_LEAK_SENTINEL", "FORGED_ROUTE_SENTINEL"} {
		if strings.Contains(before, sentinel) {
			t.Fatalf("provider request leaked %s: %s", sentinel, before)
		}
	}
	// Reopen actual Host and Store, not only an in-memory client. The original
	// credential, immutable profile, canonical history and byte snapshot survive.
	host.close(t)
	host = startApplicationHTTPHost(t, store, workspace, provider)
	firstSecret, err := os.ReadFile(credentialA)
	if err != nil {
		t.Fatal(err)
	}
	appA = host.app(string(firstSecret))
	binding, err := appA.ApplicationSession(ctx, sessionA)
	if err != nil || binding.Profile.Instructions != applicationHTTPProfile().Instructions || binding.ConnectionID != connectionA.ConnectionID {
		t.Fatalf("reopened binding = %#v, %v", binding, err)
	}
	reopenedHistory := applicationHTTPHistory(t, ctx, appA, sessionA)
	beforeIDs, afterIDs := applicationHTTPCanonicalIDs(history), applicationHTTPCanonicalIDs(reopenedHistory)
	if len(beforeIDs) < 3 {
		t.Fatalf("canonical prefix unexpectedly short before replacement: %d events", len(beforeIDs))
	}
	for id := range beforeIDs {
		if !afterIDs[id] {
			t.Fatalf("canonical Session event %s disappeared across Host replacement", id)
		}
	}
	content, err = appA.ReadApplicationResource(ctx, sessionA, resource.ID)
	if err != nil || string(content.Data) != string(data) {
		t.Fatalf("reopened resource = %#v, %v", content, err)
	}
	prompt(appA, sessionA, "prompt-after-restart")
	calls, err = appA.WaitApplicationCalls(ctx, sessionA)
	if err != nil || len(calls) != 1 {
		t.Fatalf("WaitApplicationCalls(after restart) = %#v, %v", calls, err)
	}
	if calls[0].ID == call.ID || calls[0].CallID != call.CallID || calls[0].TurnID == call.TurnID || calls[0].ItemID == "" {
		t.Fatalf("provider ID reuse collapsed canonical callback: original=%#v after=%#v", call, calls[0])
	}
	if _, err := appA.ClaimApplicationCall(ctx, sessionA, calls[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := appA.CompleteApplicationCall(ctx, sessionA, calls[0].ID, result); err != nil {
		t.Fatal(err)
	}
	waitApplicationHTTPIdle(t, ctx, appA, sessionA)
	after := provider.snapshotFrom(beforeCount)
	if after == "" || !strings.Contains(after, "APPLICATION_INSTRUCTION_SENTINEL") || !strings.Contains(after, "APPLICATION_RESULT_SENTINEL") || strings.Contains(after, "WORKSPACE_INSTRUCTION_LEAK_SENTINEL") || strings.Contains(after, "MODEL_CREDENTIAL_LEAK_SENTINEL") {
		t.Fatalf("reopened provider context lost prefix or leaked workspace/credential: %s", after)
	}
	afterSystem, afterTools := applicationHTTPPrefix(t, provider.requestAt(beforeCount))
	if !reflect.DeepEqual(beforeSystem, afterSystem) || !reflect.DeepEqual(beforeTools, afterTools) {
		t.Fatal("immutable application system/tool prefix changed across Host replacement")
	}

	// Application-authored summary is admitted as AgentCommunication evidence,
	// never a second user instruction. Confirm the canonical HTTP replay source.
	summary, err := appA.PromptApplication(ctx, appserver.ApplicationPromptRequest{
		PromptRequest: appserver.PromptRequest{
			WriteBase: appserver.WriteBase{SessionID: sessionA, OperationID: "summary-after-restart"},
			Input:     "APPLICATION_SUMMARY_EVIDENCE_SENTINEL",
		},
		SourceKind: "application_summary",
	})
	if err != nil || (summary.Outcome != appserver.OutcomeAccepted && summary.Outcome != appserver.OutcomeCommitted) {
		t.Fatalf("PromptApplication(summary) = %#v, %v", summary, err)
	}
	summaryCalls, err := appA.WaitApplicationCalls(ctx, sessionA)
	if err != nil || len(summaryCalls) != 1 || summaryCalls[0].Source.Kind != "application_summary" {
		t.Fatalf("WaitApplicationCalls(summary) = %#v, %v", summaryCalls, err)
	}
	if _, err := appA.ClaimApplicationCall(ctx, sessionA, summaryCalls[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := appA.CompleteApplicationCall(ctx, sessionA, summaryCalls[0].ID, result); err != nil {
		t.Fatal(err)
	}
	waitApplicationHTTPIdle(t, ctx, appA, sessionA)
	seenSummary := false
	for _, envelope := range applicationHTTPHistory(t, ctx, appA, sessionA) {
		chunk, ok := envelope.Update.(eventstream.ContentChunk)
		if !ok || !strings.Contains(fmt.Sprint(chunk.Content), "APPLICATION_SUMMARY_EVIDENCE_SENTINEL") {
			continue
		}
		if chunk.SessionUpdate != eventstream.UpdateUserMessage || envelope.AgentCommunicationSource == nil ||
			envelope.AgentCommunicationSource.Kind != "system" || envelope.AgentCommunicationSource.ID != "application_summary" {
			t.Fatalf("application summary projected as user authority: kind=%s source=%#v", chunk.SessionUpdate, envelope.AgentCommunicationSource)
		}
		seenSummary = true
	}
	if !seenSummary {
		t.Fatal("reconnected canonical feed lacks application summary evidence")
	}

	archiveRequest := appserver.CloseSessionRequest{WriteBase: appserver.WriteBase{SessionID: sessionA, OperationID: "archive-first"}}
	archived, err := appA.ArchiveApplicationSession(ctx, archiveRequest)
	if err != nil || archived.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ArchiveApplicationSession = %#v, %v", archived, err)
	}
	repeatedArchive, err := appA.ArchiveApplicationSession(ctx, archiveRequest)
	if err != nil || repeatedArchive.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("repeated archive = %#v, %v", repeatedArchive, err)
	}
	binding, err = appA.ApplicationSession(ctx, sessionA)
	if err != nil || !binding.Archived {
		t.Fatalf("archived binding = %#v, %v", binding, err)
	}
	if _, err := appA.PromptApplication(ctx, appserver.ApplicationPromptRequest{
		PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: sessionA, OperationID: "after-archive"}, Input: "must not run"}, SourceKind: "user",
	}); err == nil {
		t.Fatal("archived Session admitted another prompt")
	}
	archivedIDs := applicationHTTPCanonicalIDs(applicationHTTPHistory(t, ctx, appA, sessionA))
	for id := range beforeIDs {
		if !archivedIDs[id] {
			t.Fatalf("archive lost canonical event %s", id)
		}
	}
	content, err = appA.ReadApplicationResource(ctx, sessionA, resource.ID)
	if err != nil || string(content.Data) != string(data) {
		t.Fatalf("archive lost immutable resource = %#v, %v", content, err)
	}
}
