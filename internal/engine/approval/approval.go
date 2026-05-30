// Package approval defines the core-native permission policy used by the
// runtime loop before executing tools.
package approval

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/model"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/core/tool"
)

type Verdict string

const (
	VerdictAllow Verdict = "allow"
	VerdictAsk   Verdict = "ask"
	VerdictDeny  Verdict = "deny"
)

const (
	OptionAllowOnce    = "allow_once"
	OptionAllowAlways  = "allow_always"
	OptionRejectOnce   = "reject_once"
	OptionRejectAlways = "reject_always"
)

const StateRememberedApprovals = "caelis.approval.remembered"

const (
	ModeAutoReview = coreruntime.SessionModeAutoReview
	ModeManual     = coreruntime.SessionModeManual
)

type Request struct {
	Session    session.Session
	TurnID     string
	Surface    string
	Mode       string
	State      session.State
	Call       model.ToolCall
	Definition tool.Definition
}

type Decision struct {
	Verdict Verdict
	Reason  string
	Options []session.ApprovalOption
}

type Policy interface {
	ReviewToolCall(context.Context, Request) (Decision, error)
}

type PolicyFunc func(context.Context, Request) (Decision, error)

func (f PolicyFunc) ReviewToolCall(ctx context.Context, req Request) (Decision, error) {
	if f == nil {
		return Decision{Verdict: VerdictAllow}, nil
	}
	return f(ctx, req)
}

func AllowAll() Policy {
	return PolicyFunc(func(context.Context, Request) (Decision, error) {
		return Decision{Verdict: VerdictAllow}, nil
	})
}

func DenyAll(reason string) Policy {
	return PolicyFunc(func(context.Context, Request) (Decision, error) {
		return Decision{
			Verdict: VerdictDeny,
			Reason:  strings.TrimSpace(reason),
		}, nil
	})
}

func AskAll() Policy {
	return PolicyFunc(func(context.Context, Request) (Decision, error) {
		return Decision{
			Verdict: VerdictAsk,
			Options: []session.ApprovalOption{
				{ID: OptionAllowOnce, Name: "Allow once", Kind: "allow"},
				{ID: OptionAllowAlways, Name: "Allow always", Kind: "allow"},
				{ID: OptionRejectOnce, Name: "Reject", Kind: "reject"},
				{ID: OptionRejectAlways, Name: "Reject always", Kind: "reject"},
			},
		}, nil
	})
}

func AskTools(names ...string) Policy {
	wanted := normalizeNames(names)
	return PolicyFunc(func(ctx context.Context, req Request) (Decision, error) {
		if len(wanted) == 0 || slices.Contains(wanted, strings.ToLower(strings.TrimSpace(req.Call.Name))) {
			return AskAll().ReviewToolCall(ctx, req)
		}
		return Decision{}, nil
	})
}

func WithSessionMode(base Policy) Policy {
	return PolicyFunc(func(ctx context.Context, req Request) (Decision, error) {
		if remembered, ok := RememberedToolDecision(req.State, req.Call.Name); ok {
			return remembered, nil
		}
		if NormalizeMode(req.Mode) == ModeManual {
			return AskAll().ReviewToolCall(ctx, req)
		}
		if base == nil {
			return Decision{}, nil
		}
		return base.ReviewToolCall(ctx, req)
	})
}

func RememberedToolDecision(state session.State, toolName string) (Decision, bool) {
	entries, ok := rememberedApprovalEntries(state)
	if !ok {
		return Decision{}, false
	}
	entry, ok := entries[normalizeToolName(toolName)]
	if !ok {
		return Decision{}, false
	}
	values, ok := entry.(map[string]any)
	if !ok {
		return Decision{}, false
	}
	verdict := strings.ToLower(strings.TrimSpace(stringValue(values["verdict"])))
	reason := strings.TrimSpace(stringValue(values["reason"]))
	switch verdict {
	case string(VerdictAllow):
		return Decision{Verdict: VerdictAllow, Reason: firstNonEmpty(reason, "remembered approval allow_always")}, true
	case string(VerdictDeny):
		return Decision{Verdict: VerdictDeny, Reason: firstNonEmpty(reason, "remembered approval reject_always")}, true
	default:
		return Decision{}, false
	}
}

func RememberToolDecision(state session.State, toolName string, optionID string, reason string) bool {
	toolName = normalizeToolName(toolName)
	if state == nil || toolName == "" {
		return false
	}
	optionID = strings.ToLower(strings.TrimSpace(optionID))
	var verdict Verdict
	switch optionID {
	case OptionAllowAlways:
		verdict = VerdictAllow
	case OptionRejectAlways:
		verdict = VerdictDeny
	default:
		return false
	}
	entries, _ := rememberedApprovalEntries(state)
	if entries == nil {
		entries = map[string]any{}
	}
	entries = maps.Clone(entries)
	entries[toolName] = map[string]any{
		"tool":    toolName,
		"verdict": string(verdict),
		"reason":  strings.TrimSpace(firstNonEmpty(reason, optionID)),
	}
	state[StateRememberedApprovals] = entries
	return true
}

func RememberToolDecisionPatch(toolName string, optionID string, reason string) session.StatePatch {
	return func(state session.State) (session.State, error) {
		if state == nil {
			state = session.State{}
		} else {
			state = maps.Clone(state)
		}
		RememberToolDecision(state, toolName, optionID, reason)
		return state, nil
	}
}

func NormalizeMode(mode string) string {
	return coreruntime.NormalizeSessionMode(mode)
}

func normalizeNames(in []string) []string {
	out := make([]string, 0, len(in))
	for _, value := range in {
		if trimmed := strings.ToLower(strings.TrimSpace(value)); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func rememberedApprovalEntries(state session.State) (map[string]any, bool) {
	if len(state) == 0 {
		return nil, false
	}
	entries, ok := state[StateRememberedApprovals].(map[string]any)
	if !ok || len(entries) == 0 {
		return nil, false
	}
	return entries, true
}

func normalizeToolName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
