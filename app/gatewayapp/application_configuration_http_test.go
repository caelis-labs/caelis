package gatewayapp_test

import (
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

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/control/modelprofile/builder"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

// revisionProvider exercises the official OpenAI Responses adapter through a
// deterministic transport. The first HTTP request blocks after admission but
// before its response, so configuration can change while the old request lives.
type revisionProvider struct {
	mu       sync.Mutex
	requests []map[string]any
	first    chan struct{}
	release  chan struct{}
}

func newRevisionProvider() *revisionProvider {
	return &revisionProvider{first: make(chan struct{}), release: make(chan struct{})}
}

func (p *revisionProvider) RoundTrip(req *http.Request) (*http.Response, error) {
	defer func() { _ = req.Body.Close() }()
	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.requests = append(p.requests, body)
	index := len(p.requests)
	p.mu.Unlock()
	if index == 1 {
		close(p.first)
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-p.release:
		}
	}
	input, _ := body["input"].([]any)
	lastOutput := false
	if len(input) > 0 {
		last, _ := input[len(input)-1].(map[string]any)
		lastOutput = last["type"] == "function_call_output"
	}
	var events []map[string]any
	if lastOutput {
		events = []map[string]any{
			{"type": "response.output_text.delta", "item_id": "message-1", "output_index": 0, "delta": "CONFIGURATION_FINAL_SENTINEL"},
			{"type": "response.completed", "response": map[string]any{"model": body["model"], "status": "completed", "output": []any{map[string]any{"id": "message-1", "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "CONFIGURATION_FINAL_SENTINEL"}}}}}},
		}
	} else {
		arguments := `{"key":"old"}`
		if body["model"] == "gpt-5.4" {
			arguments = `{"key":2}`
		}
		events = []map[string]any{
			{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": "provider-tool-item", "type": "function_call", "call_id": "provider-reused-id", "name": "ApplicationLookup"}},
			{"type": "response.function_call_arguments.delta", "item_id": "provider-tool-item", "output_index": 0, "delta": arguments},
			{"type": "response.completed", "response": map[string]any{"model": body["model"], "status": "completed", "output": []any{map[string]any{"id": "provider-tool-item", "type": "function_call", "call_id": "provider-reused-id", "name": "ApplicationLookup", "arguments": arguments}}}},
		}
	}
	var stream strings.Builder
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		stream.WriteString("data: ")
		stream.Write(encoded)
		stream.WriteString("\n\n")
	}
	stream.WriteString("data: [DONE]\n\n")
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream.String())), Request: req}, nil
}

func (p *revisionProvider) snapshot() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]map[string]any, len(p.requests))
	for i, body := range p.requests {
		encoded, _ := json.Marshal(body)
		_ = json.Unmarshal(encoded, &result[i])
	}
	return result
}

func assertRevisionPrefix(t *testing.T, body map[string]any, model, effort, tier, instructions, schemaType string) {
	t.Helper()
	if body["model"] != model || body["instructions"] != instructions {
		t.Fatalf("wrong model/instructions: %#v", body)
	}
	reasoning, _ := body["reasoning"].(map[string]any)
	if reasoning["effort"] != effort {
		t.Fatalf("reasoning = %#v, want %s", reasoning, effort)
	}
	if body["service_tier"] != tier {
		if tier != "" || body["service_tier"] != nil {
			t.Fatalf("service_tier = %#v, want %s", body["service_tier"], tier)
		}
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 7 {
		t.Fatalf("tools = %#v", body["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	parameters, _ := tool["parameters"].(map[string]any)
	properties, _ := parameters["properties"].(map[string]any)
	key, _ := properties["key"].(map[string]any)
	if tool["name"] != "ApplicationLookup" || key["type"] != schemaType {
		t.Fatalf("callback schema = %#v, want key %s", tools, schemaType)
	}
}

func connectRevisionHTTPModel(t *testing.T, ctx context.Context, host *applicationHTTPHost, model string) {
	t.Helper()
	status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := host.host.ConnectModel(ctx, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "connect-" + model, ExpectedRevision: &status.Configuration.Revision},
		Config:    appserver.ConnectConfig{Provider: "custom-responses", BaseURL: "https://provider.invalid/v1", Model: model, APIKey: "SYNTHETIC_ONLY_CREDENTIAL", ReasoningLevels: []string{"low", "medium", "high"}, ReasoningEffort: "medium"},
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel(%s) = %+v %v", model, result, err)
	}
}

// Saved official endpoints keep service-tier and effort coverage independently
// of onboarding presets. The new-connection tests use Custom Responses instead.
func startApplicationHTTPHostWithSavedModels(t *testing.T, storeDir, workspace string, provider http.RoundTripper) *applicationHTTPHost {
	t.Helper()
	if _, err := os.Stat(filepath.Join(storeDir, "config.json")); os.IsNotExist(err) {
		doc := configstore.AppConfig{SchemaVersion: configstore.SchemaVersionV2}
		for _, name := range []string{"gpt-5.4-mini", "gpt-5.4", "gpt-4.1"} {
			cfg := modelconfig.Config{Provider: "openai", Model: name, BaseURL: "https://api.openai.com/v1", AuthType: "none", ContextWindowTokens: 272000}
			if name != "gpt-4.1" {
				cfg.ReasoningLevels, cfg.ReasoningEffort = []string{"low", "medium", "high"}, "medium"
			}
			profile, err := builder.FromProvider(cfg)
			if err != nil {
				t.Fatal(err)
			}
			doc.Models.Configs = append(doc.Models.Configs, cfg)
			doc.ModelProfiles, err = modelprofile.Upsert(doc.ModelProfiles, profile)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := configstore.New(storeDir).Save(doc); err != nil {
			t.Fatal(err)
		}
	}
	return startApplicationHTTPHost(t, storeDir, workspace, provider)
}

func promptRevisionHTTP(t *testing.T, ctx context.Context, client *httpclient.Client, session, operation string) {
	t.Helper()
	result, err := client.Prompt(ctx, appserver.PromptRequest{
		WriteBase: appserver.WriteBase{SessionID: session, OperationID: operation}, Input: "CONFIGURATION_USER_SENTINEL", SourceKind: "user",
	})
	if err != nil || (result.Outcome != appserver.OutcomeAccepted && result.Outcome != appserver.OutcomeCommitted) {
		t.Fatalf("PromptApplication(%s) = %+v %v", operation, result, err)
	}
}

func claimAndCompleteRevisionCall(t *testing.T, ctx context.Context, client *httpclient.Client, session string, revision uint64, version, args string) sessionprofile.Call {
	t.Helper()
	calls, err := client.WaitSessionToolCalls(ctx, session)
	if err != nil || len(calls) != 1 {
		t.Fatalf("WaitApplicationCalls: %+v %v", calls, err)
	}
	call := calls[0]
	if call.ConfigurationRevision != revision || call.ToolsVersion != version || string(call.Arguments) != args || call.State != "pending" {
		t.Fatalf("wrong pinned callback %+v, want revision %d version %s args %s", call, revision, version, args)
	}
	if _, err = client.ClaimSessionToolCall(ctx, session, call.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = client.ClaimSessionToolCall(ctx, session, call.ID); err == nil {
		t.Fatal("repeated claim granted an external effect")
	}
	if err = client.CompleteSessionToolCall(ctx, session, call.ID, sessionprofile.CallResult{Outcome: "succeeded", Content: json.RawMessage(`{"value":"CONFIGURATION_TOOL_SENTINEL"}`)}); err != nil {
		t.Fatal(err)
	}
	return call
}

// droppedConfigurationResponse simulates the Host committing a configuration
// update while its HTTP response is lost. The caller must query its operation.
type droppedConfigurationResponse struct {
	transport http.RoundTripper
	operation string
}

func (d droppedConfigurationResponse) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := d.transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/configuration") && req.Header.Get("Idempotency-Key") == d.operation {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		return nil, io.ErrUnexpectedEOF
	}
	return response, nil
}

func TestApplicationConfigurationHostHTTPHotRequestsAndRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(root, "store")
	provider := newRevisionProvider()
	host := startApplicationHTTPHostWithSavedModels(t, store, workspace, provider)
	defer func() {
		select {
		case <-provider.release:
		default:
			close(provider.release)
		}
		if host != nil {
			host.close(t)
		}
	}()
	client, _ := connectSessionOwnerHTTP(t, ctx, host, "owner", filepath.Join(root, "application.credential"))
	profile := applicationHTTPProfile()
	profile.Model = "openai/gpt-5.4-mini"
	profile.Instructions = "OLD_CONFIGURATION_INSTRUCTIONS"
	profile.ReasoningEffort = "low"
	profile.Tools[0].InputSchema = map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}}, "required": []any{"key"}}
	created, err := client.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "create-config"}, Profile: profilePointer(profile)})
	if err != nil || created.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("create = %+v %v", created, err)
	}
	session := created.SessionID
	baseline, err := client.SessionConfiguration(ctx, session)
	if err != nil || baseline.Revision != 1 || !reflect.DeepEqual(baseline.Profile, profile) {
		t.Fatalf("baseline %+v %v", baseline, err)
	}
	promptRevisionHTTP(t, ctx, client, session, "prompt-hot")
	select {
	case <-provider.first:
	case <-ctx.Done():
		t.Fatal("old provider request did not reach deterministic barrier")
	}
	issued, err := client.SessionConfiguration(ctx, session)
	if err != nil || issued.LastRequest == nil || issued.LastRequest.Revision != 1 || issued.LastRequest.RequestID == "" || issued.LastRequest.TurnID == "" || issued.LastRequest.Model != "gpt-5.4-mini" || issued.LastRequest.ReasoningEffort != "low" || issued.LastRequest.ServiceTier != "" || issued.LastRequest.ToolsVersion != profile.ToolsVersion {
		t.Fatalf("old request was not admitted before the provider barrier: %+v %v", issued, err)
	}
	newDefs := []sessionprofile.ToolDefinition{{Name: "ApplicationLookup", Description: "Look up integer key.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "integer"}}, "required": []any{"key"}}}}
	newModel := "openai/gpt-5.4"
	newInstructions := "NEW_CONFIGURATION_INSTRUCTIONS"
	newEffort := "high"
	newTier := "priority"
	newVersion := "lookup-tools/2"
	update := sessionprofile.UpdateConfigurationRequest{OperationID: "hot-update", ExpectedConfigurationRevision: 1,
		Patch: sessionprofile.ConfigurationPatch{Model: &newModel, Instructions: &newInstructions, ReasoningEffort: &newEffort, ServiceTier: &newTier, ToolsVersion: &newVersion, Tools: &newDefs}}
	updated, err := configurationResult(client.UpdateSessionConfiguration(ctx, configurationCommand(session, update)))
	if err != nil || updated.Revision != 2 || updated.LastRequest == nil || updated.LastRequest.Revision != 1 {
		t.Fatalf("hot update did not overlap old request: %+v %v", updated, err)
	}
	close(provider.release)
	oldCall := claimAndCompleteRevisionCall(t, ctx, client, session, 1, profile.ToolsVersion, `{"key":"old"}`)
	waitApplicationHTTPIdle(t, ctx, client, session)
	requests := provider.snapshot()
	if len(requests) != 2 {
		t.Fatalf("old Turn produced %d model requests, want old callback then new completion", len(requests))
	}
	assertRevisionPrefix(t, requests[0], "gpt-5.4-mini", "low", "", profile.Instructions, "string")
	assertRevisionPrefix(t, requests[1], "gpt-5.4", "high", "priority", newInstructions, "integer")
	if inputs, _ := requests[1]["input"].([]any); len(inputs) == 0 || !strings.Contains(fmt.Sprint(inputs), "CONFIGURATION_TOOL_SENTINEL") {
		t.Fatalf("new request lacks old callback result as model context: %#v", requests[1]["input"])
	}
	observed, err := client.SessionConfiguration(ctx, session)
	if err != nil || observed.LastRequest == nil || observed.LastRequest.Revision != 2 || observed.LastRequest.TurnID != oldCall.TurnID || observed.LastRequest.RequestID == issued.LastRequest.RequestID || observed.LastRequest.Model != "gpt-5.4" || observed.LastRequest.ReasoningEffort != "high" || observed.LastRequest.ServiceTier != "priority" || observed.LastRequest.ToolsVersion != newVersion {
		t.Fatalf("same-Turn next model request did not pin new revision: %+v %v", observed, err)
	}
	// An unchanged patch is a durable operation, but cannot rewrite the model
	// prefix, change the revision, or dispatch a user Turn.
	stable, err := configurationResult(client.UpdateSessionConfiguration(ctx, configurationCommand(session, sessionprofile.UpdateConfigurationRequest{OperationID: "noop", ExpectedConfigurationRevision: 2})))
	if err != nil || stable.Revision != 2 || !reflect.DeepEqual(stable.Profile, updated.Profile) {
		t.Fatalf("no-op %+v %v", stable, err)
	}
	if len(provider.snapshot()) != 2 {
		t.Fatal("configuration-only update dispatched a model request")
	}
	promptRevisionHTTP(t, ctx, client, session, "prompt-after-noop")
	newCall := claimAndCompleteRevisionCall(t, ctx, client, session, 2, newVersion, `{"key":2}`)
	if newCall.ID == oldCall.ID || newCall.CallID != oldCall.CallID || newCall.TurnID == oldCall.TurnID {
		t.Fatalf("same-named callback misrouted across catalog versions: old=%+v new=%+v", oldCall, newCall)
	}
	waitApplicationHTTPIdle(t, ctx, client, session)
	requests = provider.snapshot()
	if len(requests) != 4 {
		t.Fatalf("two Turns produced %d requests, want four", len(requests))
	}
	assertRevisionPrefix(t, requests[2], "gpt-5.4", "high", "priority", newInstructions, "integer")
	assertRevisionPrefix(t, requests[3], "gpt-5.4", "high", "priority", newInstructions, "integer")
	history := applicationHTTPHistory(t, ctx, client, session)
	assertRevisionUserHistory(t, history, 2)
	for _, bad := range []sessionprofile.UpdateConfigurationRequest{
		{OperationID: "stale", ExpectedConfigurationRevision: 1},
		{OperationID: "invalid-model", ExpectedConfigurationRevision: 2, Patch: sessionprofile.ConfigurationPatch{Model: stringPointer("")}},
		{OperationID: "invalid-tier", ExpectedConfigurationRevision: 2, Patch: sessionprofile.ConfigurationPatch{ServiceTier: stringPointer("unsupported-tier")}},
		{OperationID: "catalog-reuse", ExpectedConfigurationRevision: 2, Patch: sessionprofile.ConfigurationPatch{ToolsVersion: stringPointer(profile.ToolsVersion)}},
	} {
		if _, err := configurationResult(client.UpdateSessionConfiguration(ctx, configurationCommand(session, bad))); err == nil {
			t.Fatalf("accepted invalid update %+v", bad)
		}
	}
	// The transport discards only the response, never retries the dispatched POST.
	// Query the operation's exact result and confirm no extra model request.
	secret, err := os.ReadFile(filepath.Join(root, "application.credential"))
	if err != nil {
		t.Fatal(err)
	}
	clientWithLoss, err := httpclient.New(httpclient.Config{BaseURL: host.server.URL, BearerToken: string(secret), Compatibility: appserver.CurrentCompatibility(), HTTPClient: &http.Client{Transport: droppedConfigurationResponse{transport: host.server.Client().Transport, operation: "lost-response"}}})
	if err != nil {
		t.Fatal(err)
	}
	clear := ""
	loss := sessionprofile.UpdateConfigurationRequest{OperationID: "lost-response", ExpectedConfigurationRevision: 2, Patch: sessionprofile.ConfigurationPatch{ReasoningEffort: &clear, ServiceTier: &clear}}
	if _, err = configurationResult(clientWithLoss.UpdateSessionConfiguration(ctx, configurationCommand(session, loss))); err == nil {
		t.Fatal("lost HTTP response was not simulated")
	}
	receipt, err := configurationResult(client.Operation(ctx, loss.OperationID))
	if err != nil || receipt.Revision != 3 || receipt.Profile.ReasoningEffort != "" || receipt.Profile.ServiceTier != "" {
		t.Fatalf("uncertain operation receipt %+v %v", receipt, err)
	}
	if common, err := client.Operation(ctx, loss.OperationID); err != nil || common.Configuration == nil || common.Configuration.Revision != receipt.Revision {
		t.Fatalf("common receipt lost configuration: %+v %v", common, err)
	}
	beforeRestart := provider.snapshot()
	if len(beforeRestart) != 4 {
		t.Fatal("lost update dispatched a model request")
	}
	host.close(t)
	host = startApplicationHTTPHost(t, store, workspace, provider)
	client = host.app(string(secret))
	recovered, err := client.SessionConfiguration(ctx, session)
	if err != nil || !reflect.DeepEqual(receipt, recovered) {
		t.Fatalf("reopened desired configuration %+v, want %+v, err=%v", recovered, receipt, err)
	}
	replayReceipt, err := configurationResult(client.Operation(ctx, loss.OperationID))
	if err != nil || !reflect.DeepEqual(replayReceipt, receipt) {
		t.Fatalf("reopened operation receipt %+v, want %+v, err=%v", replayReceipt, receipt, err)
	}
	if got := applicationHTTPHistory(t, ctx, client, session); len(applicationHTTPCanonicalIDs(got)) < len(applicationHTTPCanonicalIDs(history)) {
		t.Fatal("reopened canonical history lost previous Turn facts")
	} else {
		assertRevisionUserHistory(t, got, 2)
	}
	promptRevisionHTTP(t, ctx, client, session, "prompt-after-restart")
	claimAndCompleteRevisionCall(t, ctx, client, session, 3, newVersion, `{"key":2}`)
	waitApplicationHTTPIdle(t, ctx, client, session)
	requests = provider.snapshot()
	if len(requests) != 6 {
		t.Fatalf("restarted Turn produced %d model requests, want six", len(requests))
	}
	assertRevisionPrefix(t, requests[4], "gpt-5.4", "medium", "", newInstructions, "integer")
	last, err := client.SessionConfiguration(ctx, session)
	if err != nil || last.LastRequest == nil || last.LastRequest.Revision != 3 || last.LastRequest.Model != "gpt-5.4" || last.LastRequest.ReasoningEffort != "medium" || last.LastRequest.ServiceTier != "" || last.LastRequest.ToolsVersion != newVersion {
		t.Fatalf("defaulted request selectors after restart %+v %v", last, err)
	}
	if !strings.Contains(fmt.Sprint(requests[4]["input"]), "CONFIGURATION_TOOL_SENTINEL") {
		t.Fatal("reopened model request lost persisted callback result")
	}
	assertRevisionUserHistory(t, applicationHTTPHistory(t, ctx, client, session), 3)
	// Public JSON distinguishes an explicit empty catalog/allowlist from an
	// omitted list. Clearing callbacks names a fresh catalog version.
	emptyCallbacks := []sessionprofile.ToolDefinition{}
	emptyNative := []string{}
	clearedVersion := "lookup-tools/3"
	cleared, err := configurationResult(client.UpdateSessionConfiguration(ctx, configurationCommand(session, sessionprofile.UpdateConfigurationRequest{
		OperationID: "clear-catalog", ExpectedConfigurationRevision: 3,
		Patch: sessionprofile.ConfigurationPatch{ToolsVersion: &clearedVersion, Tools: &emptyCallbacks, NativeTools: &emptyNative},
	})))
	if err != nil || cleared.Revision != 4 || cleared.Profile.NativeTools == nil || len(cleared.Profile.Tools) != 0 || len(cleared.Profile.NativeTools) != 0 {
		t.Fatalf("public clear did not retain [] semantics: %+v %v", cleared, err)
	}
	if len(provider.snapshot()) != 6 {
		t.Fatal("clearing configuration dispatched a model request")
	}
}

func stringPointer(value string) *string { return &value }

func assertRevisionUserHistory(t *testing.T, history []eventstream.Envelope, expected int) {
	t.Helper()
	// A model request refresh must not submit an extra user input into canonical
	// history. Count Turn user chunks by distinct Turn ID rather than SSE fragments.
	turns := map[string]bool{}
	for _, envelope := range history {
		chunk, ok := envelope.Update.(eventstream.ContentChunk)
		if !ok || envelope.Actor != "user" || chunk.SessionUpdate != eventstream.UpdateUserMessage || !strings.Contains(fmt.Sprint(chunk.Content), "CONFIGURATION_USER_SENTINEL") {
			continue
		}
		turns[envelope.TurnID] = true
	}
	if len(turns) != expected {
		t.Fatalf("canonical user Turns = %d, want %d; events=%d", len(turns), expected, len(history))
	}
}
