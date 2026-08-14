package gatewayapp

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestGuardianRequestsKeepExactPrefixWithinParentCompactEpochs(t *testing.T) {
	ctx := context.Background()
	service, activeSession := newApprovalReviewerTestSession(t, ctx)
	appendApprovalReviewerTextEvent(
		t,
		ctx,
		service,
		activeSession,
		session.EventTypeUser,
		model.RoleUser,
		"Prepare the focused fix, commit it, then inspect the resulting status.",
	)

	responses := []string{
		`{"outcome":"allow","risk_level":"medium","user_authorization":"high","rationale":"commit is requested"}`,
		`{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"status is requested"}`,
		`{"outcome":"allow","risk_level":"medium","user_authorization":"high","rationale":"push is summarized as authorized"}`,
		`{"outcome":"allow","risk_level":"low","user_authorization":"medium","rationale":"inspection is read only"}`,
	}
	recordingModel := &approvalReviewerFakeModel{responses: responses}
	reviewer := newGuardianApprovalApprover(service)

	if _, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(
		activeSession,
		recordingModel,
		"git commit -m focused-fix",
		map[string]any{"cmd": "git commit -m focused-fix"},
	)); err != nil {
		t.Fatalf("approval A: %v", err)
	}
	appendApprovalReviewerTextEvent(
		t,
		ctx,
		service,
		activeSession,
		session.EventTypeAssistant,
		model.RoleAssistant,
		"The focused commit completed; inspect the working tree next.",
	)
	if _, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(
		activeSession,
		recordingModel,
		"git status --short",
		map[string]any{"cmd": "git status --short"},
	)); err != nil {
		t.Fatalf("approval B: %v", err)
	}

	appendGuardianPrefixTestCompact(
		t,
		ctx,
		service,
		activeSession,
		"The user requested the focused fix and authorized pushing its prepared branch after verification.",
	)
	appendApprovalReviewerTextEvent(
		t,
		ctx,
		service,
		activeSession,
		session.EventTypeAssistant,
		model.RoleAssistant,
		"Verification passed after the main Session summary; push the prepared branch.",
	)
	if _, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(
		activeSession,
		recordingModel,
		"git push origin focused-fix",
		map[string]any{"cmd": "git push origin focused-fix"},
	)); err != nil {
		t.Fatalf("approval C: %v", err)
	}
	appendApprovalReviewerTextEvent(
		t,
		ctx,
		service,
		activeSession,
		session.EventTypeAssistant,
		model.RoleAssistant,
		"The branch push completed; inspect the remote ref.",
	)
	if _, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(
		activeSession,
		recordingModel,
		"git ls-remote --heads origin focused-fix",
		map[string]any{"cmd": "git ls-remote --heads origin focused-fix"},
	)); err != nil {
		t.Fatalf("approval D: %v", err)
	}

	requests := recordingModel.Requests()
	if got, want := len(requests), 4; got != want {
		t.Fatalf("Guardian model requests = %d, want %d", got, want)
	}

	// Approval B must append to the exact request prefix committed by approval A.
	requireGuardianRequestMessagePrefix(t, requests[0], requests[1], responses[0])
	if delta := requests[1].Messages[len(requests[1].Messages)-1].TextContent(); !strings.Contains(delta, ">>> TRANSCRIPT DELTA START") ||
		!strings.Contains(delta, "The focused commit completed") ||
		strings.Contains(delta, "Prepare the focused fix") {
		t.Fatalf("approval B transcript is not an append-only delta:\n%s", delta)
	}

	// A parent compact is an intentional cold rebase: none of the prior
	// Guardian user/assistant pairs may remain in the new model request.
	if got, want := len(requests[2].Messages), 1; got != want {
		t.Fatalf("approval C messages = %d, want one cold-rebased prompt", got)
	}
	if reflect.DeepEqual(requests[2].Messages[0], requests[0].Messages[0]) {
		t.Fatal("approval C unexpectedly reused the pre-compact prompt")
	}
	coldPrompt := requests[2].Messages[0].TextContent()
	if !strings.Contains(coldPrompt, "[MAIN SESSION SUMMARY]") {
		t.Fatalf("approval C prompt is missing the main Session summary marker:\n%s", coldPrompt)
	}
	if !strings.Contains(coldPrompt, "authorized pushing its prepared branch") {
		t.Fatalf("approval C prompt is missing the compact summary:\n%s", coldPrompt)
	}
	if strings.Contains(coldPrompt, "Prepare the focused fix, commit it") {
		t.Fatalf("approval C repeated pre-compact transcript instead of rebasing:\n%s", coldPrompt)
	}

	// Approval D must resume exact append-only reuse inside the new parent
	// compact epoch.
	requireGuardianRequestMessagePrefix(t, requests[2], requests[3], responses[2])
	if delta := requests[3].Messages[len(requests[3].Messages)-1].TextContent(); !strings.Contains(delta, ">>> TRANSCRIPT DELTA START") ||
		!strings.Contains(delta, "The branch push completed") ||
		strings.Contains(delta, "[MAIN SESSION SUMMARY]") {
		t.Fatalf("approval D transcript is not a post-compact append-only delta:\n%s", delta)
	}
}

func TestGuardianCoverageAwareCompactKeepsConcurrentSuccessorAndNextDelta(t *testing.T) {
	ctx := context.Background()
	service, activeSession := newApprovalReviewerTestSession(t, ctx)
	for index := 0; index < 10; index++ {
		role := model.RoleAssistant
		eventType := session.EventTypeAssistant
		if index%2 == 0 {
			role = model.RoleUser
			eventType = session.EventTypeUser
		}
		appendApprovalReviewerTextEvent(t, ctx, service, activeSession, eventType, role, "covered parent history")
	}
	coveredEvents, err := service.Events(ctx, session.EventsRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	covered := coveredEvents[len(coveredEvents)-1]
	appendApprovalReviewerTextEvent(
		t,
		ctx,
		service,
		activeSession,
		session.EventTypeUser,
		model.RoleUser,
		"CONCURRENT_UNSUMMARIZED_USER verify the focused fix",
	)
	summaryText := "Summary through the covered parent history only."
	summary := model.NewTextMessage(model.RoleUser, summaryText)
	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: activeSession.SessionRef,
		Event: &session.Event{
			Type:       session.EventTypeCompact,
			Visibility: session.VisibilityCanonical,
			Message:    &summary,
			Text:       summaryText,
			Meta: map[string]any{compact.MetaKeyCompact: compact.CompactEventDataValue(compact.CompactEventData{
				ContractVersion:      compact.CompactContractVersion,
				SummarizedThroughID:  covered.ID,
				SummarizedThroughSeq: covered.Seq,
			})},
		},
	}); err != nil {
		t.Fatalf("append coverage-aware compact: %v", err)
	}

	responses := []string{
		`{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"concurrent user request retained"}`,
		`{"outcome":"allow","risk_level":"low","user_authorization":"medium","rationale":"next read-only delta retained"}`,
	}
	recordingModel := &approvalReviewerFakeModel{responses: responses}
	reviewer := newGuardianApprovalApprover(service)
	if _, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(
		activeSession,
		recordingModel,
		"go test ./focused",
		map[string]any{"cmd": "go test ./focused"},
	)); err != nil {
		t.Fatalf("coverage-aware approval A: %v", err)
	}
	appendApprovalReviewerTextEvent(
		t,
		ctx,
		service,
		activeSession,
		session.EventTypeAssistant,
		model.RoleAssistant,
		"POST_COMPACT_DELTA the focused tests passed",
	)
	if _, err := reviewer.ReviewApproval(ctx, approvalReviewerTestRequest(
		activeSession,
		recordingModel,
		"git status --short",
		map[string]any{"cmd": "git status --short"},
	)); err != nil {
		t.Fatalf("coverage-aware approval B: %v", err)
	}

	requests := recordingModel.Requests()
	if got, want := len(requests), 2; got != want {
		t.Fatalf("Guardian model requests = %d, want %d", got, want)
	}
	first := requests[0].Messages[0].TextContent()
	for _, want := range []string{guardianMainSessionSummaryMarker, "Summary through the covered", "CONCURRENT_UNSUMMARIZED_USER"} {
		if !strings.Contains(first, want) {
			t.Fatalf("coverage-aware cold prompt missing %q:\n%s", want, first)
		}
	}
	requireGuardianRequestMessagePrefix(t, requests[0], requests[1], responses[0])
	delta := requests[1].Messages[len(requests[1].Messages)-1].TextContent()
	if !strings.Contains(delta, "POST_COMPACT_DELTA") ||
		strings.Contains(delta, "CONCURRENT_UNSUMMARIZED_USER") ||
		strings.Contains(delta, guardianMainSessionSummaryMarker) {
		t.Fatalf("coverage-aware next prompt is not the exact post-cursor delta:\n%s", delta)
	}
}

func requireGuardianRequestMessagePrefix(
	t *testing.T,
	previous model.Request,
	next model.Request,
	previousAssessment string,
) {
	t.Helper()
	if !reflect.DeepEqual(next.Instructions, previous.Instructions) {
		t.Fatal("Guardian instructions changed within one parent compact epoch")
	}
	if !reflect.DeepEqual(next.Output, previous.Output) {
		t.Fatal("Guardian output specification changed within one parent compact epoch")
	}
	if got, want := len(next.Messages), len(previous.Messages)+2; got != want {
		t.Fatalf("next Guardian messages = %d, want prior prefix plus assessment and delta (%d)", got, want)
	}
	if !reflect.DeepEqual(next.Messages[:len(previous.Messages)], previous.Messages) {
		t.Fatal("next Guardian request did not preserve the exact prior message prefix")
	}
	if got := next.Messages[len(previous.Messages)].TextContent(); got != previousAssessment {
		t.Fatalf("committed Guardian assessment = %q, want %q", got, previousAssessment)
	}
}

func appendGuardianPrefixTestCompact(
	t *testing.T,
	ctx context.Context,
	service session.Service,
	activeSession session.Session,
	summaryText string,
) {
	t.Helper()
	events, err := service.Events(ctx, session.EventsRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("load parent events before compact: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("cannot append a compact event without covered parent history")
	}
	covered := events[len(events)-1]
	summary := model.NewTextMessage(model.RoleUser, summaryText)
	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: activeSession.SessionRef,
		Event: &session.Event{
			Type:       session.EventTypeCompact,
			Visibility: session.VisibilityCanonical,
			Message:    &summary,
			Text:       summaryText,
			Meta: map[string]any{compact.MetaKeyCompact: compact.CompactEventDataValue(compact.CompactEventData{
				ContractVersion:      compact.CompactContractVersion,
				SummarizedThroughID:  covered.ID,
				SummarizedThroughSeq: covered.Seq,
			})},
		},
	}); err != nil {
		t.Fatalf("append parent compact: %v", err)
	}
}
