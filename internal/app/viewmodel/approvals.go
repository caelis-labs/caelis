package viewmodel

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/core/session"
)

type ApprovalAction struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Approved bool   `json:"approved,omitempty"`
	Primary  bool   `json:"primary,omitempty"`
}

type ApprovalDecisionView struct {
	Outcome  string `json:"outcome,omitempty"`
	OptionID string `json:"option_id,omitempty"`
	Approved bool   `json:"approved,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

func ApprovalActionsFromOptions(options []session.ApprovalOption) []ApprovalAction {
	if len(options) == 0 {
		return []ApprovalAction{
			{ID: "approve", Name: "Approve", Kind: "allow", Approved: true, Primary: true},
			{ID: "reject", Name: "Reject", Kind: "reject", Approved: false},
		}
	}
	out := make([]ApprovalAction, 0, len(options))
	primarySet := false
	for _, option := range options {
		action := ApprovalAction{
			ID:       strings.TrimSpace(option.ID),
			Name:     strings.TrimSpace(option.Name),
			Kind:     strings.TrimSpace(option.Kind),
			Approved: ApprovalOptionAllows(option),
		}
		if action.ID == "" {
			action.ID = strings.TrimSpace(action.Kind)
		}
		if action.Name == "" {
			action.Name = action.ID
		}
		if action.Approved && !primarySet {
			action.Primary = true
			primarySet = true
		}
		out = append(out, action)
	}
	return out
}

func ApprovalOptionAllows(option session.ApprovalOption) bool {
	value := strings.ToLower(strings.TrimSpace(option.Kind + " " + option.Name + " " + option.ID))
	return strings.Contains(value, "allow") ||
		strings.Contains(value, "approve") ||
		strings.Contains(value, "yes")
}
