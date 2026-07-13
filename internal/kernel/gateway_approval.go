package kernel

import (
	"context"
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	gatewayapi "github.com/caelis-labs/caelis/ports/gateway"
)

type approvalRequesterFunc func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error)

func (f approvalRequesterFunc) RequestApproval(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return f(ctx, req)
}

type approvalReviewAccountingProvider interface {
	ApprovalReviewAccounting(context.Context, ApprovalReviewRequest, ApprovalReviewResult) (*UsageSnapshot, *session.EventInvocation, error)
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
	lifecycleCtx := turnCtx
	if detachedApprovalRequest(req) {
		lifecycleCtx = approvalCtx
	}
	mode, modeErr := g.currentApprovalMode(lifecycleCtx, req.SessionRef)
	if modeErr != nil {
		return agent.ApprovalResponse{}, modeErr
	}

	// Every normalized request enters the same Control-owned queue. Manual
	// requests publish an ACP permission only when they become the active head;
	// auto-review waits for that same head and resolves it through the registry.
	pending, err := handle.enqueueApproval(req, mode == ApprovalModeManual)
	if err != nil {
		return agent.ApprovalResponse{}, err
	}
	defer handle.releasePendingApproval(pending, "abandoned")

	if mode != ApprovalModeManual {
		select {
		case <-pending.activated:
		case <-pending.done:
			if pending.activationErr != nil {
				return agent.ApprovalResponse{}, pending.activationErr
			}
			return agent.ApprovalResponse{}, context.Canceled
		case <-approvalCtx.Done():
			return agent.ApprovalResponse{}, approvalCtx.Err()
		case <-lifecycleCtx.Done():
			return agent.ApprovalResponse{}, lifecycleCtx.Err()
		}
		response, err := g.resolveActiveAutoApproval(lifecycleCtx, approvalCtx, handle, req, reviewModel, mode)
		if err != nil {
			return agent.ApprovalResponse{}, err
		}
		if err := handle.resolvePendingApproval(pending, ApprovalDecision{
			Outcome:    response.Outcome,
			OptionID:   response.OptionID,
			Approved:   response.Approved,
			Reason:     response.Reason,
			ReviewText: response.ReviewText,
		}); err != nil {
			return agent.ApprovalResponse{}, err
		}
	}

	return waitForApprovalDecision(lifecycleCtx, approvalCtx, pending)
}

func waitForApprovalDecision(turnCtx context.Context, approvalCtx context.Context, pending *pendingApproval) (agent.ApprovalResponse, error) {
	if pending == nil {
		return agent.ApprovalResponse{}, context.Canceled
	}
	select {
	case decision := <-pending.decisions:
		return agent.ApprovalResponse{
			Outcome:    decision.Outcome,
			OptionID:   decision.OptionID,
			Approved:   decision.Approved,
			Reason:     decision.Reason,
			ReviewText: decision.ReviewText,
		}, nil
	case <-pending.done:
		if pending.activationErr != nil {
			return agent.ApprovalResponse{}, pending.activationErr
		}
		return agent.ApprovalResponse{}, context.Canceled
	case <-approvalCtx.Done():
		return agent.ApprovalResponse{}, approvalCtx.Err()
	case <-turnCtx.Done():
		return agent.ApprovalResponse{}, turnCtx.Err()
	}
}

// resolveActiveAutoApproval runs Guardian/Reviewer work only after the
// request reaches the Control queue head. Its result is returned to the common
// resolver path instead of bypassing the approval registry.
func (g *Gateway) resolveActiveAutoApproval(
	turnCtx context.Context,
	approvalCtx context.Context,
	handle *turnHandle,
	req *agent.ApprovalRequest,
	reviewModel model.LLM,
	mode ApprovalMode,
) (agent.ApprovalResponse, error) {

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
	reviewReq := ApprovalReviewRequest{
		SessionRef:     req.SessionRef,
		RunID:          req.RunID,
		TurnID:         req.TurnID,
		Mode:           mode,
		ReviewID:       reviewID,
		Model:          reviewModel,
		Approval:       cloneApprovalPayload(payload),
		RuntimeRequest: *req,
	}
	result, err := approver.Decide(approvalCtx, reviewReq)
	var reviewErrStatus ApprovalReviewStatus
	if err != nil {
		status, rationale, _ := approval.ReviewErrorOutcome(err)
		reviewErrStatus = status
		result = approval.FinalizeReviewResult(payload, ApprovalReviewResult{
			Approved:       false,
			Outcome:        string(ApprovalStatusRejected),
			Risk:           "unknown",
			Authorization:  "unknown",
			Rationale:      rationale,
			DisplayText:    FormatApprovalReviewText(false, "unknown", "unknown", rationale),
			DecisionSource: string(ApprovalModeAutoReview),
		})
	}
	response := approval.RuntimeResponseFromFinalReview(result)
	if strings.TrimSpace(result.DecisionSource) == "" {
		result.DecisionSource = string(ApprovalModeAutoReview)
	}

	terminal := cloneApprovalPayload(payload)
	terminal.Status = ApprovalStatusRejected
	if result.Approved {
		terminal.Status = ApprovalStatusApproved
	}
	terminal.ReviewStatus = approvalReviewTerminalStatus(result)
	if reviewErrStatus != "" {
		terminal.ReviewStatus = reviewErrStatus
	}
	terminal.ReviewText = strings.TrimSpace(result.DisplayText)
	terminal.Risk = strings.TrimSpace(result.Risk)
	terminal.Authorization = strings.TrimSpace(result.Authorization)
	terminal.DecisionSource = strings.TrimSpace(result.DecisionSource)
	terminal.ReviewTrace = approval.CloneReviewTrace(result.Trace)
	usage, invocation := g.approvalReviewSessionAccounting(context.WithoutCancel(turnCtx), reviewReq, result)
	handle.publishApprovalReviewPayloadWithUsage(req, terminal, usage, invocation)
	_ = g.persistApprovalReviewSessionAccounting(context.WithoutCancel(turnCtx), req, usage, terminal.DecisionSource, invocation)

	// Do not abort the turn after repeated denials: a per-turn circuit breaker
	// overwrote the reviewer's rationale with a generic "too many approval requests" error.
	response.ReviewText = strings.TrimSpace(result.DisplayText)
	return response, nil
}

func (g *Gateway) approvalReviewSessionAccounting(ctx context.Context, req ApprovalReviewRequest, result ApprovalReviewResult) (*UsageSnapshot, *session.EventInvocation) {
	for _, candidate := range []any{g.approvalReviewer, g.approvalApprover} {
		provider, ok := candidate.(approvalReviewAccountingProvider)
		if !ok {
			continue
		}
		usage, invocation, err := provider.ApprovalReviewAccounting(ctx, req, result)
		if err != nil || usage == nil || usageSnapshotEmpty(*usage) {
			continue
		}
		return usage, invocation
	}
	usage, invocation, _ := g.approvalReviewTraceSessionAccounting(ctx, result.Trace)
	return usage, invocation
}

func (g *Gateway) approvalReviewTraceSessionAccounting(ctx context.Context, trace *approval.ReviewTrace) (*UsageSnapshot, *session.EventInvocation, error) {
	if g == nil || g.sessions == nil || trace == nil {
		return nil, nil, nil
	}
	sessionID := strings.TrimSpace(trace.SessionID)
	assistantEventID := strings.TrimSpace(trace.AssistantEventID)
	if sessionID == "" || assistantEventID == "" {
		return nil, nil, nil
	}
	events, err := g.sessions.Events(ctx, session.EventsRequest{
		SessionRef:       session.SessionRef{SessionID: sessionID},
		IncludeTransient: true,
	})
	if err != nil {
		return nil, nil, err
	}
	for _, event := range events {
		if event == nil || strings.TrimSpace(event.ID) != assistantEventID {
			continue
		}
		return gatewayapi.UsageSnapshotFromSessionEvent(event), approvalReviewAccountingInvocationFromSessionEvent(event), nil
	}
	return nil, nil, nil
}

func approvalReviewAccountingInvocationFromSessionEvent(event *session.Event) *session.EventInvocation {
	if event == nil || event.Invocation == nil {
		return nil
	}
	invocation := session.CloneEventInvocation(*event.Invocation)
	if invocation.Provider == "" && invocation.Model == "" {
		return nil
	}
	return &invocation
}

func (g *Gateway) persistApprovalReviewSessionAccounting(ctx context.Context, req *agent.ApprovalRequest, usage *UsageSnapshot, source string, invocation *session.EventInvocation) error {
	if g == nil || g.sessions == nil || req == nil || usage == nil || usageSnapshotEmpty(*usage) {
		return nil
	}
	source = firstNonEmpty(strings.TrimSpace(source), string(ApprovalModeAutoReview))
	usageCopy := *usage
	return g.updateSessionState(ctx, req.SessionRef, session.ControlMutationGuard(session.ControlMutationPurposeApproval), func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		accounting := anyMapValue(next[StateUsageAccounting])
		if accounting == nil {
			accounting = map[string]any{}
		}
		total := UsageSnapshot{}
		existingProvider := anyStringValue(accounting["auto_review_provider"])
		if invocation != nil && strings.TrimSpace(invocation.Provider) != "" {
			existingProvider = strings.TrimSpace(invocation.Provider)
		}
		if existing := gatewayapi.UsageSnapshotFromMapForProvider(anyMapValue(accounting["auto_review"]), existingProvider); existing != nil {
			total = *existing
		}
		addUsageSnapshot(&total, usageCopy)
		accounting["auto_review"] = usageSnapshotMeta(total)
		accounting["auto_review_source"] = source
		if invocation != nil {
			if provider := strings.TrimSpace(invocation.Provider); provider != "" {
				accounting["auto_review_provider"] = provider
			}
			if modelName := strings.TrimSpace(invocation.Model); modelName != "" {
				accounting["auto_review_model"] = modelName
			}
			accounting["by_model"] = addUsageByModelState(accounting["by_model"], *invocation, usageCopy)
		}
		next[StateUsageAccounting] = accounting
		return next, nil
	})
}

func addUsageByModelState(existing any, invocation session.EventInvocation, usage UsageSnapshot) []any {
	invocation = session.CloneEventInvocation(invocation)
	if invocation.Provider == "" && invocation.Model == "" {
		return anySliceValue(existing)
	}
	rows := anySliceValue(existing)
	key := invocation.Provider + "\x00" + invocation.Model
	for i, row := range rows {
		item := anyMapValue(row)
		if item == nil {
			continue
		}
		if anyStringValue(item["provider"])+"\x00"+anyStringValue(item["model"]) != key {
			continue
		}
		total := UsageSnapshot{}
		if existingUsage := gatewayapi.UsageSnapshotFromMapForProvider(anyMapValue(item["usage"]), invocation.Provider); existingUsage != nil {
			total = *existingUsage
		}
		addUsageSnapshot(&total, usage)
		item["category"] = "auto_review"
		item["usage"] = usageSnapshotMeta(total)
		rows[i] = item
		return rows
	}
	rows = append(rows, map[string]any{
		"category": "auto_review",
		"provider": invocation.Provider,
		"model":    invocation.Model,
		"usage":    usageSnapshotMeta(usage),
	})
	return rows
}

func anySliceValue(value any) []any {
	if value == nil {
		return nil
	}
	switch typed := value.(type) {
	case []any:
		return append([]any(nil), typed...)
	case []map[string]any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func anyStringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func usageSnapshotMeta(usage UsageSnapshot) map[string]any {
	return map[string]any{
		"prompt_tokens":         usage.PromptTokens,
		"cached_input_tokens":   usage.CachedInputTokens,
		"completion_tokens":     usage.CompletionTokens,
		"reasoning_tokens":      usage.ReasoningTokens,
		"total_tokens":          usage.TotalTokens,
		"context_window_tokens": usage.ContextWindowTokens,
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
	if usage.ContextWindowTokens > total.ContextWindowTokens {
		total.ContextWindowTokens = usage.ContextWindowTokens
	}
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
