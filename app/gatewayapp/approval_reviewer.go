package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/internal/kernel"
)

const guardianAssessmentMaxAttempts = 3
const guardianReviewAttemptTimeout = 3 * time.Minute

type guardianApprovalReviewer struct {
	queryNetwork  sandbox.Network
	sessions      session.Service
	systemAgents  systemManagedAgentRunner
	conversations *guardianConversationManager
	diagnostics   *slog.Logger
	accountingMu  sync.Mutex
	accounting    map[string]approvalReviewAccounting
}

type approvalReviewAccounting struct {
	err        error
	usage      *kernel.UsageSnapshot
	invocation *session.EventInvocation
}

// newModelApprovalReviewer keeps the historical constructor name used by local
// stack setup and tests. The implementation is a tool-capable Guardian agent.
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
		diagnostics:   logger,
		accounting:    map[string]approvalReviewAccounting{},
	}
}

func (r *guardianApprovalReviewer) ReviewApproval(ctx context.Context, req kernel.ApprovalReviewRequest) (kernel.ApprovalReviewResult, error) {
	return r.Decide(ctx, req)
}

// Decide returns one fully resolved Guardian decision. The Guardian path owns
// strict model-output validation and must not pass through generic reviewer
// reconciliation that guesses options or lets an option override its outcome.
func (r *guardianApprovalReviewer) Decide(ctx context.Context, req kernel.ApprovalReviewRequest) (result kernel.ApprovalReviewResult, resultErr error) {
	if req.Model == nil {
		return kernel.ApprovalReviewResult{}, fmt.Errorf("approval reviewer requires the current session model")
	}
	if r == nil || r.sessions == nil {
		return kernel.ApprovalReviewResult{}, fmt.Errorf("approval reviewer requires session history")
	}
	if req.Approval != nil && len(req.Approval.Options) > 0 {
		if err := approval.ValidateStrictOptions(req.Approval.Options); err != nil {
			// Invalid input is a review failure, not a policy decision.
			return kernel.ApprovalReviewResult{}, fmt.Errorf("invalid approval options: %w", err)
		}
	}
	attempts := &guardianInvocationCollector{}
	ctx = model.WithInvocationObserver(ctx, attempts.collect)
	defer func() {
		if persistErr := r.persistGuardianInvocations(ctx, req, attempts.snapshot()); persistErr != nil {
			if r.diagnostics != nil {
				r.diagnostics.Warn("Guardian usage persistence failed",
					"session_id", req.SessionRef.SessionID, "review_id", req.ReviewID, "error", persistErr)
			}
			if resultErr != nil {
				resultErr = errors.Join(resultErr, persistErr)
			} else {
				r.storeApprovalReviewAccounting(approvalAccountingKey(req), approvalReviewAccounting{err: persistErr})
				// Preserve the completed decision: the Gateway treats any Decide error
				// as a failed review and would discard this valid policy result.
				result.DisplayText = strings.TrimSpace(result.DisplayText + "\nGuardian usage accounting could not be persisted.")
			}
		}
	}()
	promptItems, _, assistantEvent, parsed, err := r.runGuardianReview(ctx, req)
	if err != nil {
		return kernel.ApprovalReviewResult{}, err
	}
	if promptItems.MandatoryInputTooLarge {
		return kernel.ApprovalReviewResult{}, fmt.Errorf("exact approval request exceeds Guardian input budget")
	}
	// Compatibility for injected legacy runners that do not emit receipts. Drop
	// this fallback when every supported system-agent runner uses model.Generate.
	if len(attempts.snapshot()) == 0 {
		r.storeApprovalReviewAccounting(approvalAccountingKey(req), approvalReviewAccountingFromEvent(assistantEvent))
	}
	return finalizeGuardianDecision(req.Approval, parsed)
}

func (r *guardianApprovalReviewer) ApprovalReviewAccounting(
	_ context.Context,
	req kernel.ApprovalReviewRequest,
	_ kernel.ApprovalReviewResult,
) (*kernel.UsageSnapshot, *session.EventInvocation, error) {
	accounting, ok := r.takeApprovalReviewAccounting(approvalAccountingKey(req))
	if !ok {
		return nil, nil, nil
	}
	return accounting.usage, accounting.invocation, accounting.err
}

func (r *guardianApprovalReviewer) storeApprovalReviewAccounting(reviewID string, accounting approvalReviewAccounting) {
	if r == nil || strings.TrimSpace(reviewID) == "" || (accounting.usage == nil && accounting.err == nil) {
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
) (items guardianPromptItems, prompt *session.Event, assistant *session.Event, assessment guardianReviewModelOutput, reviewErr error) {
	queries := &guardianQueries{network: r.queryNetwork, model: req.Model}
	baseCtx := model.WithInvocationAdmission(ctx, queries.admit)
	ctx, cancel := context.WithTimeout(baseCtx, guardianReviewAttemptTimeout)
	defer cancel()
	defer func() { reviewErr = errors.Join(reviewErr, queries.close()) }()
	path := guardianHistoryPath(ctx, r.sessions, req.SessionRef)
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
	outputSpec, err := guardianOutputSpecForModel(req.Model, req.Approval)
	if err != nil {
		return guardianPromptItems{}, nil, nil, guardianReviewModelOutput{}, err
	}
	turnKey := fmt.Sprintf("%d:%s", conversation.Version, req.ReviewID)
	windowReq := req
	windowReq.ReviewID = turnKey
	compactionCfg := guardianCompactionConfig(req.Model, outputSpec)
	historyEvents, promptItems, err := guardianWindow(conversation, windowReq, outputSpec)
	if err != nil {
		return guardianPromptItems{}, nil, nil, guardianReviewModelOutput{}, err
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
	promptEvent.Meta[guardianCallKey] = guardianApprovalCallKey(req)
	promptEvent.Meta[guardianPendingCallKey] = guardianCurrentSourceCall(conversation, req) == 0
	var lastAssistantEvent *session.Event
	var lastParseErr error
	for attempt := 0; attempt < guardianAssessmentMaxAttempts; attempt++ {
		attemptCtx := ctx
		releaseAttempt := func() {}
		if attempt > 0 {
			cancel()
			attemptCtx, releaseAttempt = context.WithTimeout(baseCtx, guardianReviewAttemptTimeout)
		}
		attemptHistory := historyEvents
		if lastParseErr != nil {
			attemptHistory = append(session.CloneEvents(historyEvents), guardianEvidenceEvent("The previous response could not be used: "+lastParseErr.Error()+". Return only option_id for an allow option; include a nonempty rationale only for a reject option. Choose from this request's options."))
		}
		runResult, err := r.runGuardianAgent(attemptCtx, req.Model, activeSession, attemptHistory, promptItems.Text, promptItems.UserEvidence, outputSpec, compactionCfg, guardianQueryContext{queries, path})
		releaseAttempt()
		if err != nil {
			return promptItems, promptEvent, runResult.AssistantEvent, guardianReviewModelOutput{}, err
		}
		if queries.failure != nil {
			return promptItems, promptEvent, runResult.AssistantEvent, guardianReviewModelOutput{}, queries.failure
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
			ParentCursor:    promptItems.ParentCursor,
			User:            promptEvent,
			Assistant:       runResult.AssistantEvent,
			ContextEvents:   runResult.ContextEvents,
			PrefixEvents:    historyEvents,
			TurnID:          turnKey,
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
	queryArgs ...guardianQueryContext,
) (systemManagedAgentRunResult, error) {
	runner := r.systemAgents
	if runner == nil {
		runner = newSystemManagedAgentRuntime(nil)
	}
	spec, ok := systemManagedAgentSpecFor(guardianSceneID)
	if !ok {
		return systemManagedAgentRunResult{}, fmt.Errorf("gatewayapp: missing %q system-managed agent", guardianSceneID)
	}
	var tools []tool.Tool
	instructions := ""
	profile := spec.CapabilityProfile
	if len(queryArgs) > 0 && queryArgs[0].queries != nil {
		tools = queryArgs[0].queries.tools()
		profile = systemManagedAgentCapabilityReadOnly
		if queryArgs[0].path != "" {
			instructions = "Available parent Session JSONL (approval context, not a child endpoint's private log): " + queryArgs[0].path
		}
	}

	result, err := runner.Run(ctx, systemManagedAgentRunRequest{
		AgentID:            spec.ID,
		Purpose:            spec.Purpose,
		Model:              model,
		ParentSession:      guardianSession,
		Events:             events,
		Input:              input,
		InputUserEvidence:  userEvidence,
		Output:             output,
		Compaction:         compaction,
		CapabilityProfile:  profile,
		Tools:              tools,
		PolicyInstructions: instructions,
	})
	if err != nil {
		return result, err
	}
	if result.AssistantEvent == nil || strings.TrimSpace(result.Text) == "" {
		return result, fmt.Errorf("approval reviewer returned no final assessment")
	}
	return result, nil
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

type guardianQueryContext struct {
	queries *guardianQueries
	path    string
}
