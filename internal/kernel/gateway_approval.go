package kernel

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/approval"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

type approvalRequesterFunc func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error)

func (f approvalRequesterFunc) RequestApproval(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return f(ctx, req)
}

func (g *Gateway) resolveApprovalRequest(
	turnCtx context.Context,
	approvalCtx context.Context,
	handle *turnHandle,
	req *agent.ApprovalRequest,
	reviewModel model.LLM,
) (agent.ApprovalResponse, error) {
	if g == nil || handle == nil || req == nil {
		return agent.ApprovalResponse{}, nil
	}
	mode, modeErr := g.currentApprovalMode(turnCtx, req.SessionRef)
	if modeErr != nil {
		return agent.ApprovalResponse{}, modeErr
	}
	if mode == ApprovalModeManual {
		wait := handle.publishApproval(req)
		select {
		case decision := <-wait:
			return agent.ApprovalResponse{
				Outcome:    decision.Outcome,
				OptionID:   decision.OptionID,
				Approved:   decision.Approved,
				Reason:     decision.Reason,
				ReviewText: decision.ReviewText,
			}, nil
		case <-approvalCtx.Done():
			return agent.ApprovalResponse{}, approvalCtx.Err()
		case <-turnCtx.Done():
			return agent.ApprovalResponse{}, turnCtx.Err()
		}
	}

	payload := canonicalApprovalPayload(req)
	if payload == nil {
		payload = &ApprovalPayload{
			ToolName: strings.TrimSpace(firstNonEmpty(req.Tool.Name, req.Call.Name)),
			Status:   ApprovalStatusPending,
		}
	}

	reviewID := handle.nextApprovalReviewID()
	payload.ReviewID = reviewID
	payload.ReviewStatus = ApprovalReviewStatusInProgress
	payload.DecisionSource = string(ApprovalModeAutoReview)
	handle.publishApprovalReviewPayload(req, payload)

	approver := g.approvalApprover
	if approver == nil {
		if g.approvalReviewer != nil {
			approver = approval.ReviewerAdapter{Reviewer: g.approvalReviewer}
		} else {
			approver = denyingApprovalApprover{}
		}
	}
	if reviewModel == nil {
		reviewModel, _ = g.approvalReviewModel(turnCtx, req.SessionRef)
	}
	result, err := approver.Decide(approvalCtx, ApprovalReviewRequest{
		SessionRef:     req.SessionRef,
		RunID:          req.RunID,
		TurnID:         req.TurnID,
		Mode:           mode,
		ReviewID:       reviewID,
		Model:          reviewModel,
		Approval:       cloneApprovalPayload(payload),
		RuntimeRequest: *req,
	})
	if err != nil {
		rationale := "automatic approval review failed: " + err.Error()
		result = ApprovalReviewResult{
			Approved:       false,
			Outcome:        string(ApprovalStatusRejected),
			Risk:           "unknown",
			Authorization:  "unknown",
			Rationale:      rationale,
			DisplayText:    FormatApprovalReviewText(false, "unknown", "unknown", rationale),
			DecisionSource: string(ApprovalModeAutoReview),
		}
	}
	response := approval.RuntimeResponseFromReview(payload, result)
	result.OptionID = response.OptionID
	result.Outcome = response.Outcome
	if strings.TrimSpace(result.DisplayText) == "" {
		result.DisplayText = FormatApprovalReviewText(result.Approved, result.Risk, result.Authorization, result.Rationale)
	}
	if strings.TrimSpace(result.DecisionSource) == "" {
		result.DecisionSource = string(ApprovalModeAutoReview)
	}

	terminal := cloneApprovalPayload(payload)
	terminal.Status = ApprovalStatusRejected
	if result.Approved {
		terminal.Status = ApprovalStatusApproved
	}
	terminal.ReviewStatus = approvalReviewTerminalStatus(result)
	terminal.ReviewText = strings.TrimSpace(result.DisplayText)
	terminal.Risk = strings.TrimSpace(result.Risk)
	terminal.Authorization = strings.TrimSpace(result.Authorization)
	terminal.DecisionSource = strings.TrimSpace(result.DecisionSource)
	handle.publishApprovalReviewPayloadWithUsage(req, terminal, result.Usage)
	_ = g.persistApprovalReviewUsage(context.WithoutCancel(turnCtx), req, result.Usage, terminal.DecisionSource)

	if handle.recordApprovalReviewDecision(result.Approved) {
		return agent.ApprovalResponse{}, fmt.Errorf("automatic approval review rejected too many approval requests for this turn")
	}
	response.ReviewText = strings.TrimSpace(result.DisplayText)
	return response, nil
}

func (g *Gateway) persistApprovalReviewUsage(ctx context.Context, req *agent.ApprovalRequest, usage *UsageSnapshot, source string) error {
	if g == nil || g.sessions == nil || req == nil || usage == nil || usageSnapshotEmpty(*usage) {
		return nil
	}
	source = firstNonEmpty(strings.TrimSpace(source), string(ApprovalModeAutoReview))
	usageCopy := *usage
	return g.sessions.UpdateState(ctx, req.SessionRef, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		accounting := anyMapValue(next[StateUsageAccounting])
		if accounting == nil {
			accounting = map[string]any{}
		}
		total := UsageSnapshot{}
		if existing := UsageSnapshotFromMap(anyMapValue(accounting["auto_review"])); existing != nil {
			total = *existing
		}
		addUsageSnapshot(&total, usageCopy)
		accounting["auto_review"] = usageSnapshotMeta(total)
		accounting["auto_review_source"] = source
		next[StateUsageAccounting] = accounting
		return next, nil
	})
}

func usageSnapshotMeta(usage UsageSnapshot) map[string]any {
	return map[string]any{
		"prompt_tokens":       usage.PromptTokens,
		"cached_input_tokens": usage.CachedInputTokens,
		"completion_tokens":   usage.CompletionTokens,
		"reasoning_tokens":    usage.ReasoningTokens,
		"total_tokens":        usage.TotalTokens,
	}
}

func usageSnapshotEmpty(usage UsageSnapshot) bool {
	return usage.PromptTokens == 0 &&
		usage.CachedInputTokens == 0 &&
		usage.CompletionTokens == 0 &&
		usage.ReasoningTokens == 0 &&
		usage.TotalTokens == 0
}

func addUsageSnapshot(total *UsageSnapshot, usage UsageSnapshot) {
	if total == nil {
		return
	}
	total.PromptTokens += usage.PromptTokens
	total.CachedInputTokens += usage.CachedInputTokens
	total.CompletionTokens += usage.CompletionTokens
	total.ReasoningTokens += usage.ReasoningTokens
	total.TotalTokens += usage.TotalTokens
}

func approvalOptionIDForDecision(options []ApprovalOption, approved bool) string {
	wantKind := "reject_once"
	wantID := "reject_once"
	if approved {
		wantKind = "allow_once"
		wantID = "allow_once"
	}
	for _, option := range options {
		kind := strings.ToLower(strings.TrimSpace(option.Kind))
		id := strings.TrimSpace(option.ID)
		if id == "" {
			continue
		}
		if kind == wantKind {
			return id
		}
	}
	for _, option := range options {
		id := strings.TrimSpace(option.ID)
		if id == "" {
			continue
		}
		if strings.EqualFold(id, wantID) {
			return id
		}
	}
	return ""
}

type approvalModelResolver = approval.ModelResolver

func (g *Gateway) approvalReviewModel(ctx context.Context, ref session.SessionRef) (model.LLM, error) {
	if g == nil || g.resolver == nil {
		return nil, fmt.Errorf("gateway: approval review model resolver is unavailable")
	}
	resolver, ok := g.resolver.(approvalModelResolver)
	if !ok || resolver == nil {
		return nil, fmt.Errorf("gateway: approval review model resolver is unsupported")
	}
	return resolver.ResolveApprovalModel(ctx, ref)
}

func (g *Gateway) currentApprovalMode(ctx context.Context, ref session.SessionRef) (ApprovalMode, error) {
	if g == nil || g.sessions == nil {
		return ApprovalModeAutoReview, fmt.Errorf("gateway: sessions service unavailable")
	}
	state, err := g.sessions.SnapshotState(ctx, ref)
	if err != nil {
		return ApprovalModeAutoReview, wrapSessionError(err)
	}
	return CurrentApprovalModeOrDefault(state, g.defaultApprovalMode), nil
}
