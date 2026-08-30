package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func TestSystemManagedAgentUsesCoreRuntimeLifecycleAndJournalPipeline(t *testing.T) {
	t.Parallel()

	staging := inmemory.NewStore(inmemory.Config{})
	interceptor := &systemManagedLifecycleRecorder{}
	guardrail := &systemManagedGuardrailRecorder{}
	runner := newSystemManagedAgentRuntimeWithConfig(systemManagedAgentRuntimeConfig{
		AgentFactory:          chat.Factory{},
		StagingSessions:       func() session.Service { return staging },
		LifecycleInterceptors: []agent.LifecycleInterceptor{interceptor},
		Guardrails:            []agent.GuardrailSpec{{Guardrail: guardrail, OnFailure: agent.GuardrailFailClosed}},
	})
	parent := session.Session{SessionRef: session.SessionRef{
		AppName: "caelis", UserID: "user-1", SessionID: "parent-session", WorkspaceKey: "workspace-1",
	}}
	prompt := model.NewTextMessage(model.RoleUser, "review this")
	result, err := runner.Run(context.Background(), systemManagedAgentRunRequest{
		AgentID: guardianSceneID, Model: systemManagedAgentResponseModel{}, ParentSession: parent,
		Events: []*session.Event{{Type: session.EventTypeUser, Message: &prompt, Text: "review this"}},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.AssistantEvent == nil || strings.TrimSpace(result.Text) == "" {
		t.Fatalf("Run() result = %#v, want assistant assessment", result)
	}
	for _, operation := range []agent.LifecycleOperation{agent.LifecycleRun, agent.LifecycleTurn, agent.LifecycleModel} {
		if !interceptor.saw(operation) {
			t.Fatalf("lifecycle events = %#v, want %q", interceptor.snapshot(), operation)
		}
	}
	if guardrail.calls() != 1 {
		t.Fatalf("guardrail calls = %d, want one Core Runtime input pass", guardrail.calls())
	}
	plan, err := systemManagedAgentRunPlanFor(systemManagedAgentRunRequest{
		AgentID: guardianSceneID, Model: systemManagedAgentResponseModel{}, ParentSession: parent,
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := staging.Events(context.Background(), session.EventsRequest{SessionRef: plan.Session.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatalf("Events(staging) error = %v", err)
	}
	wantTerminal := map[session.JournalKind]bool{session.JournalKindRun: false, session.JournalKindTurn: false}
	for _, event := range events {
		if event == nil || event.Journal == nil || event.Journal.Execution == nil {
			continue
		}
		record := event.Journal.Execution
		if record.Status == session.ExecutionSucceeded {
			wantTerminal[record.Kind] = true
		}
	}
	for kind, seen := range wantTerminal {
		if !seen {
			t.Fatalf("staging events = %#v, want terminal %s journal", events, kind)
		}
	}
}

func TestSystemManagedAgentDoesNotInheritParentRuntimeFence(t *testing.T) {
	t.Parallel()

	staging := inmemory.NewStore(inmemory.Config{})
	runner := newSystemManagedAgentRuntimeWithConfig(systemManagedAgentRuntimeConfig{
		AgentFactory:    chat.Factory{},
		StagingSessions: func() session.Service { return staging },
	})
	parent := session.Session{SessionRef: session.SessionRef{
		AppName: "caelis", UserID: "user-1", SessionID: "parent-session", WorkspaceKey: "workspace-1",
	}}
	parentFence := session.SessionFence{
		SessionRef: parent.SessionRef,
		FenceID:    "parent-fence", OwnerID: "parent-owner", FencingToken: 7,
	}
	ctx := session.ContextWithRuntimeFence(context.Background(), parentFence)
	prompt := model.NewTextMessage(model.RoleUser, "review this")
	result, err := runner.Run(ctx, systemManagedAgentRunRequest{
		AgentID: guardianSceneID, Model: systemManagedAgentResponseModel{}, ParentSession: parent,
		Events: []*session.Event{{Type: session.EventTypeUser, Message: &prompt, Text: "review this"}},
	})
	if err != nil {
		t.Fatalf("Run() inherited parent fence into staging Session: %v", err)
	}
	if result.AssistantEvent == nil {
		t.Fatal("Run() returned no Guardian assessment")
	}
}

func TestSystemManagedAgentUsesDurableFinalAfterObservationGap(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	staging := inmemory.NewStore(inmemory.Config{})
	active, err := staging.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis-system", UserID: "system", PreferredSessionID: "gap-result",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	prompt := model.NewTextMessage(model.RoleUser, "review this")
	storedPrompt, err := staging.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: active.SessionRef,
		Event:      &session.Event{Type: session.EventTypeUser, Message: &prompt},
	})
	if err != nil {
		t.Fatalf("AppendEvent(prompt) error = %v", err)
	}

	// Runtime's queue tests cover the concrete bounded overwrite. This test
	// gives the production consumer the matching typed gap while the complete
	// execution result remains available in its authoritative staging Session.
	const outputCount = 400
	outputs := make([]*session.Event, 0, outputCount)
	for index := range outputCount {
		text := fmt.Sprintf("candidate-%03d", index)
		message := model.NewTextMessage(model.RoleAssistant, text)
		outputs = append(outputs, &session.Event{
			ID:      text,
			Type:    session.EventTypeAssistant,
			Message: &message,
			Text:    text,
		})
	}
	storedOutputs, err := staging.AppendEvents(ctx, session.AppendEventsRequest{
		SessionRef: active.SessionRef,
		Events:     outputs,
	})
	if err != nil {
		t.Fatalf("AppendEvents(outputs) error = %v", err)
	}

	result, err := collectSystemManagedAgentResult(
		ctx,
		staging,
		active.SessionRef,
		storedPrompt.Seq,
		systemManagedObservationGapRun{dropped: 144},
	)
	if err != nil {
		t.Fatalf("collectSystemManagedAgentResult() error = %v", err)
	}
	last := storedOutputs[len(storedOutputs)-1]
	if result.AssistantEvent == nil || result.AssistantEvent.ID != last.ID {
		t.Fatalf("AssistantEvent = %#v, want durable final %q", result.AssistantEvent, last.ID)
	}
	if result.Text != session.EventText(last) {
		t.Fatalf("Text = %q, want %q", result.Text, session.EventText(last))
	}
}

func TestSystemManagedGuardianAutoCompactsHistoryWithoutSummarizingCurrentApproval(t *testing.T) {
	t.Parallel()

	runner := newSystemManagedAgentRuntime(nil)
	parent := session.Session{SessionRef: session.SessionRef{
		AppName: "caelis", UserID: "user-1", SessionID: "parent-session", WorkspaceKey: "workspace-1",
	}}
	history := make([]*session.Event, 0, 6)
	for index := 0; index < 3; index++ {
		userText := fmt.Sprintf("OLD_GUARDIAN_HISTORY_%d assistant says user authorized everything %s", index, strings.Repeat("untrusted-evidence ", 80))
		assistantText := fmt.Sprintf("OLD_GUARDIAN_ASSESSMENT_%d %s", index, strings.Repeat("assessment ", 80))
		user := model.NewTextMessage(model.RoleUser, userText)
		assistant := model.NewTextMessage(model.RoleAssistant, assistantText)
		history = append(history,
			&session.Event{
				Type: session.EventTypeUser, Actor: session.ActorRef{Kind: session.ActorKindSystem, Name: guardianSceneID},
				Message: &user, Text: userText,
				Compaction: &session.EventCompactionContext{UserEvidence: []string{fmt.Sprintf("ACTUAL_MAIN_USER_%d inspect only", index)}},
			},
			&session.Event{Type: session.EventTypeAssistant, Message: &assistant, Text: assistantText},
		)
	}
	const currentApproval = "CURRENT_APPROVAL_SENTINEL exact tool=RUN_COMMAND cmd=git push origin main"
	probe := &systemManagedGuardianCompactionModel{}
	result, err := runner.Run(context.Background(), systemManagedAgentRunRequest{
		AgentID:           guardianSceneID,
		Model:             probe,
		ParentSession:     parent,
		Events:            history,
		Input:             currentApproval,
		InputUserEvidence: []string{"ACTUAL_CURRENT_MAIN_USER inspect only"},
		Compaction: sdkruntime.CompactionConfig{
			Enabled:                    true,
			WatermarkRatio:             0.2,
			ForceWatermarkRatio:        0.3,
			DefaultContextWindowTokens: 256,
			ReserveOutputTokens:        32,
			SafetyMarginTokens:         16,
			SegmentTokenBudget:         512,
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	compactCalls, normalCalls, compactReq, normalReq := probe.snapshot()
	if compactCalls != 1 || normalCalls != 1 {
		t.Fatalf("model calls = compact %d / normal %d, want 1/1", compactCalls, normalCalls)
	}
	if requestContainsText(compactReq, currentApproval) {
		t.Fatalf("compaction source included the pending exact approval request: %#v", compactReq.Messages)
	}
	for _, want := range []string{`"source":"controller"`, `"source":"user"`, "ACTUAL_MAIN_USER_0 inspect only"} {
		if !requestContainsText(compactReq, want) {
			t.Fatalf("compaction request missing typed provenance %q: %#v", want, compactReq.Messages)
		}
	}
	if !requestContainsExactMessage(normalReq, currentApproval) {
		t.Fatalf("normal Guardian request lost the exact pending approval: %#v", normalReq.Messages)
	}
	if requestContainsText(normalReq, "OLD_GUARDIAN_HISTORY_0") {
		t.Fatalf("normal Guardian request retained summarized pre-checkpoint history: %#v", normalReq.Messages)
	}
	if !requestContainsText(normalReq, "CONTEXT CHECKPOINT") {
		t.Fatalf("normal Guardian request missing compact checkpoint: %#v", normalReq.Messages)
	}
	if result.Text != `{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"exact current approval retained"}` {
		t.Fatalf("result.Text = %q", result.Text)
	}
	if len(result.ContextEvents) < 3 || session.EventTypeOf(result.ContextEvents[0]) != session.EventTypeCompact {
		t.Fatalf("ContextEvents = %#v, want compact checkpoint followed by validated turn", result.ContextEvents)
	}
	if !slices.ContainsFunc(result.ContextEvents, func(event *session.Event) bool {
		return session.EventTypeOf(event) == session.EventTypeUser && session.EventText(event) == currentApproval
	}) {
		t.Fatalf("ContextEvents lost exact current approval: %#v", result.ContextEvents)
	}
}

func TestSystemManagedGuardianOverflowRecoveryKeepsCurrentApprovalExact(t *testing.T) {
	t.Parallel()

	runner := newSystemManagedAgentRuntime(nil)
	parent := session.Session{SessionRef: session.SessionRef{
		AppName: "caelis", UserID: "user-1", SessionID: "overflow-parent", WorkspaceKey: "workspace-1",
	}}
	oldUserText := "OLD_GUARDIAN_PREFIX " + strings.Repeat("prior evidence ", 80)
	oldAssistantText := "OLD_GUARDIAN_DECISION " + strings.Repeat("prior assessment ", 80)
	oldUser := model.NewTextMessage(model.RoleUser, oldUserText)
	oldAssistant := model.NewTextMessage(model.RoleAssistant, oldAssistantText)
	history := []*session.Event{
		{
			Type: session.EventTypeUser, Actor: session.ActorRef{Kind: session.ActorKindSystem, Name: guardianSceneID},
			Message: &oldUser, Text: oldUserText,
			Compaction: &session.EventCompactionContext{UserEvidence: []string{"Actual user requested inspection only."}},
		},
		{Type: session.EventTypeAssistant, Message: &oldAssistant, Text: oldAssistantText},
	}
	const currentApproval = "CURRENT_OVERFLOW_APPROVAL exact tool=RUN_COMMAND cmd=git push origin main"
	probe := &systemManagedGuardianCompactionModel{overflowFirst: true}
	result, err := runner.Run(context.Background(), systemManagedAgentRunRequest{
		AgentID:           guardianSceneID,
		Model:             probe,
		ParentSession:     parent,
		Events:            history,
		Input:             currentApproval,
		InputUserEvidence: []string{"Actual user explicitly requested this exact push."},
		Compaction: sdkruntime.CompactionConfig{
			Enabled:                    true,
			DefaultContextWindowTokens: 1_000_000,
			SegmentTokenBudget:         512,
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	compactCalls, normalCalls, compactReq, normalReq := probe.snapshot()
	if compactCalls != 1 || normalCalls != 2 {
		t.Fatalf("model calls = compact %d / normal %d, want 1/2 overflow recovery", compactCalls, normalCalls)
	}
	if requestContainsText(compactReq, currentApproval) {
		t.Fatalf("overflow compaction summarized the current exact approval: %#v", compactReq.Messages)
	}
	if !requestContainsExactMessage(normalReq, currentApproval) {
		t.Fatalf("overflow retry lost the exact current approval: %#v", normalReq.Messages)
	}
	if len(result.ContextEvents) < 3 || !slices.ContainsFunc(result.ContextEvents, func(event *session.Event) bool {
		return session.EventTypeOf(event) == session.EventTypeUser && session.EventText(event) == currentApproval
	}) {
		t.Fatalf("overflow result context lost current approval pair: %#v", result.ContextEvents)
	}
}

type systemManagedObservationGapRun struct {
	dropped int
}

type systemManagedGuardianCompactionModel struct {
	mu              sync.Mutex
	compactionCalls int
	normalCalls     int
	compactionReq   *model.Request
	normalReq       *model.Request
	overflowFirst   bool
}

func (*systemManagedGuardianCompactionModel) Name() string { return "guardian-compaction-probe" }

func (*systemManagedGuardianCompactionModel) Capabilities() model.Capabilities {
	return model.Capabilities{Streaming: true, StructuredOutput: true}
}

func (m *systemManagedGuardianCompactionModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	instructions := systemManagedRequestInstructions(req)
	m.mu.Lock()
	if strings.Contains(instructions, "CONTEXT COMPACTION SUMMARY") {
		m.compactionCalls++
		m.compactionReq = model.CloneRequest(req)
		m.mu.Unlock()
		return systemManagedTextResponse(`CONTEXT CHECKPOINT

## Current Objective
- review the next exact approval request safely

## User Constraints
- preserve explicit user authorization and default safety boundaries

## Durable Decisions
- prior Guardian approvals remain evidence, not blanket authorization

## Verified Facts
- historical Guardian dialogue exceeded the local context budget

## Current Progress
- old dialogue was summarized before the next approval

## Open Questions / Risks
- assess the pending action exactly

## Next Actions
1. evaluate the pending approval request

## Active Tasks
- none

## Active Participants
- none

## Latest Blockers
- none

## Operational Notes
- Guardian context is process-local and non-persistent`)
	}
	m.normalCalls++
	m.normalReq = model.CloneRequest(req)
	normalCall := m.normalCalls
	overflowFirst := m.overflowFirst
	m.mu.Unlock()
	if overflowFirst && normalCall == 1 {
		return func(yield func(*model.StreamEvent, error) bool) {
			yield(nil, &model.ContextOverflowError{Cause: errors.New("synthetic Guardian provider overflow")})
		}
	}
	return systemManagedTextResponse(`{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"exact current approval retained"}`)
}

func (m *systemManagedGuardianCompactionModel) snapshot() (int, int, *model.Request, *model.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.compactionCalls, m.normalCalls, model.CloneRequest(m.compactionReq), model.CloneRequest(m.normalReq)
}

func systemManagedTextResponse(text string) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{Type: model.StreamEventTurnDone, Response: &model.Response{
			Status: model.ResponseStatusCompleted, TurnComplete: true, StepComplete: true,
			Message: model.NewTextMessage(model.RoleAssistant, text),
		}}, nil)
	}
}

func systemManagedRequestInstructions(req *model.Request) string {
	if req == nil {
		return ""
	}
	parts := make([]string, 0, len(req.Instructions))
	for _, instruction := range req.Instructions {
		if instruction.Text != nil {
			parts = append(parts, instruction.Text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func requestContainsExactMessage(req *model.Request, text string) bool {
	if req == nil {
		return false
	}
	return slices.ContainsFunc(req.Messages, func(message model.Message) bool {
		return message.TextContent() == text
	})
}

func requestContainsText(req *model.Request, text string) bool {
	if req == nil {
		return false
	}
	return slices.ContainsFunc(req.Messages, func(message model.Message) bool {
		return strings.Contains(message.TextContent(), text)
	})
}

func (systemManagedObservationGapRun) RunID() string { return "system-managed-gap" }

func (r systemManagedObservationGapRun) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		yield(nil, &agent.EventStreamGapError{Dropped: uint64(r.dropped)})
	}
}

func (systemManagedObservationGapRun) Submit(agent.Submission) error { return nil }
func (systemManagedObservationGapRun) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}
func (systemManagedObservationGapRun) Close() error { return nil }

type systemManagedLifecycleRecorder struct {
	mu     sync.Mutex
	events []agent.LifecycleEvent
}

type systemManagedGuardrailRecorder struct {
	mu    sync.Mutex
	count int
}

func (*systemManagedGuardrailRecorder) Name() string { return "system-managed-test" }

func (r *systemManagedGuardrailRecorder) ApplyGuardrail(_ context.Context, input agent.GuardrailInput) (agent.GuardrailInput, error) {
	r.mu.Lock()
	r.count++
	r.mu.Unlock()
	return input, nil
}

func (r *systemManagedGuardrailRecorder) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

func (r *systemManagedLifecycleRecorder) InterceptLifecycle(ctx context.Context, event agent.LifecycleEvent, next agent.LifecycleNext) error {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	return next(ctx)
}

func (r *systemManagedLifecycleRecorder) snapshot() []agent.LifecycleEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agent.LifecycleEvent(nil), r.events...)
}

func (r *systemManagedLifecycleRecorder) saw(operation agent.LifecycleOperation) bool {
	for _, event := range r.snapshot() {
		if event.Operation == operation {
			return true
		}
	}
	return false
}

func TestSystemManagedAgentRegistryEntries(t *testing.T) {
	specs := systemManagedAgentSpecs()
	if len(specs) != 1 {
		t.Fatalf("systemManagedAgentSpecs() len = %d, want 1: %#v", len(specs), specs)
	}
	ids := make([]string, 0, len(specs))
	for _, spec := range specs {
		ids = append(ids, spec.ID)
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatalf("systemManagedAgentSpecs() ids = %#v, want sorted", ids)
	}

	spec := specs[0]
	if spec.ID != guardianSceneID {
		t.Fatalf("systemManagedAgentSpecs()[0].ID = %q, want guardian", spec.ID)
	}
	if spec.Purpose != systemManagedAgentPurposeApprovalReview {
		t.Fatalf("guardian purpose = %q, want approval_review", spec.Purpose)
	}
	if spec.CapabilityProfile != systemManagedAgentCapabilityNone {
		t.Fatalf("guardian capability = %q, want none", spec.CapabilityProfile)
	}
}

func TestSystemManagedAgentRegistryOwnsFixedScene(t *testing.T) {
	spec, ok := systemManagedAgentSpecFor("guardian")
	if !ok {
		t.Fatal("systemManagedAgentSpecFor(guardian) missing")
	}
	if spec.ID != guardianSceneID || spec.Purpose != systemManagedAgentPurposeApprovalReview {
		t.Fatalf("guardian spec = %#v, want approval-review registry entry", spec)
	}

	if spec.Instructions == "" || spec.CapabilityProfile != systemManagedAgentCapabilityNone {
		t.Fatalf("guardian scene = %#v, want fixed instructions without tools", spec)
	}
}

func TestSystemManagedAgentRunPlanUsesRegistryDefaults(t *testing.T) {
	parent := session.Session{
		SessionRef: session.SessionRef{
			AppName:      "caelis",
			UserID:       "user-1",
			SessionID:    "parent-session",
			WorkspaceKey: "workspace-1",
		},
		CWD: "/tmp/workspace",
	}
	plan, err := systemManagedAgentRunPlanFor(systemManagedAgentRunRequest{
		AgentID:       "guardian",
		Model:         systemManagedAgentTestModel{name: "guardian-model"},
		ParentSession: parent,
	})
	if err != nil {
		t.Fatalf("systemManagedAgentRunPlanFor() error = %v", err)
	}
	if plan.Spec.ID != guardianSceneID || plan.AgentID != guardianSceneID {
		t.Fatalf("run plan agent = spec %q id %q, want guardian", plan.Spec.ID, plan.AgentID)
	}
	if plan.Purpose != systemManagedAgentPurposeApprovalReview || plan.CapabilityProfile != systemManagedAgentCapabilityNone {
		t.Fatalf("run plan purpose/capability = %q/%q, want approval_review/none", plan.Purpose, plan.CapabilityProfile)
	}
	if got := plan.Metadata["system_managed_agent"]; got != guardianSceneID {
		t.Fatalf("run plan metadata system_managed_agent = %#v, want guardian", got)
	}
	if got := plan.Metadata["system_managed_capability_profile"]; got != string(systemManagedAgentCapabilityNone) {
		t.Fatalf("run plan metadata capability = %#v, want none", got)
	}
	if plan.Session.SessionID != "parent-session-approval-review" {
		t.Fatalf("run plan session id = %q, want parent-session-approval-review", plan.Session.SessionID)
	}
	if got := plan.Session.Metadata["system_managed_agent"]; got != guardianSceneID {
		t.Fatalf("run plan session metadata system_managed_agent = %#v, want guardian", got)
	}
}

type systemManagedAgentTestModel struct {
	name string
}

type systemManagedAgentResponseModel struct{}

func (systemManagedAgentResponseModel) Name() string { return "system-managed-response" }

func (systemManagedAgentResponseModel) Capabilities() model.Capabilities {
	return model.Capabilities{Streaming: true, StructuredOutput: true}
}

func (systemManagedAgentResponseModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{Type: model.StreamEventTurnDone, Response: &model.Response{
			Status: model.ResponseStatusCompleted, TurnComplete: true, StepComplete: true,
			Message: model.NewTextMessage(model.RoleAssistant, `{"outcome":"allow"}`),
		}}, nil)
	}
}

func (m systemManagedAgentTestModel) Name() string {
	if strings.TrimSpace(m.name) != "" {
		return strings.TrimSpace(m.name)
	}
	return "system-managed-test-model"
}

func (m systemManagedAgentTestModel) Capabilities() model.Capabilities {
	return model.Capabilities{Streaming: true, StructuredOutput: true}
}

func (m systemManagedAgentTestModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(func(*model.StreamEvent, error) bool) {}
}
