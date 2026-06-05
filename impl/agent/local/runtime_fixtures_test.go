package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/agent/local/chat"
	"github.com/OnslaughtSnail/caelis/impl/policy/presets"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/host"
	"github.com/OnslaughtSnail/caelis/impl/session/memory"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/filesystem"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/plan"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/shell"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/spawn"
	tasktool "github.com/OnslaughtSnail/caelis/impl/tool/builtin/task"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
	taskapi "github.com/OnslaughtSnail/caelis/ports/task"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

type stubACPController struct {
	attach            func(context.Context, controller.AttachRequest) (session.ParticipantBinding, error)
	runTurn           func(context.Context, controller.TurnRequest) (controller.TurnResult, error)
	promptParticipant func(context.Context, controller.ParticipantPromptRequest) (controller.TurnResult, error)
}

func (stubACPController) Activate(context.Context, controller.HandoffRequest) (session.ControllerBinding, error) {
	return session.ControllerBinding{}, nil
}

func (stubACPController) Deactivate(context.Context, session.SessionRef) error {
	return nil
}

func (s stubACPController) RunTurn(ctx context.Context, req controller.TurnRequest) (controller.TurnResult, error) {
	if s.runTurn != nil {
		return s.runTurn(ctx, req)
	}
	handle := newTestControllerTurnHandle(nil)
	handle.finish()
	return controller.TurnResult{Handle: handle}, nil
}

func (s stubACPController) Attach(ctx context.Context, req controller.AttachRequest) (session.ParticipantBinding, error) {
	if s.attach != nil {
		return s.attach(ctx, req)
	}
	return session.CloneParticipantBinding(req.Binding), nil
}

func (s stubACPController) PromptParticipant(ctx context.Context, req controller.ParticipantPromptRequest) (controller.TurnResult, error) {
	if s.promptParticipant != nil {
		return s.promptParticipant(ctx, req)
	}
	handle := newTestControllerTurnHandle(nil)
	handle.finish()
	return controller.TurnResult{Handle: handle}, nil
}

func (stubACPController) Detach(context.Context, controller.DetachRequest) error {
	return nil
}

type testControllerTurnHandle struct {
	cancelFn  context.CancelFunc
	eventsCh  chan testControllerTurnEvent
	closeOnce sync.Once
	mu        sync.Mutex
	cancelled bool
}

type testControllerTurnEvent struct {
	event *session.Event
	err   error
}

func newTestControllerTurnHandle(cancel context.CancelFunc) *testControllerTurnHandle {
	return &testControllerTurnHandle{
		cancelFn: cancel,
		eventsCh: make(chan testControllerTurnEvent, 16),
	}
}

func (h *testControllerTurnHandle) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for item := range h.eventsCh {
			if !yield(session.CloneEvent(item.event), item.err) {
				return
			}
		}
	}
}

func (h *testControllerTurnHandle) Cancel() controller.CancelResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancelled {
		return controller.CancelResult{Status: controller.CancelStatusAlreadyCancelled}
	}
	h.cancelled = true
	if h.cancelFn != nil {
		h.cancelFn()
	}
	return controller.CancelResult{Status: controller.CancelStatusCancelled}
}

func (h *testControllerTurnHandle) Close() error { return nil }

func (h *testControllerTurnHandle) publishEvent(event *session.Event) {
	if h == nil || event == nil {
		return
	}
	h.eventsCh <- testControllerTurnEvent{event: session.CloneEvent(event)}
}

func (h *testControllerTurnHandle) publishError(err error) {
	if h == nil || err == nil {
		return
	}
	h.eventsCh <- testControllerTurnEvent{err: err}
}

func (h *testControllerTurnHandle) finish() {
	if h == nil {
		return
	}
	h.closeOnce.Do(func() {
		close(h.eventsCh)
	})
}

type staticModel struct {
	text string
}

func (m staticModel) Name() string { return "stub" }

func (m staticModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, m.text),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			},
		}, nil)
	}
}

type gatedStreamingModel struct {
	started      chan struct{}
	releaseFinal chan struct{}
}

func (m *gatedStreamingModel) Name() string { return "gated-streaming" }

func (m *gatedStreamingModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		if m.started != nil {
			select {
			case <-m.started:
			default:
				close(m.started)
			}
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventPartDelta,
			PartDelta: &model.PartDelta{
				Kind:      model.PartKindText,
				TextDelta: "hel",
			},
		}, nil)
		if m.releaseFinal != nil {
			<-m.releaseFinal
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "hello"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			},
		}, nil)
	}
}

type steerRuntimeModel struct {
	started      chan struct{}
	releaseFirst chan struct{}

	mu       sync.Mutex
	requests []model.Request
}

func (m *steerRuntimeModel) Name() string { return "steer-runtime" }

func (m *steerRuntimeModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.mu.Lock()
	if req != nil {
		cp := *req
		cp.Messages = model.CloneMessages(req.Messages)
		cp.Instructions = model.CloneParts(req.Instructions)
		m.requests = append(m.requests, cp)
	}
	callIndex := len(m.requests)
	m.mu.Unlock()

	return func(yield func(*model.StreamEvent, error) bool) {
		if callIndex == 1 {
			if m.started != nil {
				select {
				case <-m.started:
				default:
					close(m.started)
				}
			}
			if m.releaseFirst != nil {
				<-m.releaseFirst
			}
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, "first answer"),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
				},
			}, nil)
			return
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "steered answer"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			},
		}, nil)
	}
}

func (m *steerRuntimeModel) Requests() []model.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Request, len(m.requests))
	for i, req := range m.requests {
		out[i] = req
		out[i].Messages = model.CloneMessages(req.Messages)
		out[i].Instructions = model.CloneParts(req.Instructions)
	}
	return out
}

type historyReplayModel struct {
	t         *testing.T
	wantTexts []string
	replyText string
	calls     int
}

func (m *historyReplayModel) Name() string { return "history-replay" }

func (m *historyReplayModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	if req == nil {
		m.t.Fatal("Generate() request = nil")
		return nil
	}
	got := make([]string, 0, len(req.Messages))
	for _, message := range req.Messages {
		if text := strings.TrimSpace(message.TextContent()); text != "" {
			got = append(got, text)
		}
	}
	if len(got) != len(m.wantTexts) {
		m.t.Fatalf("replayed message count = %d, want %d (%v)", len(got), len(m.wantTexts), got)
	}
	for i := range m.wantTexts {
		if got[i] != m.wantTexts[i] {
			m.t.Fatalf("replayed message[%d] = %q, want %q (all=%v)", i, got[i], m.wantTexts[i], got)
		}
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, m.replyText),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			},
		}, nil)
	}
}

type toolLoopRuntimeModel struct {
	calls int
}

func (m *toolLoopRuntimeModel) Name() string { return "tool-loop" }

func (m *toolLoopRuntimeModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	return func(yield func(*model.StreamEvent, error) bool) {
		if callIndex == 1 {
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "call-1",
						Name: "ECHO",
						Args: string(mustJSONRaw(tmap("value", "pong"))),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
			return
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "pong"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonStop,
			},
		}, nil)
	}
}

type planLoopRuntimeModel struct {
	calls int
}

func (m *planLoopRuntimeModel) Name() string { return "plan-loop" }

func (m *planLoopRuntimeModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	return func(yield func(*model.StreamEvent, error) bool) {
		if callIndex == 1 {
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "plan-1",
						Name: plan.ToolName,
						Args: string(mustJSONRaw(map[string]any{
							"entries": []map[string]any{
								{"content": "Inspect repo", "status": "completed"},
								{"content": "Implement runtime bridge", "status": "in_progress"},
							},
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
			return
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "plan ready"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonStop,
			},
		}, nil)
	}
}

func mustJSONRaw(value map[string]any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

func tmap(key string, value any) map[string]any {
	return map[string]any{key: value}
}

func newTestSessionService(t *testing.T, sessionID string) (session.Service, session.Session) {
	t.Helper()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return sessionID },
	}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	return sessions, activeSession
}

func hostRuntimeForTest(t *testing.T, cwd string) *host.Runtime {
	t.Helper()
	rt, err := host.New(host.Config{CWD: cwd})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	return rt
}

func assistantEvent(text string) *session.Event {
	message := model.NewTextMessage(model.RoleAssistant, text)
	return &session.Event{
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityCanonical,
		Message:    &message,
		Text:       text,
	}
}

func acpControllerChunk(text string) *session.Event {
	message := model.NewTextMessage(model.RoleAssistant, text)
	return &session.Event{
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityCanonical,
		Message:    &message,
		Text:       text,
		Scope: &session.EventScope{
			Source: "acp",
			ACP: session.ACPRef{
				SessionID: "remote-acp-main",
				EventType: string(session.ProtocolUpdateTypeAgentMessage),
			},
		},
		Protocol: &session.EventProtocol{
			UpdateType: string(session.ProtocolUpdateTypeAgentMessage),
		},
	}
}

func userTextEvent(text string) *session.Event {
	message := model.NewTextMessage(model.RoleUser, text)
	return &session.Event{
		Type:       session.EventTypeUser,
		Visibility: session.VisibilityCanonical,
		Message:    &message,
		Text:       strings.TrimSpace(text),
	}
}

func appendTestEvent(t *testing.T, sessions session.Service, ref session.SessionRef, event *session.Event) {
	t.Helper()
	if _, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: ref,
		Event:      event,
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
}

type contextProbeModel struct {
	t                           *testing.T
	calls                       int
	compactionCalls             int
	normalCalls                 int
	compactBody                 string
	wantCompactionInputContains []string
	wantCompactionInputOmit     []string
	wantMessageContains         []string
	wantMessagesOmit            []string
	replyText                   string
}

func (m *contextProbeModel) Name() string { return "context-probe" }

func (m *contextProbeModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	instructions := requestInstructionsText(req)
	messages := requestMessageTexts(req)
	if strings.Contains(instructions, "CONTEXT CHECKPOINT COMPACTION") {
		m.compactionCalls++
		compactionInput := strings.Join(requestMessageTexts(req), "\n")
		for _, needle := range m.wantCompactionInputContains {
			if !strings.Contains(compactionInput, needle) {
				m.t.Fatalf("compaction input missing %q: %q", needle, compactionInput)
			}
		}
		for _, needle := range m.wantCompactionInputOmit {
			if strings.Contains(compactionInput, needle) {
				m.t.Fatalf("compaction input unexpectedly contains %q: %q", needle, compactionInput)
			}
		}
		body := strings.TrimSpace(m.compactBody)
		if body == "" {
			body = `CONTEXT CHECKPOINT

## Objective
- build compact runtime

## User Constraints
- do not lose blocker continuity

## Durable Decisions
- prefer compact event checkpoint overlay

## Verified Facts
- provider intermittently returns 529 overloaded_error when histories get too large

## Current Progress
- checkpoint event inserted into durable history

## Open Questions / Risks
- compaction quality must preserve blockers

## Next Actions
1. validate with real e2e tests and tune the compact prompt

## Active Tasks
- none

## Active Participants
- none

## Latest Blockers
- provider intermittently returns 529 overloaded_error

## Operational Notes
- files touched: impl/agent/local/compaction.go
- commands run: go test ./ports/...`
		}
		return func(yield func(*model.StreamEvent, error) bool) {
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, body),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
				},
			}, nil)
		}
	}
	m.normalCalls++
	for _, needle := range m.wantMessageContains {
		found := false
		for _, text := range messages {
			if strings.Contains(text, needle) {
				found = true
				break
			}
		}
		if !found {
			m.t.Fatalf("messages missing %q: %v", needle, messages)
		}
	}
	for _, needle := range m.wantMessagesOmit {
		for _, text := range messages {
			if strings.Contains(text, needle) {
				m.t.Fatalf("messages still contain summarized text %q: %v", needle, messages)
			}
		}
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, m.replyText),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			},
		}, nil)
	}
}

type modelCheckpointProbe struct {
	t               *testing.T
	compactionCalls int
	normalCalls     int
}

func (m *modelCheckpointProbe) Name() string { return "model-checkpoint-probe" }

func (m *modelCheckpointProbe) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	instructions := requestInstructionsText(req)
	if strings.Contains(instructions, "CONTEXT CHECKPOINT COMPACTION") {
		m.compactionCalls++
		body := `CONTEXT CHECKPOINT

## Objective
- model checkpoint objective

## User Constraints
- do not lose blocker continuity

## Durable Decisions
- compact before each turn when budget is exceeded

## Verified Facts
- provider intermittently returns 529 overloaded_error

## Current Progress
- checkpoint builder is being implemented

## Open Questions / Risks
- summary quality can drift if prompts are too generic

## Next Actions
1. run realistic compact e2e tests and tune the summary prompt

## Active Tasks
- none

## Active Participants
- none

## Latest Blockers
- checkpoint quality drops when summaries become too generic

## Operational Notes
- files touched: impl/agent/local/runtime.go
- commands run: go test ./ports/...`
		return func(yield func(*model.StreamEvent, error) bool) {
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, body),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
				},
			}, nil)
		}
	}
	m.normalCalls++
	found := false
	for _, text := range requestMessageTexts(req) {
		if strings.Contains(text, "model checkpoint objective") {
			found = true
			break
		}
	}
	if !found {
		m.t.Fatalf("normal call messages missing canonical checkpoint objective: %v", requestMessageTexts(req))
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "ok"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			},
		}, nil)
	}
}

type overflowRecoveryModel struct {
	t                    *testing.T
	calls                int
	compactionCalls      int
	sawCheckpointOnRetry bool
}

func (m *overflowRecoveryModel) Name() string { return "overflow-recovery" }

func (m *overflowRecoveryModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	instructions := requestInstructionsText(req)
	if strings.Contains(instructions, "CONTEXT CHECKPOINT COMPACTION") {
		m.compactionCalls++
		compactionInput := strings.Join(requestMessageTexts(req), "\n")
		if !strings.Contains(compactionInput, "## Tool Result") ||
			!strings.Contains(compactionInput, "tool: ECHO") ||
			!strings.Contains(compactionInput, "policy_action: deny") {
			m.t.Fatalf("compaction input missing tool result continuity: %q", compactionInput)
		}
		body := `CONTEXT CHECKPOINT

Objective: finish the tool-assisted turn after overflow
Blocker: normal prompt overflowed after the tool denial result
Next action: resume from the compact checkpoint and return the final answer

## Current Progress
- the ECHO tool result was denied by workspace-write policy

## Next Actions
1. resume from the compact checkpoint and return the final answer`
		return func(yield func(*model.StreamEvent, error) bool) {
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, body),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
				},
			}, nil)
		}
	}
	if requestHasToolResult(req, "ECHO") {
		return func(yield func(*model.StreamEvent, error) bool) {
			yield(nil, &model.ContextOverflowError{Cause: errors.New("prompt is too long after tool loop")})
		}
	}
	for _, text := range requestMessageTexts(req) {
		if strings.Contains(text, "CONTEXT CHECKPOINT") && strings.Contains(strings.ToLower(text), "workspace-write policy") {
			m.sawCheckpointOnRetry = true
			return func(yield func(*model.StreamEvent, error) bool) {
				yield(&model.StreamEvent{
					Type: model.StreamEventTurnDone,
					Response: &model.Response{
						Message:      model.NewTextMessage(model.RoleAssistant, "recovered after compact"),
						TurnComplete: true,
						StepComplete: true,
						Status:       model.ResponseStatusCompleted,
					},
				}, nil)
			}
		}
	}
	if m.calls != 1 {
		m.t.Fatalf("unexpected non-compaction request without checkpoint: %v", requestMessageTexts(req))
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
					ID:   "call-overflow-1",
					Name: "ECHO",
					Args: string(mustJSONRaw(tmap("value", "pong"))),
				}}, ""),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonToolCalls,
			},
		}, nil)
	}
}

func requestInstructionsText(req *model.Request) string {
	if req == nil {
		return ""
	}
	parts := make([]string, 0, len(req.Instructions))
	for _, part := range req.Instructions {
		if part.Text != nil && strings.TrimSpace(part.Text.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text.Text))
		}
	}
	return strings.Join(parts, "\n")
}

func requestMessageTexts(req *model.Request) []string {
	if req == nil {
		return nil
	}
	out := make([]string, 0, len(req.Messages))
	for _, message := range req.Messages {
		if text := strings.TrimSpace(message.TextContent()); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func requestHasToolResult(req *model.Request, name string) bool {
	if req == nil {
		return false
	}
	for _, message := range req.Messages {
		for _, result := range message.ToolResults() {
			if strings.EqualFold(strings.TrimSpace(result.Name), strings.TrimSpace(name)) {
				return true
			}
		}
	}
	return false
}

func latestCompactEventForTest(events []*session.Event) (*session.Event, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i] != nil && events[i].Type == session.EventTypeCompact {
			return events[i], true
		}
	}
	return nil, false
}

func eventTextsForTest(events []*session.Event) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		if text := strings.TrimSpace(session.EventText(event)); text != "" {
			out = append(out, text)
		}
	}
	return out
}

type denyWriteRuntimeModel struct{ calls int }

func (m *denyWriteRuntimeModel) Name() string { return "deny-write" }

func (m *denyWriteRuntimeModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	return func(yield func(*model.StreamEvent, error) bool) {
		if callIndex == 1 {
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "write-1",
						Name: filesystem.WriteToolName,
						Args: string(mustJSONRaw(map[string]any{"path": policyOutsidePathForRuntimeTest(), "content": "x"})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
			return
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "denied"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonStop,
			},
		}, nil)
	}
}

type denyCommandRuntimeModel struct{ calls int }

func (m *denyCommandRuntimeModel) Name() string { return "deny-command" }

func (m *denyCommandRuntimeModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	return func(yield func(*model.StreamEvent, error) bool) {
		if callIndex == 1 {
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "command-1",
						Name: shell.RunCommandToolName,
						Args: string(mustJSONRaw(map[string]any{"command": "rm -rf /"})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
			return
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "blocked"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonStop,
			},
		}, nil)
	}
}

type approveEscalatedCommandRuntimeModel struct {
	calls   int
	command string
}

func (m *approveEscalatedCommandRuntimeModel) Name() string { return "approve-escalated-command" }

func (m *approveEscalatedCommandRuntimeModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	return func(yield func(*model.StreamEvent, error) bool) {
		if callIndex == 1 {
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "command-approve-1",
						Name: shell.RunCommandToolName,
						Args: string(mustJSONRaw(map[string]any{
							"command":             m.command,
							"workdir":             ".",
							"yield_time_ms":       shellCompletionYieldMillisForTest(200),
							"sandbox_permissions": "require_escalated",
							"justification":       "Do you want to run this command outside the sandbox?",
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
			return
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "done"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonStop,
			},
		}, nil)
	}
}

func shellQuoteForTest(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func powershellQuoteForTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func shellWriteFileForTest(path string, content string) string {
	if goruntime.GOOS == "windows" {
		return "[IO.File]::WriteAllText(" + powershellQuoteForTest(path) + ", " + powershellQuoteForTest(content) + ")"
	}
	return "printf " + shellQuoteForTest(content) + " > " + shellQuoteForTest(path)
}

func shellSleepThenPrintForTest(text string, delay time.Duration) string {
	if goruntime.GOOS == "windows" {
		ms := delay.Milliseconds()
		if ms <= 0 {
			ms = 1
		}
		return fmt.Sprintf("Start-Sleep -Milliseconds %d; [Console]::Out.WriteLine(%s)", ms, powershellQuoteForTest(text))
	}
	seconds := float64(delay) / float64(time.Second)
	return fmt.Sprintf("sleep %.3f; printf %s", seconds, shellQuoteForTest(text))
}

func shellPrintThenSleepForTest(text string, delay time.Duration) string {
	if goruntime.GOOS == "windows" {
		ms := delay.Milliseconds()
		if ms <= 0 {
			ms = 1
		}
		return fmt.Sprintf("[Console]::Out.Write(%s); Start-Sleep -Milliseconds %d", powershellQuoteForTest(text), ms)
	}
	seconds := float64(delay) / float64(time.Second)
	return fmt.Sprintf("printf %s; sleep %.3f", shellQuoteForTest(text), seconds)
}

func shellInteractiveGreetingForTest() string {
	if goruntime.GOOS == "windows" {
		return "[Console]::Out.WriteLine('waiting'); $name = [Console]::In.ReadLine(); [Console]::Out.WriteLine('hello ' + $name)"
	}
	return "printf 'waiting\n'; read name; printf 'hello %s\n' \"$name\""
}

func shellRunningYieldMillisForTest(fallback int) int {
	if goruntime.GOOS == "windows" {
		return 100
	}
	return fallback
}

func shellCompletionYieldMillisForTest(fallback int) int {
	if goruntime.GOOS == "windows" {
		return 7000
	}
	return fallback
}

func shellAsyncDelayForTest() time.Duration {
	if goruntime.GOOS == "windows" {
		return time.Second
	}
	return 50 * time.Millisecond
}

func policyOutsidePathForRuntimeTest() string {
	if goruntime.GOOS == "windows" {
		return `C:\outside\blocked.txt`
	}
	return "/etc/blocked.txt"
}

func jsonStringForTest(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

type commandTaskLoopRuntimeModel struct {
	t      *testing.T
	calls  int
	taskID string
}

func (m *commandTaskLoopRuntimeModel) Name() string { return "command-task-loop" }

func (m *commandTaskLoopRuntimeModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	if callIndex == 2 {
		m.taskID = mustFindTaskID(m.t, req)
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		switch callIndex {
		case 1:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "command-async-1",
						Name: shell.RunCommandToolName,
						Args: string(mustJSONRaw(map[string]any{
							"command":       shellSleepThenPrintForTest("async command done", shellAsyncDelayForTest()),
							"workdir":       ".",
							"yield_time_ms": shellRunningYieldMillisForTest(5),
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		case 2:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "task-wait-1",
						Name: tasktool.ToolName,
						Args: string(mustJSONRaw(map[string]any{
							"action":        "wait",
							"task_id":       m.taskID,
							"yield_time_ms": shellCompletionYieldMillisForTest(250),
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		default:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, "async command done"),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonStop,
				},
			}, nil)
		}
	}
}

func mustFindTaskID(t *testing.T, req *model.Request) string {
	t.Helper()
	if req == nil {
		t.Fatal("request = nil")
		return ""
	}
	for _, message := range req.Messages {
		for _, result := range message.ToolResults() {
			for _, part := range result.Content {
				if part.Kind != model.PartKindJSON || part.JSON == nil {
					continue
				}
				var payload map[string]any
				if err := json.Unmarshal(part.JSONValue(), &payload); err != nil {
					continue
				}
				if taskID, _ := payload["task_id"].(string); strings.TrimSpace(taskID) != "" {
					return strings.TrimSpace(taskID)
				}
			}
		}
	}
	raw, _ := json.MarshalIndent(req, "", "  ")
	t.Fatalf("did not find yielded task_id in request transcript:\n%s", string(raw))
	return ""
}

type spawnTaskLoopRuntimeModel struct {
	t      *testing.T
	calls  int
	taskID string
}

type spawnApprovalTaskLoopRuntimeModel struct {
	t      *testing.T
	agent  string
	calls  int
	taskID string
}

type spawnProbeTaskLoopRuntimeModel struct {
	t      *testing.T
	calls  int
	taskID string
}

func (m *spawnTaskLoopRuntimeModel) Name() string { return "spawn-task-loop" }

func (m *spawnApprovalTaskLoopRuntimeModel) Name() string { return "spawn-approval-task-loop" }

func (m *spawnProbeTaskLoopRuntimeModel) Name() string { return "spawn-probe-task-loop" }

func (m *spawnTaskLoopRuntimeModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	if callIndex == 2 {
		m.taskID = mustFindTaskID(m.t, req)
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		switch callIndex {
		case 1:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "spawn-1",
						Name: spawn.ToolName,
						Args: string(mustJSONRaw(map[string]any{
							"agent":  "self",
							"prompt": "Reply with exactly: spawn child ok",
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		case 2:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "task-wait-spawn-1",
						Name: tasktool.ToolName,
						Args: string(mustJSONRaw(map[string]any{
							"action":        "wait",
							"task_id":       m.taskID,
							"yield_time_ms": 300,
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		default:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, "spawn child ok"),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonStop,
				},
			}, nil)
		}
	}
}

func (m *spawnApprovalTaskLoopRuntimeModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	if callIndex == 2 {
		m.taskID = mustFindTaskID(m.t, req)
	}
	agent := strings.TrimSpace(m.agent)
	if agent == "" {
		agent = "codex"
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		switch callIndex {
		case 1:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "spawn-approval-1",
						Name: spawn.ToolName,
						Args: string(mustJSONRaw(map[string]any{
							"agent":  agent,
							"prompt": "Run the approval flow and reply with exactly: child approval ok",
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		case 2:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "task-wait-spawn-approval-1",
						Name: tasktool.ToolName,
						Args: string(mustJSONRaw(map[string]any{
							"action":        "wait",
							"task_id":       m.taskID,
							"yield_time_ms": 600,
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		default:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, "child approval ok"),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonStop,
				},
			}, nil)
		}
	}
}

func (m *spawnProbeTaskLoopRuntimeModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	if callIndex == 2 {
		m.taskID = mustFindTaskID(m.t, req)
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		switch callIndex {
		case 1:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "spawn-probe-1",
						Name: spawn.ToolName,
						Args: string(mustJSONRaw(map[string]any{
							"agent":  "self",
							"prompt": "Check whether SPAWN is available and reply with exactly the result.",
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		case 2:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "task-wait-spawn-probe-1",
						Name: tasktool.ToolName,
						Args: string(mustJSONRaw(map[string]any{
							"action":        "wait",
							"task_id":       m.taskID,
							"yield_time_ms": 300,
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		default:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, "spawn disabled"),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonStop,
				},
			}, nil)
		}
	}
}

func mustSessionTaskID(t *testing.T, events []*session.Event) string {
	t.Helper()
	for _, event := range events {
		if event == nil {
			continue
		}
		if taskID := taskIDFromSessionEvent(event); strings.TrimSpace(taskID) != "" {
			return taskID
		}
	}
	t.Fatal("did not find task_id in persisted session events")
	return ""
}

func eventToolRawOutput(event *session.Event) map[string]any {
	if toolPayload := session.EventToolProjection(event); toolPayload != nil {
		return toolPayload.Output
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		return update.RawOutput
	}
	return nil
}

func eventToolRawInput(event *session.Event) map[string]any {
	if toolPayload := session.EventToolProjection(event); toolPayload != nil {
		return toolPayload.Input
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		return update.RawInput
	}
	return nil
}

func taskIDFromSessionEvent(event *session.Event) string {
	for _, values := range []map[string]any{eventToolRawOutput(event), eventToolRawInput(event)} {
		if taskID, _ := values["task_id"].(string); strings.TrimSpace(taskID) != "" {
			return strings.TrimSpace(taskID)
		}
	}
	return ""
}

func terminalFramesText(frames []stream.Frame) string {
	var out strings.Builder
	for _, frame := range frames {
		out.WriteString(frame.Text)
	}
	return out.String()
}

type approvalRequesterFunc func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error)

func (f approvalRequesterFunc) RequestApproval(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
	return f(ctx, req)
}

type attemptFactory struct {
	mu     sync.Mutex
	agents []agent.Agent
	specs  []agent.AgentSpec
	calls  int
}

func (f *attemptFactory) NewAgent(_ context.Context, spec agent.AgentSpec) (agent.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls >= len(f.agents) {
		return nil, errors.New("no more agents configured")
	}
	f.specs = append(f.specs, spec)
	agent := f.agents[f.calls]
	f.calls++
	return agent, nil
}

func (f *attemptFactory) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *attemptFactory) Specs() []agent.AgentSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]agent.AgentSpec, len(f.specs))
	copy(out, f.specs)
	return out
}

type seqAgent struct {
	events []*session.Event
	err    error
}

func (a seqAgent) Name() string { return "seq" }

func (a seqAgent) Run(agent.Context) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for _, event := range a.events {
			if !yield(session.CloneEvent(event), nil) {
				return
			}
		}
		if a.err != nil {
			yield(nil, a.err)
		}
	}
}

type yieldProbeSandboxRuntime struct {
	session *yieldProbeSandboxSession
}

func (r *yieldProbeSandboxRuntime) Describe() sandbox.Descriptor {
	return sandbox.Descriptor{
		Backend:   sandbox.BackendHost,
		Isolation: sandbox.IsolationHost,
		Capabilities: sandbox.CapabilitySet{
			CommandExec:   true,
			AsyncSessions: true,
		},
	}
}

func (r *yieldProbeSandboxRuntime) FileSystem() sandbox.FileSystem { return nil }

func (r *yieldProbeSandboxRuntime) FileSystemFor(sandbox.Constraints) sandbox.FileSystem {
	return nil
}

func (r *yieldProbeSandboxRuntime) Run(context.Context, sandbox.CommandRequest) (sandbox.CommandResult, error) {
	return sandbox.CommandResult{}, nil
}

func (r *yieldProbeSandboxRuntime) Start(_ context.Context, req sandbox.CommandRequest) (sandbox.Session, error) {
	if r.session == nil {
		r.session = newYieldProbeSandboxSession()
	}
	r.session.command = req.Command
	r.session.workdir = req.Dir
	r.session.timeout = req.Timeout
	r.session.onOutput = req.OnOutput
	return r.session, nil
}

func (r *yieldProbeSandboxRuntime) OpenSession(string) (sandbox.Session, error) {
	if r.session == nil {
		r.session = newYieldProbeSandboxSession()
	}
	return r.session, nil
}

func (r *yieldProbeSandboxRuntime) OpenSessionRef(ref sandbox.SessionRef) (sandbox.Session, error) {
	return r.OpenSession(ref.SessionID)
}

func (r *yieldProbeSandboxRuntime) SupportedBackends() []sandbox.Backend {
	return []sandbox.Backend{sandbox.BackendHost}
}

func (r *yieldProbeSandboxRuntime) Status() sandbox.Status {
	return sandbox.Status{
		RequestedBackend: sandbox.BackendHost,
		ResolvedBackend:  sandbox.BackendHost,
	}
}

func (r *yieldProbeSandboxRuntime) Close() error { return nil }

type yieldProbeSandboxSession struct {
	command       string
	workdir       string
	timeout       time.Duration
	lastWait      time.Duration
	waitErr       error
	statusRunning *bool
	terminated    bool
	stdout        string
	stderr        string
	result        sandbox.CommandResult
	resultErr     error
	onOutput      func(sandbox.OutputChunk)
}

func newYieldProbeSandboxSession() *yieldProbeSandboxSession {
	return &yieldProbeSandboxSession{}
}

func (s *yieldProbeSandboxSession) Ref() sandbox.SessionRef {
	return sandbox.SessionRef{Backend: sandbox.BackendHost, SessionID: "yield-probe-session"}
}

func (s *yieldProbeSandboxSession) Terminal() sandbox.TerminalRef {
	return sandbox.TerminalRef{
		Backend:    sandbox.BackendHost,
		SessionID:  "yield-probe-session",
		TerminalID: "yield-probe-terminal",
	}
}

func (s *yieldProbeSandboxSession) WriteInput(context.Context, []byte) error { return nil }

func (s *yieldProbeSandboxSession) ReadOutput(_ context.Context, stdoutCursor int64, stderrCursor int64) ([]byte, []byte, int64, int64, error) {
	stdout, nextStdout := probeOutputFromCursor(s.stdout, stdoutCursor)
	stderr, nextStderr := probeOutputFromCursor(s.stderr, stderrCursor)
	return stdout, stderr, nextStdout, nextStderr, nil
}

func (s *yieldProbeSandboxSession) Status(context.Context) (sandbox.SessionStatus, error) {
	running := true
	if s.statusRunning != nil {
		running = *s.statusRunning
	}
	exitCode := 0
	if !running {
		exitCode = s.result.ExitCode
	}
	return sandbox.SessionStatus{
		SessionRef:    s.Ref(),
		Terminal:      s.Terminal(),
		Running:       running,
		ExitCode:      exitCode,
		SupportsInput: true,
		UpdatedAt:     time.Now(),
	}, nil
}

func (s *yieldProbeSandboxSession) Wait(_ context.Context, timeout time.Duration) (sandbox.SessionStatus, error) {
	s.lastWait = timeout
	if s.waitErr != nil {
		return sandbox.SessionStatus{}, s.waitErr
	}
	return s.Status(context.Background())
}

func (s *yieldProbeSandboxSession) Result(context.Context) (sandbox.CommandResult, error) {
	return s.result, s.resultErr
}

func (s *yieldProbeSandboxSession) Terminate(context.Context) error {
	s.terminated = true
	return nil
}

func probeOutputFromCursor(text string, cursor int64) ([]byte, int64) {
	raw := []byte(text)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > int64(len(raw)) {
		cursor = int64(len(raw))
	}
	return raw[cursor:], int64(len(raw))
}

type runningOnlyProbeSandboxSession struct {
	lastWait time.Duration
}

type runningOnlyProbeSandboxRuntime struct {
	session *runningOnlyProbeSandboxSession
}

func (s *runningOnlyProbeSandboxSession) Ref() sandbox.SessionRef {
	return sandbox.SessionRef{Backend: sandbox.BackendHost, SessionID: "running-only-session"}
}

func (s *runningOnlyProbeSandboxSession) Terminal() sandbox.TerminalRef {
	return sandbox.TerminalRef{
		Backend:    sandbox.BackendHost,
		SessionID:  "running-only-session",
		TerminalID: "running-only-terminal",
	}
}

func (s *runningOnlyProbeSandboxSession) WriteInput(context.Context, []byte) error { return nil }

func (s *runningOnlyProbeSandboxSession) ReadOutput(context.Context, int64, int64) ([]byte, []byte, int64, int64, error) {
	return nil, nil, 0, 0, nil
}

func (s *runningOnlyProbeSandboxSession) Status(context.Context) (sandbox.SessionStatus, error) {
	return sandbox.SessionStatus{
		SessionRef:    s.Ref(),
		Terminal:      s.Terminal(),
		Running:       true,
		SupportsInput: true,
		UpdatedAt:     time.Now(),
	}, nil
}

func (s *runningOnlyProbeSandboxSession) Wait(_ context.Context, timeout time.Duration) (sandbox.SessionStatus, error) {
	s.lastWait = timeout
	return s.Status(context.Background())
}

func (s *runningOnlyProbeSandboxSession) Result(context.Context) (sandbox.CommandResult, error) {
	panic("waitCommand should not request Result while task is still running")
}

func (s *runningOnlyProbeSandboxSession) Terminate(context.Context) error { return nil }

func (r *runningOnlyProbeSandboxRuntime) Describe() sandbox.Descriptor {
	return sandbox.Descriptor{
		Backend:   sandbox.BackendHost,
		Isolation: sandbox.IsolationHost,
		Capabilities: sandbox.CapabilitySet{
			CommandExec:   true,
			AsyncSessions: true,
		},
	}
}

func (r *runningOnlyProbeSandboxRuntime) FileSystem() sandbox.FileSystem { return nil }

func (r *runningOnlyProbeSandboxRuntime) FileSystemFor(sandbox.Constraints) sandbox.FileSystem {
	return nil
}

func (r *runningOnlyProbeSandboxRuntime) Run(context.Context, sandbox.CommandRequest) (sandbox.CommandResult, error) {
	return sandbox.CommandResult{}, nil
}

func (r *runningOnlyProbeSandboxRuntime) Start(_ context.Context, _ sandbox.CommandRequest) (sandbox.Session, error) {
	if r.session == nil {
		r.session = &runningOnlyProbeSandboxSession{}
	}
	return r.session, nil
}

func (r *runningOnlyProbeSandboxRuntime) OpenSession(string) (sandbox.Session, error) {
	if r.session == nil {
		r.session = &runningOnlyProbeSandboxSession{}
	}
	return r.session, nil
}

func (r *runningOnlyProbeSandboxRuntime) OpenSessionRef(ref sandbox.SessionRef) (sandbox.Session, error) {
	return r.OpenSession(ref.SessionID)
}

func (r *runningOnlyProbeSandboxRuntime) SupportedBackends() []sandbox.Backend {
	return []sandbox.Backend{sandbox.BackendHost}
}

func (r *runningOnlyProbeSandboxRuntime) Status() sandbox.Status {
	return sandbox.Status{
		RequestedBackend: sandbox.BackendHost,
		ResolvedBackend:  sandbox.BackendHost,
	}
}

func (r *runningOnlyProbeSandboxRuntime) Close() error { return nil }

func newRuntimeRunCommandToolTestHarness(t *testing.T) (session.Service, session.Session, *Runtime) {
	t.Helper()

	sessions, activeSession := newTestSessionService(t, "sess-command-yield-default")
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		DefaultPolicyMode: presets.ModeAutoReview,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return sessions, activeSession, runtime
}

func mustRuntimeRunCommandTool(t *testing.T, runtime sandbox.Runtime) tool.Tool {
	t.Helper()

	targetTool, err := shell.NewRunCommand(shell.RunCommandConfig{Runtime: runtime})
	if err != nil {
		t.Fatalf("shell.NewRunCommand() error = %v", err)
	}
	return targetTool
}

func callRuntimeRunCommandTool(t *testing.T, runCommandTool runtimeCommandTool, args map[string]any) tool.Result {
	t.Helper()

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := runCommandTool.Call(context.Background(), tool.Call{
		ID:    "command-yield-test",
		Name:  shell.RunCommandToolName,
		Input: raw,
	})
	if err != nil {
		t.Fatalf("runCommandTool.Call() error = %v", err)
	}
	return result
}

func callRuntimeTaskTool(t *testing.T, taskTool runtimeTaskTool, args map[string]any) tool.Result {
	t.Helper()

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := taskTool.Call(context.Background(), tool.Call{
		ID:    "task-control-test",
		Name:  tasktool.ToolName,
		Input: raw,
	})
	if err != nil {
		t.Fatalf("taskTool.Call() error = %v", err)
	}
	return result
}

func testToolResultPayload(t *testing.T, result tool.Result) map[string]any {
	t.Helper()
	if len(result.Content) == 0 || result.Content[0].JSON == nil {
		t.Fatalf("result.Content = %#v, want JSON payload", result.Content)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("unmarshal result payload: %v", err)
	}
	return payload
}

func testToolResultRuntimeMeta(t *testing.T, result tool.Result, section string) map[string]any {
	t.Helper()
	caelis, _ := result.Metadata["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	values, _ := runtimeMeta[section].(map[string]any)
	if values == nil {
		t.Fatalf("result.Metadata caelis.runtime.%s = %#v", section, result.Metadata)
	}
	return values
}

func assertRunningTaskSnapshot(t *testing.T, result tool.Result) {
	t.Helper()

	if len(result.Content) == 0 {
		t.Fatal("result.Content = empty, want task snapshot payload")
	}
	part := result.Content[0]
	if part.Kind != model.PartKindJSON || part.JSON == nil {
		t.Fatalf("result.Content[0] = %#v, want json part", part)
	}
	var payload map[string]any
	if err := json.Unmarshal(part.JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(snapshot) error = %v", err)
	}
	if got, _ := payload["state"].(string); got != string(taskapi.StateRunning) {
		t.Fatalf("snapshot state = %q, want %q", got, taskapi.StateRunning)
	}
	if strings.TrimSpace(testStringValue(payload["task_id"])) == "" {
		t.Fatalf("snapshot task_id missing: %#v", payload)
	}
}

func testStringValue(raw any) string {
	text, _ := raw.(string)
	return strings.TrimSpace(text)
}
