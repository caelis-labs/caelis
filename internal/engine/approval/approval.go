// Package approval defines the core-native permission policy used by the
// runtime loop before executing tools.
package approval

import (
	"context"
	"slices"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/model"
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
	OptionAllowOnce   = "allow_once"
	OptionAllowAlways = "allow_always"
	OptionRejectOnce  = "reject_once"
)

type Request struct {
	Session    session.Session
	TurnID     string
	Surface    string
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
		return Decision{Verdict: VerdictAllow}, nil
	})
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
