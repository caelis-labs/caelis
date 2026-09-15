package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/internal/kernel"
)

const guardianAssessmentMaxAttempts = 2

type guardianApprovalReviewer struct {
	queryNetwork  sandbox.Network
	sessions      session.Service
	systemAgents  systemManagedAgentRunner
	conversations *guardianConversationManager
	diagnostics   *slog.Logger
	accountingMu  sync.Mutex
	accounting    map[string]approvalReviewAccounting
	reviews       sync.WaitGroup
	resourcesMu   sync.Mutex
	residents     map[string]*guardianResident
	closed        bool
	observeReview func(guardianReviewMetrics)
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

// decide returns one fully resolved Guardian decision. The Guardian path owns
// strict model-output validation and must not pass through generic reviewer
// reconciliation that guesses options or lets an option override its outcome.
func (r *guardianApprovalReviewer) decide(ctx context.Context, req kernel.ApprovalReviewRequest) (result kernel.ApprovalReviewResult, resultErr error) {
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
		if resultErr != nil && r.diagnostics != nil {
			r.diagnostics.Warn("Guardian review failed", "session_id", req.SessionRef.SessionID, "review_id", req.ReviewID, "error", resultErr)
		}
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
	r.resourcesMu.Lock()
	resident := r.residents[ref.SessionID]
	delete(r.residents, ref.SessionID)
	r.resourcesMu.Unlock()
	if resident != nil {
		_ = resident.close()
	}
	// Draining comes first: an in-flight validated turn must not resurrect
	// the private conversation after its owner has forgotten it.
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
		if queryArgs[0].queries.runner != nil {
			runner = queryArgs[0].queries.runner
		}
		tools = queryArgs[0].queries.tools()
		profile = systemManagedAgentCapabilityReadOnly
		instructions = guardianEnvironmentContext(queryArgs[0].queries.network)
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
}
