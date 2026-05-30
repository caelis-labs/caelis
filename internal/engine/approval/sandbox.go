package approval

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/session"
)

func WithSandboxEscalation(base Policy) Policy {
	return PolicyFunc(func(ctx context.Context, req Request) (Decision, error) {
		escalation, parseErr := sandboxEscalation(req.Call.Input)
		if parseErr != nil {
			return Decision{Verdict: VerdictDeny, Reason: parseErr.Error()}, nil
		}
		if escalation.Required && escalation.Justification == "" {
			return Decision{
				Verdict: VerdictDeny,
				Reason:  "sandbox_permissions=require_escalated requires a justification",
			}, nil
		}
		decision, err := reviewBase(ctx, base, req)
		if err != nil {
			return Decision{}, err
		}
		if !escalation.Required {
			return decision, nil
		}
		if decision.Verdict == VerdictDeny {
			return decision, nil
		}
		reason := ""
		if decision.Verdict == VerdictAsk {
			reason = decision.Reason
		}
		return Decision{
			Verdict: VerdictAsk,
			Reason:  escalationReason(reason, escalation.Justification),
			Options: oneShotOptions(),
		}, nil
	})
}

type sandboxEscalationRequest struct {
	Required      bool
	Justification string
}

func sandboxEscalation(raw json.RawMessage) (sandboxEscalationRequest, error) {
	if len(raw) == 0 {
		return sandboxEscalationRequest{}, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return sandboxEscalationRequest{}, nil
	}
	rawPermission, ok := fields["sandbox_permissions"]
	if !ok || len(rawPermission) == 0 {
		return sandboxEscalationRequest{}, nil
	}
	var permissionText string
	if err := json.Unmarshal(rawPermission, &permissionText); err != nil {
		return sandboxEscalationRequest{}, errors.New("sandbox_permissions must be a string")
	}
	permission, err := sandbox.NormalizePermissionRequest(permissionText)
	if err != nil {
		return sandboxEscalationRequest{}, err
	}
	if permission != sandbox.PermissionRequestRequireEscalated {
		return sandboxEscalationRequest{}, nil
	}
	justification, err := sandboxJustification(fields["justification"])
	if err != nil {
		return sandboxEscalationRequest{}, err
	}
	return sandboxEscalationRequest{
		Required:      true,
		Justification: justification,
	}, nil
}

func sandboxJustification(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", errors.New("justification must be a string")
	}
	return strings.TrimSpace(text), nil
}

func reviewBase(ctx context.Context, base Policy, req Request) (Decision, error) {
	if base == nil {
		return Decision{}, nil
	}
	return base.ReviewToolCall(ctx, req)
}

func escalationReason(reason string, justification string) string {
	reason = firstNonEmpty(reason, "host execution requires user approval")
	justification = strings.TrimSpace(justification)
	if justification == "" {
		return reason
	}
	return reason + ": " + justification
}

func oneShotOptions() []session.ApprovalOption {
	return []session.ApprovalOption{
		{ID: OptionAllowOnce, Name: "Allow once", Kind: "allow"},
		{ID: OptionRejectOnce, Name: "Reject", Kind: "reject"},
	}
}
