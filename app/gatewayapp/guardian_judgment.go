package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/kernel"
	"github.com/google/uuid"
)

// The classifier evaluates original options and two evidence gates in one call.
// It shares Guardian's cancellation and settlement, but never retrieves tool
// evidence, waits for Tasks, initializes a query sandbox or generates a rationale.
func (r *guardianApprovalReviewer) runGuardianJudgment(ctx context.Context, req kernel.ApprovalReviewRequest, attempts *guardianInvocationCollector) (kernel.ApprovalReviewResult, error) {
	if err := guardianScreenOptions(req.Approval); err != nil {
		return kernel.ApprovalReviewResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resident, _, release, err := r.acquireResident(ctx, req.SessionRef)
	if err != nil {
		return kernel.ApprovalReviewResult{}, err
	}
	defer release()
	stop := context.AfterFunc(resident.ctx, cancel)
	defer stop()
	events, _, err := resident.projection.read(ctx, r.sessions, req.SessionRef)
	if err != nil {
		return kernel.ApprovalReviewResult{}, err
	}
	request, err := guardianJudgmentRequest(req, events)
	if err != nil {
		var failure *guardianScreenError
		if errors.As(err, &failure) {
			return kernel.ApprovalReviewResult{}, err
		}
		return kernel.ApprovalReviewResult{}, &guardianScreenError{reason: "input_invalid", cause: err}
	}
	response, err := req.Judgment.Evaluate(ctx, request)
	outcome := "completed"
	if err != nil {
		outcome = "failed"
	}
	if ctx.Err() != nil {
		outcome = "cancelled"
		err = ctx.Err()
	}
	provider := ""
	if named, ok := req.Judgment.(interface{ ProviderName() string }); ok {
		provider = named.ProviderName()
	}
	attempts.collect(model.Invocation{ID: uuid.NewString(), Provider: provider, Model: firstNonEmpty(response.Model, req.Judgment.Name()), Outcome: outcome, Usage: model.Usage{Reported: response.Model != "", PromptTokens: response.Usage.InputTokens, CompletionTokens: response.Usage.OutputTokens, TotalTokens: response.Usage.InputTokens + response.Usage.OutputTokens}})
	if err != nil {
		return kernel.ApprovalReviewResult{}, &guardianScreenError{reason: "provider_unavailable", cause: err}
	}
	selected, err := guardianScreenSelection(req, response)
	if err != nil {
		return kernel.ApprovalReviewResult{}, &guardianScreenError{reason: "judgment_inconclusive", cause: err}
	}
	return finalizeGuardianDecision(req.Approval, guardianReviewModelOutput{OptionID: selected.ID})
}

func guardianJudgmentRequest(req kernel.ApprovalReviewRequest, events []*session.Event) (judgment.Request, error) {
	if err := guardianScreenOptions(req.Approval); err != nil {
		return judgment.Request{}, err
	}
	state, err := guardianScreenState(req, events)
	if err != nil {
		return judgment.Request{}, err
	}
	options := make(map[string]approval.Option, len(req.Approval.Options))
	for _, option := range req.Approval.Options {
		options[option.ID] = option
	}
	request := judgment.Request{State: state, Questions: map[string]judgment.Question{
		"decision":          {Type: judgment.Choice, Instructions: guardianScreenPrompt(), Criteria: options},
		"material_unknown":  guardianScreenUnknownQuestion(),
		"visible_violation": guardianScreenViolationQuestion(),
	}}
	encoded, err := json.Marshal(request)
	if err != nil {
		return judgment.Request{}, err
	}
	// A conservative byte bound also fits the provider's token budget for CJK.
	// Dropping earlier user constraints to fit would change approval semantics.
	if len(encoded) > 24000 {
		return judgment.Request{}, &guardianScreenError{reason: "input_budget", cause: fmt.Errorf("canonical evidence exceeds classifier input budget")}
	}
	return request, nil
}
