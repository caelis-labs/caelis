package services

import (
	"context"
	"errors"
	"strings"

	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	"github.com/OnslaughtSnail/caelis/core/session"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

type ApprovalService struct {
	services Services
}

type ApprovalDecisionRequest struct {
	Approval appviewmodel.ApprovalItem `json:"approval,omitempty"`
	Outcome  string                    `json:"outcome,omitempty"`
	OptionID string                    `json:"option_id,omitempty"`
	Approved bool                      `json:"approved,omitempty"`
	Reason   string                    `json:"reason,omitempty"`
}

func (s ApprovalService) Pending(ctx context.Context, ref session.Ref) ([]appviewmodel.ApprovalItem, error) {
	view, err := s.services.Views().Session(ctx, ref)
	if err != nil {
		return nil, err
	}
	return cloneApprovalItems(view.PendingApprovals), nil
}

func (s ApprovalService) Decision(req ApprovalDecisionRequest) (appviewmodel.ApprovalDecisionView, error) {
	optionID := strings.TrimSpace(req.OptionID)
	approved := req.Approved
	if optionID != "" {
		if option, ok := approvalOptionByID(req.Approval.Options, optionID); ok {
			approved = appviewmodel.ApprovalOptionAllows(option)
		}
	}
	outcome := normalizeApprovalOutcome(req.Outcome, optionID, approved)
	if outcome == "selected" && optionID == "" {
		return appviewmodel.ApprovalDecisionView{}, errors.New("app/services: selected approval decision requires option id")
	}
	return appviewmodel.ApprovalDecisionView{
		Outcome:  outcome,
		OptionID: optionID,
		Approved: approved,
		Reason:   strings.TrimSpace(req.Reason),
	}, nil
}

func (s ApprovalService) Submit(ctx context.Context, turn coreruntime.Turn, req ApprovalDecisionRequest) (appviewmodel.ApprovalDecisionView, error) {
	if turn == nil {
		return appviewmodel.ApprovalDecisionView{}, errors.New("app/services: active turn is required")
	}
	decision, err := s.Decision(req)
	if err != nil {
		return appviewmodel.ApprovalDecisionView{}, err
	}
	if err := turn.Submit(ctx, coreruntime.Submission{
		Kind: coreruntime.SubmissionApproval,
		Approval: &coreruntime.ApprovalDecision{
			Outcome:  decision.Outcome,
			OptionID: decision.OptionID,
			Approved: decision.Approved,
			Reason:   decision.Reason,
		},
	}); err != nil {
		return appviewmodel.ApprovalDecisionView{}, err
	}
	return decision, nil
}

func normalizeApprovalOutcome(outcome string, optionID string, approved bool) string {
	switch strings.ToLower(strings.TrimSpace(outcome)) {
	case "selected", "select":
		return "selected"
	case "approve", "approved", "allow", "allowed", "yes", "y":
		if strings.TrimSpace(optionID) != "" {
			return "selected"
		}
		return "approved"
	case "reject", "rejected", "deny", "denied", "no", "n":
		if strings.TrimSpace(optionID) != "" {
			return "selected"
		}
		return "rejected"
	case "":
		if strings.TrimSpace(optionID) != "" {
			return "selected"
		}
		if approved {
			return "approved"
		}
		return "rejected"
	default:
		return strings.ToLower(strings.TrimSpace(outcome))
	}
}

func approvalOptionByID(options []session.ApprovalOption, id string) (session.ApprovalOption, bool) {
	id = strings.TrimSpace(id)
	for _, option := range options {
		if strings.TrimSpace(option.ID) == id {
			return option, true
		}
	}
	return session.ApprovalOption{}, false
}

func cloneApprovalItems(in []appviewmodel.ApprovalItem) []appviewmodel.ApprovalItem {
	if len(in) == 0 {
		return nil
	}
	out := make([]appviewmodel.ApprovalItem, 0, len(in))
	for _, item := range in {
		item.Options = append([]session.ApprovalOption(nil), item.Options...)
		item.Actions = append([]appviewmodel.ApprovalAction(nil), item.Actions...)
		out = append(out, item)
	}
	return out
}
