package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
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
	NetworkEnabled  *bool    `json:"network_enabled,omitempty"`
}

// ToolContext is the stable tool-authorization input consumed by runtime-local
// policy decisions.
type ToolContext struct {
	Session session.Session    `json:"session"`
	State   map[string]any     `json:"state,omitempty"`
	Tool    tool.Definition    `json:"tool"`
	Call    tool.Call          `json:"call"`
	Sandbox sandbox.Descriptor `json:"sandbox"`
	Mode    string             `json:"mode,omitempty"`
	Options ModeOptions        `json:"options,omitempty"`
}

// Decision is the normalized policy result. Constraints are backend-neutral
// and can be forwarded into sandbox-aware tools.
type Decision struct {
	Action      Action                    `json:"action,omitempty"`
	Reason      string                    `json:"reason,omitempty"`
	Constraints sandbox.Constraints       `json:"constraints,omitempty"`
	Metadata    map[string]any            `json:"metadata,omitempty"`
	Approval    *session.ProtocolApproval `json:"approval,omitempty"`
}

type ToolInputDecodeError struct {
	Tool string
	Err  error
}

func (e *ToolInputDecodeError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Tool != "" {
		return fmt.Sprintf("decode tool call input for %s: %v", e.Tool, e.Err)
	}
	return fmt.Sprintf("decode tool call input: %v", e.Err)
}

func (e *ToolInputDecodeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
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
	out.Session = session.CloneSession(in.Session)
	out.State = maps.Clone(in.State)
	out.Tool = tool.CloneDefinition(in.Tool)
	out.Call = tool.CloneCall(in.Call)
	out.Sandbox = sandbox.CloneDescriptor(in.Sandbox)
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
	if in.NetworkEnabled != nil {
		value := *in.NetworkEnabled
		out.NetworkEnabled = &value
	}
	return out
}

// CloneDecision returns one isolated copy of one policy decision.
func CloneDecision(in Decision, err error) (Decision, error) {
	out := in
	out.Action = Action(strings.TrimSpace(string(in.Action)))
	out.Reason = strings.TrimSpace(in.Reason)
	out.Constraints = sandbox.NormalizeConstraints(in.Constraints)
	out.Metadata = maps.Clone(in.Metadata)
	if in.Approval != nil {
		approval := *in.Approval
		out.Approval = &approval
	}
	return out, err
}

// CallArgs decodes one tool-call input object for policy inspection. Malformed
// tool input is a policy error because authorization must not continue from a
// guessed empty argument set.
func CallArgs(call tool.Call) (map[string]any, error) {
	if len(call.Input) == 0 {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal(call.Input, &out); err != nil {
		return nil, &ToolInputDecodeError{Tool: strings.TrimSpace(call.Name), Err: err}
	}
	return out, nil
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
