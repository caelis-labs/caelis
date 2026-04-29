package policy

import (
	"context"
	"encoding/json"
	"maps"
	"strings"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
)

type Action string

const (
	ActionAllow       Action = "allow"
	ActionDeny        Action = "deny"
	ActionAskApproval Action = "ask_approval"
)

// ModeOptions are app-provided runtime facts used by policy presets. The SDK
// defines the shape, while the app decides actual roots and custom modes.
type ModeOptions struct {
	WorkspaceRoot   string   `json:"workspace_root,omitempty"`
	TempRoot        string   `json:"temp_root,omitempty"`
	ExtraReadRoots  []string `json:"extra_read_roots,omitempty"`
	ExtraWriteRoots []string `json:"extra_write_roots,omitempty"`
}

// ToolContext is the stable tool-authorization input consumed by runtime-local
// policy decisions.
type ToolContext struct {
	Session sdksession.Session    `json:"session"`
	State   map[string]any        `json:"state,omitempty"`
	Tool    sdktool.Definition    `json:"tool"`
	Call    sdktool.Call          `json:"call"`
	Sandbox sdksandbox.Descriptor `json:"sandbox"`
	Mode    string                `json:"mode,omitempty"`
	Options ModeOptions           `json:"options,omitempty"`
}

// Decision is the normalized policy result. Constraints are backend-neutral
// and can be forwarded into sandbox-aware tools.
type Decision struct {
	Action      Action                       `json:"action,omitempty"`
	Reason      string                       `json:"reason,omitempty"`
	Constraints sdksandbox.Constraints       `json:"constraints,omitempty"`
	Metadata    map[string]any               `json:"metadata,omitempty"`
	Approval    *sdksession.ProtocolApproval `json:"approval,omitempty"`
}

// Mode decides whether one tool call may run under one named permission model.
type Mode interface {
	Name() string
	DecideTool(context.Context, ToolContext) (Decision, error)
}

// Registry resolves named modes. The SDK provides a memory registry and
// built-in presets, while apps may register their own modes or plugin-backed
// extensions.
type Registry interface {
	Lookup(context.Context, string) (Mode, bool, error)
}

// NamedMode is a small adapter for one static mode name and decision function.
type NamedMode struct {
	ID     string
	Decide func(context.Context, ToolContext) (Decision, error)
}

func (m NamedMode) Name() string {
	return strings.TrimSpace(m.ID)
}

func (m NamedMode) DecideTool(ctx context.Context, input ToolContext) (Decision, error) {
	if m.Decide == nil {
		return Decision{Action: ActionAllow}, nil
	}
	return CloneDecision(m.Decide(ctx, CloneToolContext(input)))
}

// CloneToolContext returns one isolated copy of one policy tool context.
func CloneToolContext(in ToolContext) ToolContext {
	out := in
	out.Session = sdksession.CloneSession(in.Session)
	out.State = maps.Clone(in.State)
	out.Tool = sdktool.CloneDefinition(in.Tool)
	out.Call = sdktool.CloneCall(in.Call)
	out.Sandbox = sdksandbox.CloneDescriptor(in.Sandbox)
	out.Options = CloneModeOptions(in.Options)
	return out
}

// CloneModeOptions returns one normalized copy of one policy mode option set.
func CloneModeOptions(in ModeOptions) ModeOptions {
	out := in
	out.WorkspaceRoot = strings.TrimSpace(in.WorkspaceRoot)
	out.TempRoot = strings.TrimSpace(in.TempRoot)
	out.ExtraReadRoots = cloneStringSlice(in.ExtraReadRoots)
	out.ExtraWriteRoots = cloneStringSlice(in.ExtraWriteRoots)
	return out
}

// CloneDecision returns one isolated copy of one policy decision.
func CloneDecision(in Decision, err error) (Decision, error) {
	out := in
	out.Action = Action(strings.TrimSpace(string(in.Action)))
	out.Reason = strings.TrimSpace(in.Reason)
	out.Constraints = sdksandbox.NormalizeConstraints(in.Constraints)
	out.Metadata = maps.Clone(in.Metadata)
	if in.Approval != nil {
		approval := *in.Approval
		out.Approval = &approval
	}
	return out, err
}

// CallArgs decodes one tool-call input object for policy inspection.
func CallArgs(call sdktool.Call) map[string]any {
	if len(call.Input) == 0 {
		return nil
	}
	var out map[string]any
	_ = json.Unmarshal(call.Input, &out)
	return out
}

func cloneStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, item := range in {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
