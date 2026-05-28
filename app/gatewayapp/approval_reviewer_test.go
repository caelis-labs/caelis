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

	"github.com/OnslaughtSnail/caelis/impl/model/providers"
	"github.com/OnslaughtSnail/caelis/impl/session/memory"
	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestApprovalReviewerUsesRequestModelAndSessionContext(t *testing.T) {
	ctx := context.Background()
	service, activeSession := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(t, ctx, service, activeSession, session.EventTypeUser, model.RoleUser, "Please push the current changes after the focused tests pass.")
	testModel := &approvalReviewerFakeModel{
		responses: []string{`{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"narrow request"}`},
	}
	reviewer := newModelApprovalReviewer(service)

	result, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(activeSession, testModel, "git push origin dev", map[string]any{
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

	requests := testModel.Requests()
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
	if req.Output == nil || req.Output.Mode != model.OutputModeSchema {
		t.Fatalf("Output = %#v, want schema output", req.Output)
	}
	if req.Output.MaxOutputTokens != guardianMaxOutputTokens {
		t.Fatalf("Output.MaxOutputTokens = %d, want %d", req.Output.MaxOutputTokens, guardianMaxOutputTokens)
	}
	if got := len(req.Instructions); got != 1 {
		t.Fatalf("len(Instructions) = %d, want guardian policy", got)
	}
	if !strings.Contains(req.Instructions[0].Text.Text, "You are judging one planned coding-agent action") {
		t.Fatalf("instruction text = %q, want guardian policy", req.Instructions[0].Text.Text)
	}
	if !strings.Contains(req.Instructions[0].Text.Text, `return exactly {"outcome":"allow"}`) {
		t.Fatalf("instruction text = %q, want low-risk compact output contract", req.Instructions[0].Text.Text)
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

	events, err := service.Events(ctx, session.EventsRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if got, want := len(events), 1; got != want {
		t.Fatalf("parent session event count = %d, want %d", got, want)
	}
}

func TestApprovalReviewerReusesStablePrefixAndSendsTranscriptDelta(t *testing.T) {
	ctx := context.Background()
	service, activeSession := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(t, ctx, service, activeSession, session.EventTypeUser, model.RoleUser, "Please commit and push the prepared fix.")
	testModel := &approvalReviewerFakeModel{responses: []string{
		`{"outcome":"allow","risk_level":"medium","user_authorization":"high","rationale":"commit is user requested"}`,
		`{"outcome":"allow","risk_level":"medium","user_authorization":"high","rationale":"push is user requested"}`,
	}}
	reviewer := newModelApprovalReviewer(service)

	first, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(activeSession, testModel, "git commit -m fix", map[string]any{"cmd": "git commit -m fix"}))
	if err != nil {
		t.Fatalf("first ReviewApproval() error = %v", err)
	}
	if !first.Approved {
		t.Fatalf("first Approved = false, want true: %#v", first)
	}
	appendApprovalReviewerTextEvent(t, ctx, service, activeSession, session.EventTypeAssistant, model.RoleAssistant, "Focused tests passed; next I will push the branch.")
	second, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(activeSession, testModel, "git push origin dev", map[string]any{"cmd": "git push origin dev"}))
	if err != nil {
		t.Fatalf("second ReviewApproval() error = %v", err)
	}
	if !second.Approved {
		t.Fatalf("second Approved = false, want true: %#v", second)
	}

	requests := testModel.Requests()
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
	if got, want := secondReq.Messages[1].TextContent(), testModel.responses[0]; got != want {
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

func TestApprovalReviewerRetriesInvalidJSONAssessment(t *testing.T) {
	ctx := context.Background()
	service, activeSession := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(t, ctx, service, activeSession, session.EventTypeUser, model.RoleUser, "Please inspect the tree and report findings.")
	testModel := &approvalReviewerFakeModel{responses: []string{
		`{"outcome":`,
		`{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"read-only inspection is authorized"}`,
	}}
	reviewer := newModelApprovalReviewer(service)

	result, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(activeSession, testModel, "read-only tree inspection", map[string]any{"cmd": "rg TODO"}))
	if err != nil {
		t.Fatalf("ReviewApproval() error = %v", err)
	}
	if !result.Approved {
		t.Fatalf("Approved = false, want true: %#v", result)
	}

	requests := testModel.Requests()
	if got, want := len(requests), 2; got != want {
		t.Fatalf("model calls = %d, want retry after invalid JSON", got)
	}
	if !reflect.DeepEqual(requests[1].Messages, requests[0].Messages) {
		t.Fatal("retry prompt was polluted by the invalid reviewer response")
	}

	guardian := reviewer.(*guardianApprovalReviewer)
	guardian.mu.Lock()
	reviewSession := guardian.sessionsByParent[activeSession.SessionID]
	guardian.mu.Unlock()
	if reviewSession == nil {
		t.Fatal("review session not recorded")
	}
	reviewSession.mu.Lock()
	reviewEvents := len(reviewSession.events)
	reviewSession.mu.Unlock()
	if got, want := reviewEvents, 2; got != want {
		t.Fatalf("review trunk events = %d, want one valid prompt/answer pair", got)
	}
}

func TestApprovalReviewerStopsAfterInvalidJSONAssessmentRetries(t *testing.T) {
	ctx := context.Background()
	service, activeSession := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(t, ctx, service, activeSession, session.EventTypeUser, model.RoleUser, "Please inspect the tree and report findings.")
	responses := make([]string, 0, guardianAssessmentMaxAttempts)
	for i := 0; i < guardianAssessmentMaxAttempts; i++ {
		responses = append(responses, `{"outcome":`)
	}
	testModel := &approvalReviewerFakeModel{responses: responses}
	reviewer := newModelApprovalReviewer(service)

	_, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(activeSession, testModel, "read-only tree inspection", map[string]any{"cmd": "rg TODO"}))
	if err == nil || !strings.Contains(err.Error(), "valid JSON assessment") {
		t.Fatalf("ReviewApproval() error = %v, want invalid JSON retry exhaustion", err)
	}
	if got, want := len(testModel.Requests()), guardianAssessmentMaxAttempts; got != want {
		t.Fatalf("model calls = %d, want %d", got, want)
	}

	guardian := reviewer.(*guardianApprovalReviewer)
	guardian.mu.Lock()
	reviewSession := guardian.sessionsByParent[activeSession.SessionID]
	guardian.mu.Unlock()
	if reviewSession == nil {
		t.Fatal("review session not recorded")
	}
	reviewSession.mu.Lock()
	reviewEvents := len(reviewSession.events)
	reviewSession.mu.Unlock()
	if reviewEvents != 0 {
		t.Fatalf("review trunk events = %d, want no invalid reviewer responses committed", reviewEvents)
	}
}

func TestApprovalReviewerProviderE2EReportsCachedPromptHit(t *testing.T) {
	ctx := context.Background()
	service, activeSession := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(t, ctx, service, activeSession, session.EventTypeUser, model.RoleUser, "Please commit and push the prepared fix.")

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
		if got, ok := payload["max_tokens"].(float64); !ok || int(got) != guardianMaxOutputTokens {
			t.Fatalf("max_tokens = %#v, want %d", payload["max_tokens"], guardianMaxOutputTokens)
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

	factory := providers.NewFactory()
	if err := factory.Register(providers.Config{
		Alias:    "cache-provider",
		Provider: "openai-compatible",
		API:      providers.APIOpenAICompatible,
		Model:    "cache-provider",
		BaseURL:  server.URL,
		Timeout:  2 * time.Second,
		Auth:     providers.AuthConfig{Type: providers.AuthNone},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	llm, err := factory.NewByAlias("cache-provider")
	if err != nil {
		t.Fatalf("NewByAlias() error = %v", err)
	}
	testModel := &approvalReviewerProviderRecorder{base: llm}
	reviewer := newModelApprovalReviewer(service)

	first, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(activeSession, testModel, "git commit -m fix", map[string]any{"cmd": "git commit -m fix"}))
	if err != nil {
		t.Fatalf("first ReviewApproval() error = %v", err)
	}
	if !first.Approved || first.Authorization != "high" {
		t.Fatalf("first result = %#v, want approved high authorization", first)
	}
	appendApprovalReviewerTextEvent(t, ctx, service, activeSession, session.EventTypeAssistant, model.RoleAssistant, "Focused tests passed; next I will push the branch.")
	second, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(activeSession, testModel, "git push origin dev", map[string]any{"cmd": "git push origin dev"}))
	if err != nil {
		t.Fatalf("second ReviewApproval() error = %v", err)
	}
	if !second.Approved || second.Authorization != "high" {
		t.Fatalf("second result = %#v, want approved high authorization", second)
	}

	requests, usages := testModel.Snapshot()
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
	events, err := service.Events(ctx, session.EventsRequest{SessionRef: activeSession.SessionRef})
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

func TestParseGuardianAssessmentDefaultsCompactAllowAndDeny(t *testing.T) {
	allow, err := parseGuardianAssessment(`{"outcome":"allow"}`)
	if err != nil {
		t.Fatalf("parseGuardianAssessment(allow) error = %v", err)
	}
	if allow.Outcome != "allow" || allow.RiskLevel != "low" || allow.UserAuthorization != "unknown" {
		t.Fatalf("allow assessment = %#v, want low-risk unknown-authorization allow", allow)
	}
	if !strings.Contains(allow.Rationale, "low-risk allow") {
		t.Fatalf("allow rationale = %q, want compact default rationale", allow.Rationale)
	}

	mediumAllow, err := parseGuardianAssessment(`{"outcome":"allow","risk_level":"medium"}`)
	if err != nil {
		t.Fatalf("parseGuardianAssessment(medium allow) error = %v", err)
	}
	if mediumAllow.Outcome != "allow" || mediumAllow.RiskLevel != "medium" || mediumAllow.UserAuthorization != "unknown" {
		t.Fatalf("medium allow assessment = %#v, want medium-risk unknown-authorization allow", mediumAllow)
	}
	if strings.Contains(mediumAllow.Rationale, "low-risk") {
		t.Fatalf("medium allow rationale = %q, must not claim low risk", mediumAllow.Rationale)
	}

	deny, err := parseGuardianAssessment(`{"outcome":"deny"}`)
	if err != nil {
		t.Fatalf("parseGuardianAssessment(deny) error = %v", err)
	}
	if deny.Outcome != "deny" || deny.RiskLevel != "high" || deny.UserAuthorization != "unknown" {
		t.Fatalf("deny assessment = %#v, want high-risk unknown-authorization deny", deny)
	}
	if !strings.Contains(deny.Rationale, "deny decision") {
		t.Fatalf("deny rationale = %q, want compact default rationale", deny.Rationale)
	}
}

func TestApprovalReviewerConcurrentReviewsDoNotMutateParentSession(t *testing.T) {
	ctx := context.Background()
	service, activeSession := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(t, ctx, service, activeSession, session.EventTypeUser, model.RoleUser, "Please inspect this directory and request the minimum permission needed.")
	release := make(chan struct{})
	testModel := &approvalReviewerFakeModel{
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
			result, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(activeSession, testModel, "read temp dir", map[string]any{
				"path": readPath,
			}))
			if err == nil && !result.Approved {
				err = errApprovalReviewerNotApproved
			}
			errs <- err
		}()
	}
	waitForApprovalReviewerCalls(t, testModel.started, 2)
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("ReviewApproval() error = %v", err)
		}
	}
	if got := len(testModel.Requests()); got != 2 {
		t.Fatalf("model calls = %d, want 2", got)
	}
	events, err := service.Events(ctx, session.EventsRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if got, want := len(events), 1; got != want {
		t.Fatalf("parent session event count = %d, want %d", got, want)
	}
	guardian.mu.Lock()
	reviewSession := guardian.sessionsByParent[activeSession.SessionID]
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
	_, err := newModelApprovalReviewer(nil).ReviewApproval(context.Background(), kernel.ApprovalReviewRequest{})
	if err == nil || !strings.Contains(err.Error(), "current session model") {
		t.Fatalf("ReviewApproval() error = %v, want current session model error", err)
	}
}

func TestApprovalReviewerRejectsMissingSessionHistory(t *testing.T) {
	testModel := &approvalReviewerFakeModel{responses: []string{`{"outcome":"allow"}`}}
	_, err := newModelApprovalReviewer(nil).ReviewApproval(context.Background(), kernel.ApprovalReviewRequest{
		Model: testModel,
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
	requests  []model.Request
	release   <-chan struct{}
	started   chan struct{}
}

func (m *approvalReviewerFakeModel) Name() string { return "approval-reviewer-fake" }

func (m *approvalReviewerFakeModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	index := m.recordRequest(req)
	return func(yield func(*model.StreamEvent, error) bool) {
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
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Status:       model.ResponseStatusCompleted,
				TurnComplete: true,
				StepComplete: true,
				Message:      model.NewTextMessage(model.RoleAssistant, response),
			},
		}, nil)
	}
}

func (m *approvalReviewerFakeModel) recordRequest(req *model.Request) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := len(m.requests)
	if req == nil {
		m.requests = append(m.requests, model.Request{})
		return index
	}
	cp := *req
	cp.Messages = model.CloneMessages(req.Messages)
	cp.Instructions = model.CloneParts(req.Instructions)
	cp.Tools = append([]model.ToolSpec(nil), req.Tools...)
	cp.Output = agent.ModelRequestOptions{Output: req.Output}.OutputSpec()
	m.requests = append(m.requests, cp)
	return index
}

func (m *approvalReviewerFakeModel) Requests() []model.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Request, 0, len(m.requests))
	for _, req := range m.requests {
		cp := req
		cp.Messages = model.CloneMessages(req.Messages)
		cp.Instructions = model.CloneParts(req.Instructions)
		cp.Tools = append([]model.ToolSpec(nil), req.Tools...)
		cp.Output = agent.ModelRequestOptions{Output: req.Output}.OutputSpec()
		out = append(out, cp)
	}
	return out
}

type approvalReviewerProviderRecorder struct {
	base model.LLM
	mu   sync.Mutex
	reqs []model.Request
	uses []model.Usage
}

func (m *approvalReviewerProviderRecorder) Name() string { return m.base.Name() }

func (m *approvalReviewerProviderRecorder) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.recordRequest(req)
	return func(yield func(*model.StreamEvent, error) bool) {
		for event, err := range m.base.Generate(ctx, req) {
			if event != nil && event.Response != nil {
				m.recordUsage(event.Usage)
			}
			if !yield(event, err) {
				return
			}
		}
	}
}

func (m *approvalReviewerProviderRecorder) recordRequest(req *model.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if req == nil {
		m.reqs = append(m.reqs, model.Request{})
		return
	}
	cp := *req
	cp.Messages = model.CloneMessages(req.Messages)
	cp.Instructions = model.CloneParts(req.Instructions)
	cp.Tools = append([]model.ToolSpec(nil), req.Tools...)
	cp.Output = agent.ModelRequestOptions{Output: req.Output}.OutputSpec()
	m.reqs = append(m.reqs, cp)
}

func (m *approvalReviewerProviderRecorder) recordUsage(usage model.Usage) {
	if usage.PromptTokens == 0 && usage.CachedInputTokens == 0 && usage.CompletionTokens == 0 && usage.ReasoningTokens == 0 && usage.TotalTokens == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.uses = append(m.uses, usage)
}

func (m *approvalReviewerProviderRecorder) Snapshot() ([]model.Request, []model.Usage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	reqs := make([]model.Request, 0, len(m.reqs))
	for _, req := range m.reqs {
		cp := req
		cp.Messages = model.CloneMessages(req.Messages)
		cp.Instructions = model.CloneParts(req.Instructions)
		cp.Tools = append([]model.ToolSpec(nil), req.Tools...)
		cp.Output = agent.ModelRequestOptions{Output: req.Output}.OutputSpec()
		reqs = append(reqs, cp)
	}
	return reqs, append([]model.Usage(nil), m.uses...)
}

func newApprovalReviewerTestSession(t *testing.T, ctx context.Context) (session.Service, session.Session) {
	t.Helper()
	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	activeSession, err := service.StartSession(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "user-1",
		PreferredSessionID: "approval-reviewer-test",
		Workspace:          session.WorkspaceRef{Key: "workspace-1", CWD: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	return service, activeSession
}

func appendApprovalReviewerTextEvent(
	t *testing.T,
	ctx context.Context,
	service session.Service,
	activeSession session.Session,
	eventType session.EventType,
	role model.Role,
	text string,
) {
	t.Helper()
	message := model.NewTextMessage(role, text)
	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: activeSession.SessionRef,
		Event: &session.Event{
			Type:       eventType,
			Visibility: session.VisibilityCanonical,
			Message:    &message,
			Text:       text,
		},
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
}

func approvalReviewerTestRequest(activeSession session.Session, llm model.LLM, reason string, input map[string]any) kernel.ApprovalReviewRequest {
	raw, _ := json.Marshal(input)
	return kernel.ApprovalReviewRequest{
		SessionRef: activeSession.SessionRef,
		Mode:       kernel.ApprovalModeAutoReview,
		ReviewID:   "review-test",
		RunID:      "run-test",
		TurnID:     "turn-test",
		Model:      llm,
		Approval: &kernel.ApprovalPayload{
			ToolName: "request_permissions",
			RawInput: input,
			Reason:   reason,
			Status:   kernel.ApprovalStatusPending,
		},
		RuntimeRequest: agent.ApprovalRequest{
			Mode: "auto-review",
			Tool: tool.Definition{Name: "request_permissions"},
			Call: tool.Call{Name: "request_permissions", Input: raw},
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
