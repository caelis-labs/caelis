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

	sessionapi "github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

// reviewerHTTPProvider is a deterministic real-provider-adapter transport. Main
// requests emit one configured tool call and then answer on the tool result;
// reviewer requests use a distinct configured model and emit only a decision.
// toolName and args select the native tool or Code Mode invocation.
type reviewerHTTPProvider struct {
	mu        sync.Mutex
	requests  []map[string]any
	toolName  string
	args      string
	decision  string
	entered   chan struct{}
	release   chan struct{}
	once      sync.Once
	toolReady <-chan struct{}
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
	if body["model"] == "reviewer-model" || body["model"] == "reviewer-next" {
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
		return reviewerHTTPStream(req, map[string]any{"role": "assistant", "content": "REVIEWED_TOOL_FINAL"}, "stop")
	}
	if p.toolReady != nil {
		select {
		case <-p.toolReady:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
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
			Config:    appserver.ConnectConfig{Provider: "custom-chat", Model: model, BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY"},
		})
		if err != nil || connected.Outcome != appserver.OutcomeCommitted {
			t.Fatalf("connect %s: %+v %v", model, connected, err)
		}
	}
	catalog, err := host.host.AgentBindingStatus(ctx, appserver.AgentRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var guardian agentbinding.Binding
	var guardianModel string
	for _, target := range catalog.Targets {
		if strings.Contains(target.ID, "reviewer-model") {
			guardian = agentbinding.Binding{Handle: agentbinding.HandleGuardian, ProfileID: target.ID, Effort: target.Effort.DefaultEffort}
			guardianModel = target.Backend.Provider.ModelConfigID
		}
	}
	status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := host.host.BindAgentBinding(ctx, appserver.BindAgentBindingRequest{
		WriteBase: appserver.WriteBase{OperationID: "bind-guardian", ExpectedRevision: &status.Configuration.Revision}, Binding: guardian,
	})
	if err != nil || bound.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("bind Guardian: %+v %v", bound, err)
	}
	client, _ := connectSessionOwnerHTTP(t, ctx, host, "owner", filepath.Join(root, "application.credential"))
	profile := applicationHTTPProfile()
	profile.Permissions.ApprovalMode = "auto-review"
	profile.Reviewer = &sessionprofile.Reviewer{Kind: "guardian", Model: "retired-mimo"}
	if provider.toolReady != nil {
		profile.MCPServers = []sessionprofile.MCPServer{reviewerFixtureMCP(root)}
	}
	created, err := client.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "create-reviewed"}, Profile: profilePointer(profile)})
	if err != nil || created.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("create: %+v %v", created, err)
	}
	state, err := client.SessionReviewerState(ctx, created.SessionID)
	if err != nil || state.Status != "ready" || state.ApprovalMode != "auto-review" || state.Reviewer == nil || state.Reviewer.Model != guardianModel {
		t.Fatalf("reviewer readiness: %+v %v", state, err)
	}
	return host, client, created.SessionID
}

func reviewerFixtureMCP(root string) sessionprofile.MCPServer {
	return sessionprofile.MCPServer{Name: "documents", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=^TestAtomicDocumentsMCPHelper$"}, WorkDir: root}
}

const reviewerMCPArgs = `{"code":"return await tools.call('documents__lookup', {key: 'one'});"}`

func promptReviewerHTTP(t *testing.T, ctx context.Context, client *httpclient.Client, session, operation string) appserver.CommandResult {
	t.Helper()
	result, err := client.Prompt(ctx, appserver.PromptRequest{
		WriteBase: appserver.WriteBase{SessionID: session, OperationID: operation}, Input: "Please look up key one.", SourceKind: "user",
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
		if env.Kind != eventstream.KindApprovalReview || env.ApprovalReview == nil || env.ApprovalReview.ToolName != "documents__lookup" {
			continue
		}
		if env.Delivery == nil || env.Delivery.Mode != eventstream.DeliveryMirror {
			continue // Live progress can also be transient; replay must be mirrored.
		}
		if env.ApprovalReview.RawInput["key"] != "one" || env.TurnID == "" || env.EventID == "" {
			t.Fatalf("review mirror missing exact input/identity: review=%+v delivery=%+v event=%+v", env.ApprovalReview, env.Delivery, env)
		}
		if env.ApprovalReview.Status == status {
			matching = append(matching, env)
		}
	}
	return matching
}

func reviewerHTTPFacts(t *testing.T, ctx context.Context, host *applicationHTTPHost, sessionID string) []*sessionapi.Event {
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

// reviewerHTTPOccurrence reads the Runtime's accepted ToolCall occurrence,
// not a provider tool_call_id (which can be reused on later Turns).
func reviewerHTTPOccurrence(t *testing.T, ctx context.Context, host *applicationHTTPHost, sessionID, turnID, toolName string) string {
	t.Helper()
	step := ""
	events := reviewerHTTPFacts(t, ctx, host, sessionID)
	for _, event := range events {
		if event.Type != sessionapi.EventTypeToolCall || event.Tool == nil {
			continue
		}
		execution := event.Tool
		key := execution.Execution
		if key.TurnID != turnID || execution.Name != toolName || execution.ID != "reviewed-provider-tool-id" {
			continue
		}
		if key.SessionID != sessionID || key.ItemID == "" || (step != "" && step != key.ItemID) {
			t.Fatalf("invalid tool occurrence identity: %+v; prior step=%q", key, step)
		}
		step = key.ItemID
	}
	if step == "" {
		t.Fatalf("no accepted ToolCall for Session %s Turn %s tool %s", sessionID, turnID, toolName)
	}
	return step
}

func reviewerHTTPApprovalItem(t *testing.T, ctx context.Context, host *applicationHTTPHost, sessionID, turnID, toolName string, status sessionapi.ApprovalRecordStatus) string {
	t.Helper()
	item := ""
	for _, event := range reviewerHTTPFacts(t, ctx, host, sessionID) {
		if event.Type != sessionapi.EventTypeLifecycle || event.Approval == nil {
			continue
		}
		token := event.Approval
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
