//go:build e2e

package gatewayapp

import (
	"context"
	"iter"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/model/providers/e2etest"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestApprovalReviewerMultiTurnReviewAgentE2E(t *testing.T) {
	spec := e2etest.RequireLLM(t, e2etest.Config{
		DefaultProvider: "minimax",
		DefaultModel:    "MiniMax-M2",
		Timeout:         90 * time.Second,
		MaxTokens:       512,
	})
	model := &approvalReviewerRecordingLLM{base: spec.LLM}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	service, session := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(t, ctx, service, session, session.EventTypeUser, model.RoleUser, strings.Repeat("The user explicitly asked the agent to inspect the repository, commit the focused fix, and push the current branch after tests pass. ", 40))
	reviewer := newModelApprovalReviewer(service)

	first, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(session, model, "git commit the focused fix", map[string]any{
		"cmd": "git commit -m approval-guardian-e2e",
	}))
	if err != nil {
		t.Fatalf("first ReviewApproval() error = %v", err)
	}
	if !first.Approved {
		t.Fatalf("first result = %#v, want approved explicit user-requested commit", first)
	}
	if strings.TrimSpace(first.Rationale) == "" || first.Risk == "" || first.Authorization == "" {
		t.Fatalf("first result incomplete: %#v", first)
	}
	appendApprovalReviewerTextEvent(t, ctx, service, session, session.EventTypeAssistant, model.RoleAssistant, "Tests passed. The next action is the user-requested push of the same branch.")
	second, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(session, model, "git push the current branch", map[string]any{
		"cmd": "git push origin dev",
	}))
	if err != nil {
		t.Fatalf("second ReviewApproval() error = %v", err)
	}
	if !second.Approved {
		t.Fatalf("second result = %#v, want approved explicit user-requested push", second)
	}
	if strings.TrimSpace(second.Rationale) == "" || second.Risk == "" || second.Authorization == "" {
		t.Fatalf("second result incomplete: %#v", second)
	}

	requests, usages := model.Snapshot()
	if got, want := len(requests), 2; got != want {
		t.Fatalf("model calls = %d, want %d", got, want)
	}
	for i, req := range requests {
		if got := len(req.Tools); got != 0 {
			t.Fatalf("request %d len(Tools) = %d, want 0", i, got)
		}
		if req.Output == nil || req.Output.Mode != model.OutputModeSchema {
			t.Fatalf("request %d Output = %#v, want schema", i, req.Output)
		}
	}
	if !reflect.DeepEqual(requests[1].Messages[0], requests[0].Messages[0]) {
		t.Fatal("second real review request did not preserve first prompt as cacheable prefix")
	}
	if !strings.Contains(requests[1].Messages[len(requests[1].Messages)-1].TextContent(), ">>> TRANSCRIPT DELTA START") {
		t.Fatalf("second real review request missing transcript delta:\n%s", requests[1].Messages[len(requests[1].Messages)-1].TextContent())
	}
	if len(usages) >= 2 && usages[1].CachedInputTokens > 0 {
		t.Logf("reviewer prompt cache hit: cached_input_tokens=%d prompt_tokens=%d", usages[1].CachedInputTokens, usages[1].PromptTokens)
	} else {
		t.Logf("provider did not report a cache hit; stable prefix was verified locally, usages=%+v", usages)
	}

	events, err := service.Events(ctx, session.EventsRequest{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if got, want := len(events), 2; got != want {
		t.Fatalf("parent session event count = %d, want unchanged parent transcript size %d", got, want)
	}
}

type approvalReviewerRecordingLLM struct {
	base model.LLM
	mu   sync.Mutex
	reqs []model.Request
	uses []model.Usage
}

func (m *approvalReviewerRecordingLLM) Name() string { return m.base.Name() }

func (m *approvalReviewerRecordingLLM) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
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

func (m *approvalReviewerRecordingLLM) recordRequest(req *model.Request) {
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

func (m *approvalReviewerRecordingLLM) recordUsage(usage model.Usage) {
	if usage.PromptTokens == 0 && usage.CachedInputTokens == 0 && usage.CompletionTokens == 0 && usage.TotalTokens == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.uses = append(m.uses, usage)
}

func (m *approvalReviewerRecordingLLM) Snapshot() ([]model.Request, []model.Usage) {
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
