package gatewayapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func (r *guardianApprovalReviewer) runGuardianReview(
	ctx context.Context,
	req kernel.ApprovalReviewRequest,
) (items guardianPromptItems, prompt *session.Event, assistant *session.Event, assessment guardianReviewModelOutput, reviewErr error) {
	started := kernel.AutoReviewStarted(ctx)
	ctx, cancel := kernel.WithAutoReviewBudget(ctx)
	defer cancel()
	var queries *guardianQueries
	var release func()
	metrics := guardianReviewMetrics{ReviewID: req.ReviewID}
	metrics.Model = req.Model.Name()
	if origin := req.RuntimeRequest.Origin; origin != nil {
		metrics.Origin = string(origin.Role) + "/" + string(origin.Endpoint)
	}
	defer func() {
		if queries != nil {
			queries.metrics(&metrics)
			if err := queries.finish(); err != nil && r.diagnostics != nil {
				r.diagnostics.Warn("Guardian scratch cleanup failed", "review_id", req.ReviewID, "error", err)
			}
		}
		metrics.TotalMS = guardianElapsed(started)
		metrics.OptionID = assessment.OptionID
		metrics.Outcome = "decided"
		if reviewErr != nil {
			metrics.Outcome = "unavailable"
		}
		if ctx.Err() != nil {
			metrics.Outcome = "cancelled"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				metrics.Outcome = "timed_out"
			}
		}
		if items.MandatoryInputTooLarge {
			metrics.Outcome = "invalid_request"
		}
		if release != nil {
			release()
		}
		if r.observeReview != nil {
			r.observeReview(metrics)
		}
		if r.diagnostics != nil {
			r.diagnostics.Info("Guardian review completed", "session_id", req.SessionRef.SessionID, "metrics", metrics)
		}
	}()
	var resident *guardianResident
	var err error
	metrics.ControlQueueMS = guardianElapsed(started)
	resident, queries, release, err = r.acquireResident(ctx, req.SessionRef)
	metrics.QueueMS = guardianElapsed(started)
	metrics.LaneQueueMS = metrics.QueueMS - metrics.ControlQueueMS
	if err != nil {
		return items, nil, nil, assessment, err
	}
	metrics.SandboxReused = queries.runtime != nil
	stop := context.AfterFunc(resident.ctx, cancel)
	defer stop()
	queries.begin(req.Model)
	ctx = model.WithInvocationAdmission(ctx, queries.admit)
	ctx = model.WithInvocationObserver(ctx, queries.invocation)
	activeSession, err := r.sessions.Session(ctx, req.SessionRef)
	if err != nil {
		return guardianPromptItems{}, nil, nil, guardianReviewModelOutput{}, err
	}
	if r.conversations == nil {
		return guardianPromptItems{}, nil, nil, guardianReviewModelOutput{}, fmt.Errorf("approval reviewer requires an in-memory conversation manager")
	}
	parentEvents, sourceCursor, err := resident.projection.read(ctx, r.sessions, req.SessionRef)
	if err != nil {
		return guardianPromptItems{}, nil, nil, guardianReviewModelOutput{}, err
	}
	forkRef := guardianConversationForkFromApproval(req)
	conversation, err := r.conversations.fork(req.SessionRef.SessionID, forkRef, parentEvents, sourceCursor)
	if err != nil {
		return guardianPromptItems{}, nil, nil, guardianReviewModelOutput{}, err
	}
	metrics.SourceThrough = conversation.SourceCursor.EventSeq
	outputSpec, err := guardianOutputSpecForModel(req.Model, req.Approval)
	if err != nil {
		return guardianPromptItems{}, nil, nil, guardianReviewModelOutput{}, err
	}
	turnKey := fmt.Sprintf("%d:%s", conversation.Version, req.ReviewID)
	windowReq := req
	windowReq.ReviewID = turnKey
	compactionCfg := guardianCompactionConfig(req.Model, outputSpec)
	historyEvents, promptItems, err := guardianWindow(conversation, windowReq, outputSpec)
	metrics.ContextTrimmed = promptItems.ContextTrimmed
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
	metrics.PrepareMS = guardianElapsed(started) - metrics.QueueMS
	var lastAssistantEvent *session.Event
	var lastParseErr error
	for attempt := 0; attempt < guardianAssessmentMaxAttempts; attempt++ {
		attemptHistory := historyEvents
		if lastParseErr != nil {
			attemptHistory = append(session.CloneEvents(historyEvents), guardianEvidenceEvent("The previous response could not be used: "+lastParseErr.Error()+". Return only option_id for an allow option; include a nonempty rationale only for a reject option. Choose from this request's options."))
		}
		runResult, err := r.runGuardianAgent(ctx, req.Model, activeSession, attemptHistory, promptItems.Text, promptItems.UserEvidence, outputSpec, compactionCfg, guardianQueryContext{queries: queries})
		metrics.RuntimeReused = runResult.RuntimeReused
		metrics.PrepareMS += runResult.PrepareMS
		if err != nil {
			return promptItems, promptEvent, runResult.AssistantEvent, guardianReviewModelOutput{}, err
		}
		lastAssistantEvent = runResult.AssistantEvent
		parsed, err := parseGuardianAssessmentForMode(runResult.Text, outputSpec.Mode, options)
		if err != nil {
			lastParseErr = err
			continue
		}
		if err := ctx.Err(); err != nil {
			return promptItems, promptEvent, runResult.AssistantEvent, guardianReviewModelOutput{}, err
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
