package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	appgateway "github.com/OnslaughtSnail/caelis/gateway"
	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	modelproviders "github.com/OnslaughtSnail/caelis/sdk/model/providers"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	"github.com/OnslaughtSnail/caelis/sdk/session/inmemory"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
)

func TestApprovalReviewerUsesRequestModelAndSessionContext(t *testing.T) {
	ctx := context.Background()
	service, session := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(t, ctx, service, session, sdksession.EventTypeUser, sdkmodel.RoleUser, "Please push the current changes after the focused tests pass.")
	model := &approvalReviewerFakeModel{
		responses: []string{`{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"narrow request"}`},
	}
	reviewer := newModelApprovalReviewer(service)

	result, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(session, model, "git push origin dev", map[string]any{
		"cmd":        "git push origin dev",
		"call_id":    "call-123",
		"session_id": "session-123",
		"valid":      true,
	}))
	if err != nil {
		t.Fatalf("ReviewApproval() error = %v", err)
	}
	if !result.Approved {
		t.Fatal("Approved = false, want true")
	}
	if !strings.Contains(result.DisplayText, "narrow request") {
		t.Fatalf("DisplayText = %q, want rationale", result.DisplayText)
	}

	requests := model.Requests()
	if got, want := len(requests), 1; got != want {
		t.Fatalf("model calls = %d, want %d", got, want)
	}
	req := requests[0]
	if req.Stream {
		t.Fatal("model request Stream = true, want false")
	}
	if got := len(req.Tools); got != 0 {
		t.Fatalf("len(Tools) = %d, want no reviewer tools", got)
	}
	if req.Output == nil || req.Output.Mode != sdkmodel.OutputModeSchema {
		t.Fatalf("Output = %#v, want schema output", req.Output)
	}
	if got := len(req.Instructions); got != 1 {
		t.Fatalf("len(Instructions) = %d, want guardian policy", got)
	}
	if !strings.Contains(req.Instructions[0].Text.Text, "You are judging one planned coding-agent action") {
		t.Fatalf("instruction text = %q, want guardian policy", req.Instructions[0].Text.Text)
	}
	prompt := req.Messages[0].TextContent()
	for _, want := range []string{
		">>> TRANSCRIPT START",
		"Please push the current changes",
		"git push origin dev",
		`"valid": true`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("review prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, forbidden := range []string{"call-123", "session-123", "call_id", "session_id", "review_id", "turn_id"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("review prompt contains id-like field %q:\n%s", forbidden, prompt)
		}
	}

	events, err := service.Events(ctx, sdksession.EventsRequest{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if got, want := len(events), 1; got != want {
		t.Fatalf("parent session event count = %d, want %d", got, want)
	}
}

func TestApprovalReviewerReusesStablePrefixAndSendsTranscriptDelta(t *testing.T) {
	ctx := context.Background()
	service, session := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(t, ctx, service, session, sdksession.EventTypeUser, sdkmodel.RoleUser, "Please commit and push the prepared fix.")
	model := &approvalReviewerFakeModel{responses: []string{
		`{"outcome":"allow","risk_level":"medium","user_authorization":"high","rationale":"commit is user requested"}`,
		`{"outcome":"allow","risk_level":"medium","user_authorization":"high","rationale":"push is user requested"}`,
	}}
	reviewer := newModelApprovalReviewer(service)

	first, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(session, model, "git commit -m fix", map[string]any{"cmd": "git commit -m fix"}))
	if err != nil {
		t.Fatalf("first ReviewApproval() error = %v", err)
	}
	if !first.Approved {
		t.Fatalf("first Approved = false, want true: %#v", first)
	}
	appendApprovalReviewerTextEvent(t, ctx, service, session, sdksession.EventTypeAssistant, sdkmodel.RoleAssistant, "Focused tests passed; next I will push the branch.")
	second, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(session, model, "git push origin dev", map[string]any{"cmd": "git push origin dev"}))
	if err != nil {
		t.Fatalf("second ReviewApproval() error = %v", err)
	}
	if !second.Approved {
		t.Fatalf("second Approved = false, want true: %#v", second)
	}

	requests := model.Requests()
	if got, want := len(requests), 2; got != want {
		t.Fatalf("model calls = %d, want %d", got, want)
	}
	firstReq := requests[0]
	secondReq := requests[1]
	if got, want := len(secondReq.Messages), len(firstReq.Messages)+2; got != want {
		t.Fatalf("second len(Messages) = %d, want first prompt + first answer + second prompt", got)
	}
	if !reflect.DeepEqual(secondReq.Messages[0], firstReq.Messages[0]) {
		t.Fatal("second review did not reuse the exact first prompt as stable prefix")
	}
	if got, want := secondReq.Messages[1].TextContent(), model.responses[0]; got != want {
		t.Fatalf("second prefix assistant text = %q, want first assessment %q", got, want)
	}
	prompt := secondReq.Messages[len(secondReq.Messages)-1].TextContent()
	if !strings.Contains(prompt, ">>> TRANSCRIPT DELTA START") {
		t.Fatalf("second prompt missing transcript delta:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Focused tests passed") {
		t.Fatalf("second prompt missing new parent transcript:\n%s", prompt)
	}
	if strings.Contains(prompt, "Please commit and push the prepared fix.") {
		t.Fatalf("second prompt repeated old transcript instead of delta:\n%s", prompt)
	}
}

func TestApprovalReviewerProviderE2EReportsCachedPromptHit(t *testing.T) {
	ctx := context.Background()
	service, session := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(t, ctx, service, session, sdksession.EventTypeUser, sdkmodel.RoleUser, "Please commit and push the prepared fix.")

	var (
		serverMu sync.Mutex
		calls    int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		responseFormat, _ := payload["response_format"].(map[string]any)
		if got := responseFormat["type"]; got != "json_schema" {
			t.Fatalf("response_format.type = %v, want json_schema", got)
		}
		if _, exists := payload["tools"]; exists {
			t.Fatalf("provider payload unexpectedly contains tools: %#v", payload["tools"])
		}

		serverMu.Lock()
		calls++
		call := calls
		serverMu.Unlock()

		cached := 0
		rationale := "commit is user requested"
		if call == 2 {
			cached = 128
			rationale = "push is user requested"
		}
		content := fmt.Sprintf(`{"outcome":"allow","risk_level":"medium","user_authorization":"high","rationale":%q}`, rationale)
		rawContent, _ := json.Marshal(content)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"model":"cache-provider","choices":[{"message":{"role":"assistant","content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":2048,"completion_tokens":32,"total_tokens":2080,"prompt_tokens_details":{"cached_tokens":%d}}}`, rawContent, cached)
	}))
	defer server.Close()

	factory := modelproviders.NewFactory()
	if err := factory.Register(modelproviders.Config{
		Alias:    "cache-provider",
		Provider: "openai-compatible",
		API:      modelproviders.APIOpenAICompatible,
		Model:    "cache-provider",
		BaseURL:  server.URL,
		Timeout:  2 * time.Second,
		Auth:     modelproviders.AuthConfig{Type: modelproviders.AuthNone},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	llm, err := factory.NewByAlias("cache-provider")
	if err != nil {
		t.Fatalf("NewByAlias() error = %v", err)
	}
	model := &approvalReviewerProviderRecorder{base: llm}
	reviewer := newModelApprovalReviewer(service)

	first, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(session, model, "git commit -m fix", map[string]any{"cmd": "git commit -m fix"}))
	if err != nil {
		t.Fatalf("first ReviewApproval() error = %v", err)
	}
	if !first.Approved || first.Authorization != "high" {
		t.Fatalf("first result = %#v, want approved high authorization", first)
	}
	appendApprovalReviewerTextEvent(t, ctx, service, session, sdksession.EventTypeAssistant, sdkmodel.RoleAssistant, "Focused tests passed; next I will push the branch.")
	second, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(session, model, "git push origin dev", map[string]any{"cmd": "git push origin dev"}))
	if err != nil {
		t.Fatalf("second ReviewApproval() error = %v", err)
	}
	if !second.Approved || second.Authorization != "high" {
		t.Fatalf("second result = %#v, want approved high authorization", second)
	}

	requests, usages := model.Snapshot()
	if got, want := len(requests), 2; got != want {
		t.Fatalf("model requests = %d, want %d", got, want)
	}
	if got, want := len(usages), 2; got != want {
		t.Fatalf("usage reports = %d, want %d", got, want)
	}
	if usages[1].CachedInputTokens <= 0 {
		t.Fatalf("second cached input tokens = %d, want provider-reported cache hit", usages[1].CachedInputTokens)
	}
	if !reflect.DeepEqual(requests[1].Messages[0], requests[0].Messages[0]) {
		t.Fatal("second provider-backed review did not preserve first prompt as stable prefix")
	}
	if !strings.Contains(requests[1].Messages[len(requests[1].Messages)-1].TextContent(), ">>> TRANSCRIPT DELTA START") {
		t.Fatalf("second provider-backed prompt missing transcript delta:\n%s", requests[1].Messages[len(requests[1].Messages)-1].TextContent())
	}
	events, err := service.Events(ctx, sdksession.EventsRequest{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if got, want := len(events), 2; got != want {
		t.Fatalf("parent session event count = %d, want %d", got, want)
	}
}

func TestParseGuardianAssessmentAcceptsJSONEmbeddedInText(t *testing.T) {
	tests := []string{
		`{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"ok"}`,
		"Assessment follows:\n{\"outcome\":\"deny\",\"risk_level\":\"high\",\"user_authorization\":\"low\",\"rationale\":\"too broad\"}\nDone.",
		"```json\n{\"outcome\":\"allow\",\"risk_level\":\"medium\",\"user_authorization\":\"medium\",\"rationale\":\"bounded\"}\n```",
	}
	for _, input := range tests {
		parsed, err := parseGuardianAssessment(input)
		if err != nil {
			t.Fatalf("parseGuardianAssessment(%q) error = %v", input, err)
		}
		if strings.TrimSpace(parsed.Outcome) == "" {
			t.Fatalf("parseGuardianAssessment(%q) returned no outcome", input)
		}
	}
}

func TestApprovalReviewerConcurrentReviewsDoNotMutateParentSession(t *testing.T) {
	ctx := context.Background()
	service, session := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(t, ctx, service, session, sdksession.EventTypeUser, sdkmodel.RoleUser, "Please inspect this directory and request the minimum permission needed.")
	release := make(chan struct{})
	model := &approvalReviewerFakeModel{
		responses: []string{
			`{"outcome":"allow","risk_level":"low","user_authorization":"medium","rationale":"read-only path is bounded"}`,
			`{"outcome":"allow","risk_level":"low","user_authorization":"medium","rationale":"read-only path is bounded"}`,
		},
		release: release,
		started: make(chan struct{}, 2),
	}
	reviewer := newModelApprovalReviewer(service)
	guardian := reviewer.(*guardianApprovalReviewer)
	readPath := t.TempDir()

	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			result, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(session, model, "read temp dir", map[string]any{
				"path": readPath,
			}))
			if err == nil && !result.Approved {
				err = errApprovalReviewerNotApproved
			}
			errs <- err
		}()
	}
	waitForApprovalReviewerCalls(t, model.started, 2)
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("ReviewApproval() error = %v", err)
		}
	}
	if got := len(model.Requests()); got != 2 {
		t.Fatalf("model calls = %d, want 2", got)
	}
	events, err := service.Events(ctx, sdksession.EventsRequest{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if got, want := len(events), 1; got != want {
		t.Fatalf("parent session event count = %d, want %d", got, want)
	}
	guardian.mu.Lock()
	reviewSession := guardian.sessionsByParent[session.SessionID]
	guardian.mu.Unlock()
	if reviewSession == nil {
		t.Fatal("review session not recorded")
	}
	reviewSession.mu.Lock()
	reviewEvents := len(reviewSession.events)
	reviewSession.mu.Unlock()
	if got, want := reviewEvents, 2; got != want {
		t.Fatalf("review trunk events = %d, want exactly one committed prompt/answer pair", got)
	}
}

func TestApprovalReviewerRejectsMissingRequestModel(t *testing.T) {
	_, err := newModelApprovalReviewer(nil).ReviewApproval(context.Background(), appgateway.ApprovalReviewRequest{})
	if err == nil || !strings.Contains(err.Error(), "current session model") {
		t.Fatalf("ReviewApproval() error = %v, want current session model error", err)
	}
}

func TestApprovalReviewerRejectsMissingSessionHistory(t *testing.T) {
	model := &approvalReviewerFakeModel{responses: []string{`{"outcome":"allow"}`}}
	_, err := newModelApprovalReviewer(nil).ReviewApproval(context.Background(), appgateway.ApprovalReviewRequest{
		Model: model,
	})
	if err == nil || !strings.Contains(err.Error(), "session history") {
		t.Fatalf("ReviewApproval() error = %v, want session history error", err)
	}
}

var errApprovalReviewerNotApproved = approvalReviewerError("approval reviewer returned denial")

type approvalReviewerError string

func (e approvalReviewerError) Error() string { return string(e) }

type approvalReviewerFakeModel struct {
	mu        sync.Mutex
	responses []string
	requests  []sdkmodel.Request
	release   <-chan struct{}
	started   chan struct{}
}

func (m *approvalReviewerFakeModel) Name() string { return "approval-reviewer-fake" }

func (m *approvalReviewerFakeModel) Generate(ctx context.Context, req *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	index := m.recordRequest(req)
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		if m.started != nil {
			m.started <- struct{}{}
		}
		if m.release != nil {
			select {
			case <-ctx.Done():
				yield(nil, ctx.Err())
				return
			case <-m.release:
			}
		}
		response := `{"outcome":"allow","risk_level":"low","user_authorization":"medium","rationale":"ok"}`
		m.mu.Lock()
		if index < len(m.responses) {
			response = m.responses[index]
		}
		m.mu.Unlock()
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventTurnDone,
			Response: &sdkmodel.Response{
				Status:       sdkmodel.ResponseStatusCompleted,
				TurnComplete: true,
				StepComplete: true,
				Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, response),
			},
		}, nil)
	}
}

func (m *approvalReviewerFakeModel) recordRequest(req *sdkmodel.Request) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := len(m.requests)
	if req == nil {
		m.requests = append(m.requests, sdkmodel.Request{})
		return index
	}
	cp := *req
	cp.Messages = sdkmodel.CloneMessages(req.Messages)
	cp.Instructions = sdkmodel.CloneParts(req.Instructions)
	cp.Tools = append([]sdkmodel.ToolSpec(nil), req.Tools...)
	cp.Output = sdkruntime.ModelRequestOptions{Output: req.Output}.OutputSpec()
	m.requests = append(m.requests, cp)
	return index
}

func (m *approvalReviewerFakeModel) Requests() []sdkmodel.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]sdkmodel.Request, 0, len(m.requests))
	for _, req := range m.requests {
		cp := req
		cp.Messages = sdkmodel.CloneMessages(req.Messages)
		cp.Instructions = sdkmodel.CloneParts(req.Instructions)
		cp.Tools = append([]sdkmodel.ToolSpec(nil), req.Tools...)
		cp.Output = sdkruntime.ModelRequestOptions{Output: req.Output}.OutputSpec()
		out = append(out, cp)
	}
	return out
}

type approvalReviewerProviderRecorder struct {
	base sdkmodel.LLM
	mu   sync.Mutex
	reqs []sdkmodel.Request
	uses []sdkmodel.Usage
}

func (m *approvalReviewerProviderRecorder) Name() string { return m.base.Name() }

func (m *approvalReviewerProviderRecorder) Generate(ctx context.Context, req *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	m.recordRequest(req)
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		for event, err := range m.base.Generate(ctx, req) {
			if event != nil && event.Response != nil {
				m.recordUsage(event.Response.Usage)
			}
			if !yield(event, err) {
				return
			}
		}
	}
}

func (m *approvalReviewerProviderRecorder) recordRequest(req *sdkmodel.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if req == nil {
		m.reqs = append(m.reqs, sdkmodel.Request{})
		return
	}
	cp := *req
	cp.Messages = sdkmodel.CloneMessages(req.Messages)
	cp.Instructions = sdkmodel.CloneParts(req.Instructions)
	cp.Tools = append([]sdkmodel.ToolSpec(nil), req.Tools...)
	cp.Output = sdkruntime.ModelRequestOptions{Output: req.Output}.OutputSpec()
	m.reqs = append(m.reqs, cp)
}

func (m *approvalReviewerProviderRecorder) recordUsage(usage sdkmodel.Usage) {
	if usage.PromptTokens == 0 && usage.CachedInputTokens == 0 && usage.CompletionTokens == 0 && usage.TotalTokens == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.uses = append(m.uses, usage)
}

func (m *approvalReviewerProviderRecorder) Snapshot() ([]sdkmodel.Request, []sdkmodel.Usage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	reqs := make([]sdkmodel.Request, 0, len(m.reqs))
	for _, req := range m.reqs {
		cp := req
		cp.Messages = sdkmodel.CloneMessages(req.Messages)
		cp.Instructions = sdkmodel.CloneParts(req.Instructions)
		cp.Tools = append([]sdkmodel.ToolSpec(nil), req.Tools...)
		cp.Output = sdkruntime.ModelRequestOptions{Output: req.Output}.OutputSpec()
		reqs = append(reqs, cp)
	}
	return reqs, append([]sdkmodel.Usage(nil), m.uses...)
}

func newApprovalReviewerTestSession(t *testing.T, ctx context.Context) (sdksession.Service, sdksession.Session) {
	t.Helper()
	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	session, err := service.StartSession(ctx, sdksession.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "user-1",
		PreferredSessionID: "approval-reviewer-test",
		Workspace:          sdksession.WorkspaceRef{Key: "workspace-1", CWD: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	return service, session
}

func appendApprovalReviewerTextEvent(
	t *testing.T,
	ctx context.Context,
	service sdksession.Service,
	session sdksession.Session,
	eventType sdksession.EventType,
	role sdkmodel.Role,
	text string,
) {
	t.Helper()
	message := sdkmodel.NewTextMessage(role, text)
	if _, err := service.AppendEvent(ctx, sdksession.AppendEventRequest{
		SessionRef: session.SessionRef,
		Event: &sdksession.Event{
			Type:       eventType,
			Visibility: sdksession.VisibilityCanonical,
			Message:    &message,
			Text:       text,
		},
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
}

func approvalReviewerTestRequest(session sdksession.Session, model sdkmodel.LLM, reason string, input map[string]any) appgateway.ApprovalReviewRequest {
	raw, _ := json.Marshal(input)
	return appgateway.ApprovalReviewRequest{
		SessionRef: session.SessionRef,
		Mode:       appgateway.ApprovalModeAutoReview,
		ReviewID:   "review-test",
		RunID:      "run-test",
		TurnID:     "turn-test",
		Model:      model,
		Approval: &appgateway.ApprovalPayload{
			ToolName:           "request_permissions",
			RawInput:           input,
			Reason:             reason,
			SandboxPermissions: "with_additional_permissions",
			Status:             appgateway.ApprovalStatusPending,
		},
		RuntimeRequest: sdkruntime.ApprovalRequest{
			Mode: "auto-review",
			Tool: sdktool.Definition{Name: "request_permissions"},
			Call: sdktool.Call{Name: "request_permissions", Input: raw},
		},
	}
}

func waitForApprovalReviewerCalls(t *testing.T, ch <-chan struct{}, count int) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for i := 0; i < count; i++ {
		select {
		case <-ch:
		case <-timer.C:
			t.Fatalf("timed out waiting for %d reviewer calls", count)
		}
	}
}
