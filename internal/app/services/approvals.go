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

type ApprovalPanelRequest struct {
	SessionRef session.Ref `json:"session_ref,omitempty"`
}

type ApprovalDecisionRequest struct {
	Approval appviewmodel.ApprovalItem `json:"approval,omitempty"`
	Outcome  string                    `json:"outcome,omitempty"`
	OptionID string                    `json:"option_id,omitempty"`
	Approved bool                      `json:"approved,omitempty"`
	Reason   string                    `json:"reason,omitempty"`
}

func (s ApprovalService) Panel(ctx context.Context, req ApprovalPanelRequest) (appviewmodel.ApprovalPanelView, error) {
	ref := defaultSessionRef(s.services.Runtime(), req.SessionRef)
	panel := appviewmodel.ApprovalPanelView{SessionRef: ref}
	if status, active, err := s.services.Controllers().Status(ctx, ref); err != nil {
		return appviewmodel.ApprovalPanelView{}, err
	} else if active {
		panel.Scope = "controller"
		panel.CurrentMode = firstNonEmpty(status.Mode, coreruntime.SessionModeAutoReview)
		panel.ControllerAgent = strings.TrimSpace(status.Agent)
		panel.RemoteSessionID = strings.TrimSpace(status.RemoteSessionID)
		panel.ModeOptions = approvalModeChoicesFromController(status.ModeOptions, panel.CurrentMode)
	} else {
		current, err := s.services.Modes().Current(ctx, ref)
		if err != nil {
			return appviewmodel.ApprovalPanelView{}, err
		}
		choices, err := s.services.Modes().List(ctx)
		if err != nil {
			return appviewmodel.ApprovalPanelView{}, err
		}
		panel.Scope = "session"
		panel.CurrentMode = firstNonEmpty(current.ID, coreruntime.SessionModeAutoReview)
		panel.CurrentModeName = strings.TrimSpace(current.Name)
		panel.ModeOptions = approvalModeChoicesFromSession(choices, panel.CurrentMode)
	}
	if panel.CurrentModeName == "" {
		panel.CurrentModeName = approvalModeName(panel.ModeOptions, panel.CurrentMode)
	}
	if strings.TrimSpace(ref.SessionID) != "" {
		pending, err := s.Pending(ctx, ref)
		if err != nil {
			return appviewmodel.ApprovalPanelView{}, err
		}
		panel.Pending = pending
	}
	panel.Actions = approvalPanelActions(panel)
	return panel, nil
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

func approvalModeChoicesFromSession(choices []ModeChoice, current string) []appviewmodel.ApprovalModeChoice {
	out := make([]appviewmodel.ApprovalModeChoice, 0, len(choices))
	for _, choice := range choices {
		id := strings.TrimSpace(choice.ID)
		if id == "" {
			continue
		}
		out = append(out, appviewmodel.ApprovalModeChoice{
			ID:          id,
			Name:        strings.TrimSpace(choice.Name),
			Description: strings.TrimSpace(choice.Description),
			Current:     strings.EqualFold(id, strings.TrimSpace(current)),
			Command:     "/approval " + id,
		})
	}
	return out
}

func approvalModeChoicesFromController(choices []ControllerMode, current string) []appviewmodel.ApprovalModeChoice {
	out := make([]appviewmodel.ApprovalModeChoice, 0, len(choices))
	for _, choice := range choices {
		id := strings.TrimSpace(choice.ID)
		if id == "" {
			continue
		}
		out = append(out, appviewmodel.ApprovalModeChoice{
			ID:          id,
			Name:        strings.TrimSpace(choice.Name),
			Description: strings.TrimSpace(choice.Description),
			Current:     strings.EqualFold(id, strings.TrimSpace(current)) || strings.EqualFold(strings.TrimSpace(choice.Name), strings.TrimSpace(current)),
			Command:     "/approval " + id,
		})
	}
	return out
}

func approvalPanelActions(panel appviewmodel.ApprovalPanelView) []appviewmodel.ApprovalPanelAction {
	actions := []appviewmodel.ApprovalPanelAction{{
		ID:      "approval.mode.toggle",
		Kind:    "toggle",
		Label:   "Toggle mode",
		Command: "/approval toggle",
		Enabled: true,
	}}
	for _, choice := range panel.ModeOptions {
		if strings.TrimSpace(choice.ID) == "" || strings.TrimSpace(choice.Command) == "" {
			continue
		}
		actions = append(actions, appviewmodel.ApprovalPanelAction{
			ID:      "approval.mode." + strings.TrimSpace(choice.ID),
			Kind:    "mode",
			Label:   firstNonEmpty(choice.Name, choice.ID),
			Command: choice.Command,
			Enabled: !choice.Current,
		})
	}
	return actions
}

func approvalModeName(choices []appviewmodel.ApprovalModeChoice, current string) string {
	current = strings.TrimSpace(current)
	for _, choice := range choices {
		if strings.EqualFold(strings.TrimSpace(choice.ID), current) || strings.EqualFold(strings.TrimSpace(choice.Name), current) {
			return firstNonEmpty(choice.Name, choice.ID)
		}
	}
	return current
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
