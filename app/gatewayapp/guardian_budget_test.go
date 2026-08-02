package gatewayapp

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestGuardianUnifiedBudgetKeepsExactActionAndBoundsFinalRequest(t *testing.T) {
	ctx := context.Background()
	service, activeSession := newApprovalReviewerTestSession(t, ctx)
	for index := 0; index < 40; index++ {
		role := model.RoleAssistant
		eventType := session.EventTypeAssistant
		if index%2 == 0 {
			role = model.RoleUser
			eventType = session.EventTypeUser
		}
		appendApprovalReviewerTextEvent(t, ctx, service, activeSession, eventType, role,
			"parent evidence "+strings.Repeat("e", 4_000))
	}
	command := "printf %s " + strings.Repeat("x", 70_000)
	llm := &approvalReviewerFakeModel{
		contextWindowTokens: 32_768,
		responses: []string{
			`{"outcome":"allow","risk_level":"medium","user_authorization":"high","rationale":"exact command was requested"}`,
		},
	}
	reviewer := newGuardianApprovalApprover(service)

	result, err := reviewer.Decide(ctx, approvalReviewerTestRequest(
		activeSession,
		llm,
		"run the exact long command",
		map[string]any{"cmd": command},
	))
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if !result.Approved {
		t.Fatalf("result = %#v, want model-reviewed allow", result)
	}
	requests := llm.Requests()
	if got, want := len(requests), 1; got != want {
		t.Fatalf("model requests = %d, want one normal Guardian request and no compaction request", got)
	}
	request := requests[0]
	if len(request.Messages) != 1 {
		t.Fatalf("request messages = %d, want one cold Guardian prompt", len(request.Messages))
	}
	input := request.Messages[0].TextContent()
	if !strings.Contains(input, command) {
		t.Fatal("Guardian prompt did not preserve the exact action")
	}
	if strings.Contains(input, "<guardian_truncated />") {
		t.Fatal("Guardian prompt contains a legacy truncation marker")
	}
	if !strings.Contains(input, "Some older conversation entries were omitted for budget") {
		t.Fatal("Guardian prompt omitted parent evidence without a disclosure")
	}
	projected := guardianModelRequest(nil, input, request.Output)
	if !reflect.DeepEqual(projected.Instructions, request.Instructions) ||
		!reflect.DeepEqual(projected.Messages, request.Messages) ||
		!reflect.DeepEqual(projected.Output, request.Output) {
		t.Fatalf("budget projection diverged from the model-visible request:\nprojected=%#v\nactual=%#v", projected, request)
	}
	budget := sdkruntimeBudgetForGuardianTest(llm, &request)
	if budget.Usage.TotalTokens > budget.Usage.EffectiveInputBudget {
		t.Fatalf("final request tokens = %d, want <= effective input budget %d", budget.Usage.TotalTokens, budget.Usage.EffectiveInputBudget)
	}
}

func TestGuardianCompactsAccumulatedHistoryOnceBeforeBudgetingCurrentPrompt(t *testing.T) {
	ctx := context.Background()
	service, activeSession := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(t, ctx, service, activeSession, session.EventTypeUser, model.RoleUser, "Inspect and apply the focused fix.")
	parentEvents, err := service.Events(ctx, session.EventsRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	_, cursor := collectGuardianTranscriptEntries(parentEvents)

	reviewer := newGuardianApprovalApprover(service)
	oldUser, oldAssistant := guardianBudgetConversationPair(strings.Repeat("old Guardian context ", 3_000))
	committed, _, err := reviewer.conversations.commitValidated(guardianConversationCommit{
		SessionID:       activeSession.SessionID,
		ExpectedVersion: 0,
		ParentCursor:    cursor,
		User:            oldUser,
		Assistant:       oldAssistant,
	})
	if err != nil || !committed {
		t.Fatalf("seed Guardian conversation = (%v, %v)", committed, err)
	}
	appendApprovalReviewerTextEvent(t, ctx, service, activeSession, session.EventTypeAssistant, model.RoleAssistant, "The focused fix is ready for the requested verification.")

	runner := &guardianBudgetSystemRunner{
		response: `{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"requested verification"}`,
	}
	reviewer.systemAgents = runner
	llm := &approvalReviewerFakeModel{contextWindowTokens: 16_384}
	result, err := reviewer.Decide(ctx, approvalReviewerTestRequest(
		activeSession,
		llm,
		"verify the focused fix",
		map[string]any{"cmd": "go test ./focused"},
	))
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if !result.Approved || runner.compactCalls != 1 || runner.runCalls != 1 {
		t.Fatalf("result/compact/run = %#v/%d/%d, want allow after exactly one history compact", result, runner.compactCalls, runner.runCalls)
	}
	if len(runner.runRequest.Events) != 1 || session.EventTypeOf(runner.runRequest.Events[0]) != session.EventTypeCompact {
		t.Fatalf("run history = %#v, want one transient compact checkpoint", runner.runRequest.Events)
	}
	snapshot, err := reviewer.conversations.snapshot(activeSession.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 2 || len(snapshot.Events) != 3 || session.EventTypeOf(snapshot.Events[0]) != session.EventTypeCompact {
		t.Fatalf("committed compacted conversation = %#v", snapshot)
	}
}

func TestGuardianCompactionFailureColdRebasesAndStillCallsGuardian(t *testing.T) {
	ctx := context.Background()
	service, activeSession := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(t, ctx, service, activeSession, session.EventTypeUser, model.RoleUser, "Inspect and apply the focused fix.")
	parentEvents, err := service.Events(ctx, session.EventsRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	_, cursor := collectGuardianTranscriptEntries(parentEvents)

	reviewer := newGuardianApprovalApprover(service)
	oldUser, oldAssistant := guardianBudgetConversationPair(strings.Repeat("old Guardian context ", 3_000))
	committed, _, err := reviewer.conversations.commitValidated(guardianConversationCommit{
		SessionID:       activeSession.SessionID,
		ExpectedVersion: 0,
		ParentCursor:    cursor,
		User:            oldUser,
		Assistant:       oldAssistant,
	})
	if err != nil || !committed {
		t.Fatalf("seed Guardian conversation = (%v, %v)", committed, err)
	}
	appendApprovalReviewerTextEvent(t, ctx, service, activeSession, session.EventTypeAssistant, model.RoleAssistant, "The focused fix is ready for the requested verification.")

	runner := &guardianBudgetSystemRunner{
		compactErr: errors.New("invalid transient checkpoint"),
		response:   `{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"requested verification"}`,
	}
	reviewer.systemAgents = runner
	result, err := reviewer.Decide(ctx, approvalReviewerTestRequest(
		activeSession,
		&approvalReviewerFakeModel{contextWindowTokens: 16_384},
		"verify the focused fix",
		map[string]any{"cmd": "go test ./focused"},
	))
	if err != nil {
		t.Fatalf("Decide() error = %v, want safe cold rebase", err)
	}
	if !result.Approved || runner.compactCalls != 1 || runner.runCalls != 1 {
		t.Fatalf("result/compact/run = %#v/%d/%d, want allow after one failed compact and one normal review", result, runner.compactCalls, runner.runCalls)
	}
	if len(runner.runRequest.Events) != 0 {
		t.Fatalf("cold-rebased run history = %#v, want no stale Guardian events", runner.runRequest.Events)
	}
	for _, want := range []string{"TRANSCRIPT START", "Inspect and apply the focused fix.", "focused fix is ready"} {
		if !strings.Contains(runner.runRequest.Input, want) {
			t.Fatalf("cold-rebased prompt missing %q:\n%s", want, runner.runRequest.Input)
		}
	}
}

func TestGuardianDiscardedAssessmentDoesNotCommitPreparedCompaction(t *testing.T) {
	ctx := context.Background()
	service, activeSession := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(t, ctx, service, activeSession, session.EventTypeUser, model.RoleUser, "Inspect the focused change.")
	parentEvents, err := service.Events(ctx, session.EventsRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	_, cursor := collectGuardianTranscriptEntries(parentEvents)

	reviewer := newGuardianApprovalApprover(service)
	oldUser, oldAssistant := guardianBudgetConversationPair(strings.Repeat("uncommitted compact source ", 3_000))
	committed, _, err := reviewer.conversations.commitValidated(guardianConversationCommit{
		SessionID:       activeSession.SessionID,
		ExpectedVersion: 0,
		ParentCursor:    cursor,
		User:            oldUser,
		Assistant:       oldAssistant,
	})
	if err != nil || !committed {
		t.Fatalf("seed Guardian conversation = (%v, %v)", committed, err)
	}
	appendApprovalReviewerTextEvent(t, ctx, service, activeSession, session.EventTypeAssistant, model.RoleAssistant, "The inspection is complete.")

	runner := &guardianBudgetSystemRunner{response: "not a JSON assessment"}
	reviewer.systemAgents = runner
	_, err = reviewer.Decide(ctx, approvalReviewerTestRequest(
		activeSession,
		&approvalReviewerFakeModel{contextWindowTokens: 16_384},
		"read status",
		map[string]any{"cmd": "git status --short"},
	))
	if err == nil {
		t.Fatal("Decide() error = nil, want invalid assessment rejection")
	}
	if runner.compactCalls != 1 || runner.runCalls != guardianAssessmentMaxAttempts {
		t.Fatalf("compact/run calls = %d/%d, want 1/%d", runner.compactCalls, runner.runCalls, guardianAssessmentMaxAttempts)
	}
	snapshot, err := reviewer.conversations.snapshot(activeSession.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 1 || len(snapshot.Events) != 2 || session.EventTypeOf(snapshot.Events[0]) == session.EventTypeCompact {
		t.Fatalf("invalid assessment committed prepared compaction: %#v", snapshot)
	}
}

func sdkruntimeBudgetForGuardianTest(llm model.LLM, req *model.Request) sdkruntime.ModelRequestBudget {
	return sdkruntime.EvaluateModelRequestBudget(llm, req, guardianCompactionConfig(llm, req.Output))
}

func guardianBudgetConversationPair(text string) (*session.Event, *session.Event) {
	userText := "historical prompt " + text
	assistantText := "historical assessment " + text
	user := model.NewTextMessage(model.RoleUser, userText)
	assistant := model.NewTextMessage(model.RoleAssistant, assistantText)
	return &session.Event{Type: session.EventTypeUser, Message: &user, Text: userText},
		&session.Event{Type: session.EventTypeAssistant, Message: &assistant, Text: assistantText}
}

type guardianBudgetSystemRunner struct {
	compactCalls  int
	runCalls      int
	compactErr    error
	response      string
	runRequest    systemManagedAgentRunRequest
	compactResult []*session.Event
}

func (r *guardianBudgetSystemRunner) CompactContext(
	_ context.Context,
	_ systemManagedAgentCompactRequest,
) (systemManagedAgentCompactResult, error) {
	r.compactCalls++
	if r.compactErr != nil {
		return systemManagedAgentCompactResult{}, r.compactErr
	}
	checkpointText := "CONTEXT CHECKPOINT\nPrior Guardian dialogue was compacted for the current parent objective."
	checkpointMessage := model.NewTextMessage(model.RoleUser, checkpointText)
	r.compactResult = []*session.Event{{
		Type:       session.EventTypeCompact,
		Visibility: session.VisibilityCanonical,
		Message:    &checkpointMessage,
		Text:       checkpointText,
	}}
	return systemManagedAgentCompactResult{Events: session.CloneEvents(r.compactResult), Compacted: true}, nil
}

func (r *guardianBudgetSystemRunner) Run(
	_ context.Context,
	req systemManagedAgentRunRequest,
) (systemManagedAgentRunResult, error) {
	r.runCalls++
	r.runRequest = req
	response := strings.TrimSpace(r.response)
	if response == "" {
		response = `{"outcome":"allow"}`
	}
	assistant := model.NewTextMessage(model.RoleAssistant, response)
	userEvent := guardianUserEvent(req.ParentSession, req.Input)
	assistantEvent := &session.Event{Type: session.EventTypeAssistant, Message: &assistant, Text: response}
	contextEvents := append(session.CloneEvents(req.Events), userEvent, assistantEvent)
	return systemManagedAgentRunResult{
		AssistantEvent: assistantEvent,
		ContextEvents:  contextEvents,
		Text:           response,
	}, nil
}

var _ systemManagedAgentRunner = (*guardianBudgetSystemRunner)(nil)
var _ systemManagedAgentContextCompactor = (*guardianBudgetSystemRunner)(nil)
