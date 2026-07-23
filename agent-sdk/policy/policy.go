package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/internal/jsonvalue"
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
	WorkspaceRoot string `json:"workspace_root,omitempty"`
	TempRoot      string `json:"temp_root,omitempty"`
	// ExtraReadRoots are app-approved exceptions to sensitive-read policy.
	// Built-in sandboxes keep host reads broad and do not consume these roots.
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

// DecisionError reports a missing, empty, or invalid policy decision. Runtime
// callers must treat it as fail-closed and must not execute the tool.
type DecisionError struct {
	Mode   string
	Detail string
	Err    error
}

func (e *DecisionError) Error() string {
	if e == nil {
		return "<nil>"
	}
	detail := strings.TrimSpace(e.Detail)
	if detail == "" && e.Err != nil {
		detail = e.Err.Error()
	}
	if detail == "" {
		detail = "invalid decision"
	}
	if mode := strings.TrimSpace(e.Mode); mode != "" {
		return fmt.Sprintf("agent-sdk/policy: mode %q: %s", mode, detail)
	}
	return "agent-sdk/policy: " + detail
}

func (e *DecisionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *DecisionError) ErrorCode() errorcode.Code { return errorcode.FailedPrecondition }

// ProfileError reports that a requested policy profile could not be resolved.
// It is intentionally distinct from a deny decision so hosts can diagnose
// configuration and registry availability without guessing from strings.
type ProfileError struct {
	Profile string
	Detail  string
	Err     error
}

func (e *ProfileError) Error() string {
	if e == nil {
		return "<nil>"
	}
	detail := strings.TrimSpace(e.Detail)
	if detail == "" && e.Err != nil {
		detail = e.Err.Error()
	}
	if detail == "" {
		detail = "profile is unavailable"
	}
	if profile := strings.TrimSpace(e.Profile); profile != "" {
		return fmt.Sprintf("agent-sdk/policy: profile %q: %s", profile, detail)
	}
	return "agent-sdk/policy: " + detail
}

func (e *ProfileError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *ProfileError) ErrorCode() errorcode.Code { return errorcode.FailedPrecondition }

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

func (e *ToolInputDecodeError) ErrorCode() errorcode.Code { return errorcode.InvalidArgument }

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
		return Decision{}, &DecisionError{Mode: m.Name(), Detail: "decision function is required"}
	}
	decision, err := m.Decide(ctx, CloneToolContext(input))
	if err != nil {
		return CloneDecision(decision, err)
	}
	return NormalizeDecision(m.Name(), decision)
}

// CloneToolContext returns one isolated copy of one policy tool context.
func CloneToolContext(in ToolContext) ToolContext {
	out := in
	out.Session = session.CloneSession(in.Session)
	out.State = jsonvalue.CloneMap(in.State)
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
	out.Metadata = jsonvalue.CloneMap(in.Metadata)
	if in.Approval != nil {
		approval := session.CloneProtocolApproval(*in.Approval)
		out.Approval = &approval
	}
	return out, err
}

// NormalizeDecision validates and recursively clones one third-party policy
// decision. Only the three declared actions are accepted; in particular an
// empty action is never an implicit allow.
func NormalizeDecision(mode string, in Decision) (Decision, error) {
	out, _ := CloneDecision(in, nil)
	switch out.Action {
	case ActionAllow, ActionDeny, ActionAskApproval:
	default:
		detail := "decision action is required"
		if out.Action != "" {
			detail = fmt.Sprintf("unsupported decision action %q", out.Action)
		}
		return Decision{}, &DecisionError{Mode: mode, Detail: detail}
	}
	if err := validateConstraints(out.Constraints); err != nil {
		return Decision{}, &DecisionError{Mode: mode, Detail: "invalid constraints", Err: err}
	}
	if err := jsonvalue.ValidateMap(out.Metadata); err != nil {
		return Decision{}, &DecisionError{Mode: mode, Detail: "invalid metadata", Err: err}
	}
	return out, nil
}

func validateConstraints(in sandbox.Constraints) error {
	if !oneOf(string(in.Route), "", string(sandbox.RouteHost), string(sandbox.RouteSandbox)) {
		return fmt.Errorf("route %q is unsupported", in.Route)
	}
	if !oneOf(string(in.Permission), "", string(sandbox.PermissionDefault), string(sandbox.PermissionWorkspaceWrite), string(sandbox.PermissionFullAccess)) {
		return fmt.Errorf("permission %q is unsupported", in.Permission)
	}
	if !oneOf(string(in.Isolation), "", string(sandbox.IsolationHost), string(sandbox.IsolationProcess), string(sandbox.IsolationContainer)) {
		return fmt.Errorf("isolation %q is unsupported", in.Isolation)
	}
	if !oneOf(string(in.Network), "", string(sandbox.NetworkInherit), string(sandbox.NetworkEnabled), string(sandbox.NetworkDisabled)) {
		return fmt.Errorf("network %q is unsupported", in.Network)
	}
	for i, rule := range in.PathRules {
		if strings.TrimSpace(rule.Path) == "" {
			return fmt.Errorf("path_rules[%d].path is required", i)
		}
		if !oneOf(string(rule.Access), string(sandbox.PathAccessReadWrite), string(sandbox.PathAccessHidden)) {
			return fmt.Errorf("path_rules[%d].access %q is unsupported", i, rule.Access)
		}
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
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
