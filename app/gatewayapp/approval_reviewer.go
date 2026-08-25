package gatewayapp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/kernel"
)

const guardianAssessmentMaxAttempts = 3

type guardianApprovalReviewer struct {
	sessions      session.Service
	systemAgents  systemManagedAgentRunner
	conversations *guardianConversationManager
	accountingMu  sync.Mutex
	accounting    map[string]approvalReviewAccounting
}

type approvalReviewAccounting struct {
	usage      *kernel.UsageSnapshot
	invocation *session.EventInvocation
}

// newModelApprovalReviewer keeps the historical constructor name used by local
// stack setup and tests while the concrete implementation is now a no-tool
// guardian agent.
func newModelApprovalReviewer(sessions ...session.Service) kernel.ApprovalReviewer {
	var service session.Service
	if len(sessions) > 0 {
		service = sessions[0]
	}
	return newGuardianApprovalReviewer(service)
}

func newGuardianApprovalReviewer(service session.Service) kernel.ApprovalReviewer {
	return newGuardianApprovalApprover(service)
}

func newGuardianApprovalApprover(service session.Service, diagnostics ...*slog.Logger) *guardianApprovalReviewer {
	var logger *slog.Logger
	if len(diagnostics) > 0 {
		logger = diagnostics[0]
	}
	return &guardianApprovalReviewer{
		sessions: service,
		systemAgents: newSystemManagedAgentRuntimeWithConfig(systemManagedAgentRuntimeConfig{
			Diagnostics: logger,
		}),
		conversations: newGuardianConversationManager(),
		accounting:    map[string]approvalReviewAccounting{},
	}
}

func (r *guardianApprovalReviewer) ReviewApproval(ctx context.Context, req kernel.ApprovalReviewRequest) (kernel.ApprovalReviewResult, error) {
	return r.Decide(ctx, req)
}

// Decide returns one fully resolved Guardian decision. The Guardian path owns
// strict model-output validation and must not pass through generic reviewer
// reconciliation that guesses options or lets an option override its outcome.
func (r *guardianApprovalReviewer) Decide(ctx context.Context, req kernel.ApprovalReviewRequest) (kernel.ApprovalReviewResult, error) {
	if req.Model == nil {
		return kernel.ApprovalReviewResult{}, fmt.Errorf("approval reviewer requires the current session model")
	}
	if r == nil || r.sessions == nil {
		return kernel.ApprovalReviewResult{}, fmt.Errorf("approval reviewer requires session history")
	}
	if req.Approval != nil && len(req.Approval.Options) > 0 {
		if err := approval.ValidateStrictOptions(req.Approval.Options); err != nil {
			//nolint:nilerr // Strict validation failure is a resolved deterministic denial, not a reviewer transport error.
			return guardianDeterministicDenial(req.Approval, "automatic approval review denied malformed approval options: "+err.Error()), nil
		}
	}
	promptItems, _, assistantEvent, parsed, err := r.runGuardianReview(ctx, req)
	if err != nil {
		return kernel.ApprovalReviewResult{}, err
	}
	if promptItems.MandatoryInputTooLarge {
		return guardianDeterministicDenial(req.Approval, "automatic approval review denied because the exact approval request exceeds the Guardian model input budget; narrow the action"), nil
	}
	r.storeApprovalReviewAccounting(req.ReviewID, approvalReviewAccountingFromEvent(assistantEvent))
	return finalizeGuardianDecision(req.Approval, parsed)
}

func (r *guardianApprovalReviewer) ApprovalReviewAccounting(
	_ context.Context,
	req kernel.ApprovalReviewRequest,
	_ kernel.ApprovalReviewResult,
) (*kernel.UsageSnapshot, *session.EventInvocation, error) {
	accounting, ok := r.takeApprovalReviewAccounting(req.ReviewID)
	if !ok {
		return nil, nil, nil
	}
	return accounting.usage, accounting.invocation, nil
}

func (r *guardianApprovalReviewer) storeApprovalReviewAccounting(reviewID string, accounting approvalReviewAccounting) {
	if r == nil || strings.TrimSpace(reviewID) == "" || accounting.usage == nil {
		return
	}
	r.accountingMu.Lock()
	defer r.accountingMu.Unlock()
	if r.accounting == nil {
		r.accounting = map[string]approvalReviewAccounting{}
	}
	r.accounting[strings.TrimSpace(reviewID)] = accounting
}

func (r *guardianApprovalReviewer) takeApprovalReviewAccounting(reviewID string) (approvalReviewAccounting, bool) {
	if r == nil || strings.TrimSpace(reviewID) == "" {
		return approvalReviewAccounting{}, false
	}
	r.accountingMu.Lock()
	defer r.accountingMu.Unlock()
	accounting, ok := r.accounting[strings.TrimSpace(reviewID)]
	if ok {
		delete(r.accounting, strings.TrimSpace(reviewID))
	}
	return accounting, ok
}

// ReleaseApprovalContext drops the non-persistent Guardian dialogue when the
// owning main Session is semantically closed.
func (r *guardianApprovalReviewer) ReleaseApprovalContext(ref session.SessionRef) {
	if r == nil || r.conversations == nil {
		return
	}
	r.conversations.forget(ref.SessionID)
}

func approvalReviewAccountingFromEvent(event *session.Event) approvalReviewAccounting {
	return approvalReviewAccounting{
		usage:      session.UsageSnapshotFromSessionEvent(event),
		invocation: approvalInvocationFromEvent(event),
	}
}

func approvalInvocationFromEvent(event *session.Event) *session.EventInvocation {
	if event == nil || event.Invocation == nil {
		return nil
	}
	invocation := session.CloneEventInvocation(*event.Invocation)
	if invocation.Provider == "" && invocation.Model == "" {
		return nil
	}
	return &invocation
}

func (r *guardianApprovalReviewer) runGuardianReview(
	ctx context.Context,
	req kernel.ApprovalReviewRequest,
) (guardianPromptItems, *session.Event, *session.Event, guardianReviewModelOutput, error) {
	activeSession, err := r.sessions.Session(ctx, req.SessionRef)
	if err != nil {
		return guardianPromptItems{}, nil, nil, guardianReviewModelOutput{}, err
	}
	if r.conversations == nil {
		return guardianPromptItems{}, nil, nil, guardianReviewModelOutput{}, fmt.Errorf("approval reviewer requires an in-memory conversation manager")
	}
	parentEvents, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: req.SessionRef})
	if err != nil {
		return guardianPromptItems{}, nil, nil, guardianReviewModelOutput{}, err
	}
	forkRef := guardianConversationForkFromApproval(req)
	conversation, err := r.conversations.fork(req.SessionRef.SessionID, forkRef, parentEvents)
	if err != nil {
		return guardianPromptItems{}, nil, nil, guardianReviewModelOutput{}, err
	}
	parentEvents = conversation.ParentEvents
	parentCompact := guardianParentCompactIdentityFromEvents(parentEvents)
	promptMode, err := guardianPromptModeForConversation(conversation, parentCompact)
	if err != nil {
		return guardianPromptItems{}, nil, nil, guardianReviewModelOutput{}, err
	}
	outputSpec, err := guardianOutputSpecForModel(req.Model, req.Approval)
	if err != nil {
		return guardianPromptItems{}, nil, nil, guardianReviewModelOutput{}, err
	}
	compactionCfg := guardianCompactionConfig(req.Model, outputSpec)
	historyEvents := conversation.Events
	if !promptMode.Delta {
		historyEvents = nil
	}
	promptItems, err := buildGuardianPromptItemsWithinBudget(parentEvents, promptMode, req, guardianPromptBudget{
		Model:         req.Model,
		HistoryEvents: historyEvents,
		Output:        outputSpec,
		Compaction:    compactionCfg,
	})
	if err != nil {
		return guardianPromptItems{}, nil, nil, guardianReviewModelOutput{}, err
	}
	if promptItems.HistoryNeedsCompaction {
		compacted, compactErr := r.compactGuardianHistory(ctx, req.Model, activeSession, historyEvents, compactionCfg)
		if compactErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return guardianPromptItems{}, nil, nil, guardianReviewModelOutput{}, ctxErr
			}
			// Compaction is an optimization of process-local history, not approval
			// authority. If its transient staging/model step fails, cold-rebase from
			// the authoritative parent projection and still ask Guardian to decide.
			compacted = systemManagedAgentCompactResult{}
		}
		if compacted.Compacted && len(compacted.Events) > 0 {
			historyEvents = compacted.Events
			promptItems, err = buildGuardianPromptItemsWithinBudget(parentEvents, promptMode, req, guardianPromptBudget{
				Model:         req.Model,
				HistoryEvents: historyEvents,
				Output:        outputSpec,
				Compaction:    compactionCfg,
			})
			if err != nil {
				return guardianPromptItems{}, nil, nil, guardianReviewModelOutput{}, err
			}
		}
		if !compacted.Compacted || promptItems.HistoryNeedsCompaction {
			// A checkpoint that still consumes the request watermark cannot be
			// compacted further without new dialogue. Start a new process-local
			// Guardian epoch from the authoritative parent projection instead.
			historyEvents = nil
			promptMode.Delta = false
			promptMode.Cursor = guardianParentCanonicalCursor{}
			promptItems, err = buildGuardianPromptItemsWithinBudget(parentEvents, promptMode, req, guardianPromptBudget{
				Model:      req.Model,
				Output:     outputSpec,
				Compaction: compactionCfg,
			})
			if err != nil {
				return guardianPromptItems{}, nil, nil, guardianReviewModelOutput{}, err
			}
		}
	}
	if promptItems.MandatoryInputTooLarge {
		return promptItems, nil, nil, guardianReviewModelOutput{}, nil
	}
	var options []kernel.ApprovalOption
	if req.Approval != nil {
		options = req.Approval.Options
	}
	promptEvent := guardianUserEvent(activeSession, promptItems.Text)
	annotateGuardianReviewEvent(promptEvent, req.ReviewID)
	var lastAssistantEvent *session.Event
	var lastParseErr error
	for attempt := 0; attempt < guardianAssessmentMaxAttempts; attempt++ {
		runResult, err := r.runGuardianAgent(ctx, req.Model, activeSession, historyEvents, promptItems.Text, promptItems.UserEvidence, outputSpec, compactionCfg)
		if err != nil {
			return promptItems, promptEvent, runResult.AssistantEvent, guardianReviewModelOutput{}, err
		}
		lastAssistantEvent = runResult.AssistantEvent
		parsed, err := parseGuardianAssessmentForMode(runResult.Text, outputSpec.Mode, options)
		if err != nil {
			lastParseErr = err
			continue
		}
		// Commit only validated assessments to the process-local conversation;
		// malformed attempts and staging compact artifacts are discarded together.
		annotateGuardianReviewEvent(runResult.AssistantEvent, req.ReviewID)
		_, _, err = r.conversations.commitValidated(guardianConversationCommit{
			SessionID:       req.SessionRef.SessionID,
			ExpectedVersion: conversation.Version,
			Fork:            forkRef,
			ParentCompact:   promptItems.ParentCompact,
			ParentCursor:    promptItems.ParentCursor,
			User:            promptEvent,
			Assistant:       runResult.AssistantEvent,
			ContextEvents:   runResult.ContextEvents,
		})
		if err != nil {
			return promptItems, promptEvent, runResult.AssistantEvent, guardianReviewModelOutput{}, err
		}
		// A concurrent version loser keeps its already validated decision but
		// cannot advance the reusable prefix or parent transcript cursor.
		return promptItems, promptEvent, runResult.AssistantEvent, parsed, nil
	}
	return promptItems, promptEvent, lastAssistantEvent, guardianReviewModelOutput{}, fmt.Errorf("approval reviewer failed to return a valid JSON assessment after %d attempts: %w", guardianAssessmentMaxAttempts, lastParseErr)
}

func guardianConversationForkFromApproval(req kernel.ApprovalReviewRequest) guardianConversationForkRef {
	step := req.RuntimeRequest.ModelStep
	if step == nil || step.AdmissionDone() == nil || step.CallCount < 2 || step.Index < 0 || step.Index >= step.CallCount {
		return guardianConversationForkRef{}
	}
	stepID := strings.TrimSpace(step.ID)
	runID := strings.TrimSpace(req.RunID)
	turnID := strings.TrimSpace(req.TurnID)
	if stepID == "" || runID == "" || turnID == "" {
		return guardianConversationForkRef{}
	}
	return guardianConversationForkRef{
		Key:       runID + "\x00" + turnID + "\x00" + stepID,
		Index:     step.Index,
		CallCount: step.CallCount,
	}
}

func (r *guardianApprovalReviewer) runGuardianAgent(
	ctx context.Context,
	model model.LLM,
	guardianSession session.Session,
	events []*session.Event,
	input string,
	userEvidence []string,
	output *model.OutputSpec,
	compaction sdkruntime.CompactionConfig,
) (systemManagedAgentRunResult, error) {
	runner := r.systemAgents
	if runner == nil {
		runner = newSystemManagedAgentRuntime(nil)
	}
	spec, ok := systemManagedAgentSpecFor(guardianSceneID)
	if !ok {
		return systemManagedAgentRunResult{}, fmt.Errorf("gatewayapp: missing %q system-managed agent", guardianSceneID)
	}
	result, err := runner.Run(ctx, systemManagedAgentRunRequest{
		AgentID:           spec.ID,
		Purpose:           spec.Purpose,
		Model:             model,
		ParentSession:     guardianSession,
		Events:            events,
		Input:             input,
		InputUserEvidence: userEvidence,
		Output:            output,
		Compaction:        compaction,
		CapabilityProfile: spec.CapabilityProfile,
	})
	if err != nil {
		return result, err
	}
	if result.AssistantEvent == nil || strings.TrimSpace(result.Text) == "" {
		return result, fmt.Errorf("approval reviewer returned no final assessment")
	}
	return result, nil
}

func (r *guardianApprovalReviewer) compactGuardianHistory(
	ctx context.Context,
	llm model.LLM,
	parent session.Session,
	events []*session.Event,
	compaction sdkruntime.CompactionConfig,
) (systemManagedAgentCompactResult, error) {
	runner := r.systemAgents
	if runner == nil {
		runner = newSystemManagedAgentRuntime(nil)
	}
	compactor, ok := runner.(systemManagedAgentContextCompactor)
	if !ok {
		return systemManagedAgentCompactResult{}, nil
	}
	return compactor.CompactContext(ctx, systemManagedAgentCompactRequest{
		AgentID:       guardianSceneID,
		Purpose:       systemManagedAgentPurposeApprovalReview,
		Model:         llm,
		ParentSession: parent,
		Events:        events,
		Compaction:    compaction,
	})
}

func guardianUserEvent(_ session.Session, text string) *session.Event {
	message := model.NewTextMessage(model.RoleUser, strings.TrimSpace(text))
	return &session.Event{
		Type:       session.EventTypeUser,
		Visibility: session.VisibilityCanonical,
		Actor:      session.ActorRef{Kind: session.ActorKindSystem, Name: "guardian_input"},
		Scope: &session.EventScope{
			TurnID: "guardian-review",
			Source: "auto-review",
		},
		Message: &message,
		Text:    message.TextContent(),
	}
}

func annotateGuardianReviewEvent(event *session.Event, reviewID string) {
	if event == nil {
		return
	}
	if event.Visibility == "" {
		event.Visibility = session.VisibilityCanonical
	}
	if event.Scope == nil {
		event.Scope = &session.EventScope{}
	}
	event.Scope.TurnID = firstNonEmpty(strings.TrimSpace(reviewID), strings.TrimSpace(event.Scope.TurnID), "guardian-review")
	event.Scope.Source = firstNonEmpty(strings.TrimSpace(event.Scope.Source), "auto-review")
	if event.Meta == nil {
		event.Meta = map[string]any{}
	}
	event.Meta["system_managed_agent"] = guardianSceneID
	event.Meta["hidden_from_transcript"] = true
	if strings.TrimSpace(reviewID) != "" {
		event.Meta["review_id"] = strings.TrimSpace(reviewID)
	}
}

var _ kernel.ApprovalReviewer = (*guardianApprovalReviewer)(nil)
var _ kernel.ApprovalApprover = (*guardianApprovalReviewer)(nil)
