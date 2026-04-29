package client

import "strings"

type PermissionDecision string

const (
	PermissionDecisionAutoSelect          PermissionDecision = "auto_select"
	PermissionDecisionAskUser             PermissionDecision = "ask_user"
	PermissionDecisionDeferExistingPolicy PermissionDecision = "defer_to_existing_policy"
)

type PermissionResolution struct {
	Decision PermissionDecision
	OptionID string
}

func ResolveApproveAllOnce(sessionMode string, agentID string, req RequestPermissionRequest) PermissionResolution {
	if !strings.EqualFold(strings.TrimSpace(sessionMode), "full_access") {
		return PermissionResolution{Decision: PermissionDecisionDeferExistingPolicy}
	}
	if optionID, ok := selectApproveAllOptionID(req.Options, agentID); ok {
		return PermissionResolution{
			Decision: PermissionDecisionAutoSelect,
			OptionID: optionID,
		}
	}
	return PermissionResolution{Decision: PermissionDecisionAskUser}
}

func (r PermissionResolution) AutoResponse() (RequestPermissionResponse, bool) {
	if r.Decision != PermissionDecisionAutoSelect || strings.TrimSpace(r.OptionID) == "" {
		return RequestPermissionResponse{}, false
	}
	return PermissionSelectedOutcome(r.OptionID), true
}

func PermissionSelectedOutcome(optionID string) RequestPermissionResponse {
	return RequestPermissionResponse{
		Outcome: PermissionOutcome{
			Outcome:  "selected",
			OptionID: strings.TrimSpace(optionID),
		},
	}
}

func SelectPermissionOptionID(options []PermissionOption, allowed bool) string {
	for _, option := range options {
		kind := strings.TrimSpace(strings.ToLower(option.Kind))
		switch {
		case allowed && kind == schemaPermAllowOnce:
			return strings.TrimSpace(option.OptionID)
		case !allowed && kind == schemaPermRejectOnce:
			return strings.TrimSpace(option.OptionID)
		}
	}
	if allowed {
		return "allow_once"
	}
	return "reject_once"
}

const (
	schemaPermAllowOnce   = "allow_once"
	schemaPermAllowAlways = "allow_always"
	schemaPermRejectOnce  = "reject_once"
)

func selectApproveAllOptionID(options []PermissionOption, _ string) (string, bool) {
	for _, option := range options {
		if kind := strings.TrimSpace(strings.ToLower(option.Kind)); kind == schemaPermAllowOnce || kind == schemaPermAllowAlways {
			if id := strings.TrimSpace(option.OptionID); id != "" {
				return id, true
			}
		}
	}
	if len(options) == 0 {
		return "", false
	}
	first := strings.TrimSpace(options[0].OptionID)
	if first == "" {
		return "", false
	}
	return first, true
}
