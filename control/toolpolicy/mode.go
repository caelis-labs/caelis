package toolpolicy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

// Advance this revision when changing the product-owned app approval contract;
// MCP catalog revisions do not change the scope of app consent.
const (
	desktopAtomicScopeRevision = "desktop-atomic-app-v2"
	desktopScriptScopeRevision = "desktop-script-app-v1"
)

// IsDesktopWorld identifies the product's desktop-world MCP tools by their
// Host-assigned plugin ID and supported server names.
func IsDesktopWorld(def tool.Definition) bool {
	pluginID, _ := def.Metadata[mcp.MetadataPluginID].(string)
	server, _ := def.Metadata[mcp.MetadataServer].(string)
	return mcp.IsDefinition(def) && pluginID == "desktop-world" && (server == "dtw" || server == "desktop-world")
}

// AtomicOperations is the app-scoped contract, independent of projected names.
func AtomicOperations() []string {
	return []string{"look", "capture", "readText", "wait", "waitForNewWindow", "click", "setValue", "move", "drag", "typeText", "keypress", "scroll", "focus", "quit"}
}

// Mode adds product source and APP requirements inside the final Policy entry.
// Base keeps native/callback restrictions. Rules is the canonical configuration
// reader; it is sampled for each call rather than frozen at Runtime activation.
type Mode struct {
	Base      policy.Mode
	Authority string
	Rules     func(context.Context) ([]Rule, func(), error)
}

// Registry binds the same product policy to ordinary and application profiles.
type Registry struct {
	Base      policy.Registry
	Authority string
	Rules     func(context.Context) ([]Rule, func(), error)
}

func (r Registry) Lookup(ctx context.Context, name string) (policy.Mode, bool, error) {
	base, found, err := r.Base.Lookup(ctx, name)
	if err != nil || !found {
		return nil, found, err
	}
	return Mode{Base: base, Authority: r.Authority, Rules: r.Rules}, true, nil
}

func (m Mode) Name() string { return m.Base.Name() }

func (m Mode) DecideTool(ctx context.Context, input policy.ToolContext) (policy.Decision, error) {
	decision, err := m.Base.DecideTool(ctx, input)
	if err != nil {
		return policy.Decision{}, err
	}
	decision, err = policy.NormalizeDecision(m.Name(), decision)
	if err != nil || decision.Action == policy.ActionDeny || !mcp.IsDefinition(input.Tool) {
		return decision, err
	}
	pluginID, _ := input.Tool.Metadata[mcp.MetadataPluginID].(string)
	server, _ := input.Tool.Metadata[mcp.MetadataServer].(string)
	if pluginID == "" || server == "" {
		return policy.Decision{}, errors.New("MCP source identity is incomplete")
	}
	rawName, _ := input.Tool.Metadata[mcp.MetadataTool].(string)
	desktop := IsDesktopWorld(input.Tool)
	desktopScript := desktop && rawName == "exec" && mcp.IsModelFunction(input.Tool)
	if desktop && rawName == "apps" {
		return decision, nil
	}
	scope := session.AuthorizationScope{
		Authority: m.Authority, Workspace: input.Session.CWD,
		SessionID: input.Session.SessionID, SessionIncarnation: input.Session.CreatedAt.UTC().Format(time.RFC3339Nano),
		SourceID: pluginID + "/" + server,
	}
	desktopGrant := "this exact application and all 14 DTW atomic operations in this Session"
	if desktop {
		if !desktopScript && !slices.Contains(AtomicOperations(), rawName) {
			return policy.Decision{Action: policy.ActionDeny, Reason: "tool is outside the DTW atomic operation contract"}, nil
		}
		var args map[string]json.RawMessage
		if err := json.Unmarshal(input.Call.Input, &args); err != nil {
			return policy.Decision{}, err
		}
		var app string
		if err := json.Unmarshal(args["app"], &app); err != nil || !ValidApplicationIdentity(app) {
			return policy.Decision{}, errors.New("invalid_app: app must be an exact application identifier from apps")
		}
		scope.CapabilitySet, scope.CapabilityRevision = "desktop-world/atomic-app", desktopAtomicScopeRevision
		scope.SourceRevision = desktopAtomicScopeRevision
		if desktopScript {
			scope.CapabilitySet, scope.CapabilityRevision = "desktop-world/script-app", desktopScriptScopeRevision
			scope.SourceRevision, _ = input.Tool.Metadata[mcp.MetadataSourceFingerprint].(string)
			desktopGrant = "DTW scripts in this exact application for this Session"
		}
		scope.PolicyRevision = scope.CapabilityRevision + "/" + m.Name()
		scope.ResourceKind, scope.Resource = "application", app
	} else {
		// Ordinary source consent is deliberately exact to this tool definition.
		fingerprint, _ := input.Tool.Metadata[mcp.MetadataSourceFingerprint].(string)
		if fingerprint == "" {
			return policy.Decision{}, errors.New("MCP callable fingerprint is incomplete")
		}
		scope.SourceRevision = fingerprint
		scope.PolicyRevision = "product-source-v1/" + m.Name()
		scope.Tool = input.Tool.Name
	}
	if err := scope.Validate(); err != nil || input.Session.CreatedAt.IsZero() {
		return policy.Decision{}, fmt.Errorf("invalid authorization scope: %w", errors.Join(err, errors.New("session incarnation is required")))
	}
	evidence := session.AuthorizationEvidence{}
	if input.Authorizations != nil {
		evidence, err = input.Authorizations(ctx)
		if err != nil {
			return policy.Decision{}, err
		}
	}
	digest, err := callDigest(ctx, input, decision)
	if err != nil {
		return policy.Decision{}, err
	}
	a := &session.ApprovalAuthorization{Schema: session.AuthorizationSchemaVersion, Scope: scope, CallDigest: digest, PerCall: decision.Action == policy.ActionAskApproval || !desktop, Revocation: evidence.Revocations[scope.Key()]}
	consented := slices.Contains(evidence.Consents, scope)
	var ruleID string
	if !desktop && !consented && m.Rules != nil {
		rules, release, err := m.Rules(ctx)
		if err != nil {
			return policy.Decision{}, err
		}
		if release != nil {
			release()
		}
		persistent := scope
		persistent.SessionID, persistent.SessionIncarnation = "", ""
		for _, rule := range rules {
			if !rule.Revoked && rule.Scope == persistent {
				consented = true
				ruleID = rule.ID
				break
			}
		}
	}
	if consented {

		if decision.Action == policy.ActionAskApproval {
			for _, option := range decision.Approval.Options {
				if option.Kind == "allow_once" || option.Kind == "allow_always" {
					a.Choices = append(a.Choices, session.AuthorizationChoice{OptionID: option.ID, Persistence: session.AuthorizationOnce})
				}
			}
		}
		decision.Authorization = a
		// A remembered source cannot waive a fresh per-call Ask.
		return m.withRuleValidation(decision, a, ruleID), nil
	}
	combined, err := presets.MCPApprovalDecision(input, decision.Constraints)
	if err != nil {
		return policy.Decision{}, err
	}
	if a.PerCall {
		combined.Reason += "; " + decision.Reason
	}
	if desktop {
		combined.Reason = "Application access requires approval for this Session"
		if a.PerCall {
			combined.Reason += "; " + decision.Reason
		}
		combined.Approval.Options = []session.ProtocolApprovalOption{{ID: "allow_session", Name: "Allow this app for this Session", Kind: "allow_once"}, {ID: "cancel", Name: "Cancel", Kind: "reject_once"}}
		combined.Approval.ToolCall.Title += " · " + scope.Resource
		combined.Approval.Subject.Targets = append(combined.Approval.Subject.Targets, session.KnownApprovalField(scope.Resource))
		combined.Approval.Subject.Scope = session.KnownApprovalField(desktopGrant)
		combined.Approval.Subject.Grants = []session.ApprovalGrant{{OptionID: "allow_session", Scope: session.KnownApprovalField(desktopGrant)}, {OptionID: "cancel", Scope: session.KnownApprovalField("no grant")}}
		a.Choices = []session.AuthorizationChoice{{OptionID: "allow_session", Persistence: session.AuthorizationSession}}
	} else {
		a.Choices = []session.AuthorizationChoice{{OptionID: "allow_once", Persistence: session.AuthorizationOnce}, {OptionID: "allow_session", Persistence: session.AuthorizationSession}, {OptionID: "allow_always", Persistence: session.AuthorizationPolicy}}
	}
	description := session.KnownApprovalField(combined.Reason)
	combined.Approval.Subject.Description = &description
	combined.Metadata = decision.Metadata
	if combined.Metadata == nil {
		combined.Metadata = map[string]any{}
	}
	combined.Metadata["approval_reason"] = combined.Reason
	combined.Validate = decision.Validate
	combined.Authorization = a
	return m.withRuleValidation(combined, a, ""), nil
}

// callDigest binds only effective dispatch values, not mutable observer metadata.
func callDigest(ctx context.Context, input policy.ToolContext, decision policy.Decision) (string, error) {
	execution := tool.InvocationFromContext(ctx)
	// Keep existing call digests stable. The parent and child share a Session
	// and Turn, so only the parent's ItemID comes from the relation.
	type digestParent struct {
		Kind      string                 `json:"kind,omitempty"`
		ID        string                 `json:"id"`
		Name      string                 `json:"name"`
		Execution tool.InvocationContext `json:"execution"`
	}
	var parent *digestParent
	if p := tool.ParentFromContext(ctx); p != nil {
		parentExecution := execution
		parentExecution.ItemID = p.ItemID
		parent = &digestParent{Kind: p.Kind, ID: p.ID, Name: p.Name, Execution: parentExecution}
	}
	raw, err := json.Marshal(struct {
		ID, Name    string
		Input       json.RawMessage
		Execution   tool.InvocationContext
		Parent      *digestParent
		Binding     tool.Binding
		Constraints any
	}{input.Call.ID, input.Call.Name, input.Call.Input, execution, parent, mcp.BindingForDefinition(input.Tool), decision.Constraints})
	if err != nil {
		return "", fmt.Errorf("cannot freeze Policy call: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// ValidApplicationIdentity follows DTW's platform-independent app_id parser.
// Validation preserves exact bytes; existence and native instance checks are DTW's.
func ValidApplicationIdentity(id string) bool {
	if id == "" || len(id) > 4096 || strings.TrimSpace(id) != id {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return false
		}
	}
	for _, part := range strings.FieldsFunc(id, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == "." || part == ".." {
			return false
		}
	}
	if !strings.Contains(id, "/") || strings.HasPrefix(id, "/") || strings.HasPrefix(id, "windows:") || strings.HasPrefix(id, "x11:") {
		return true
	}
	return len(id) > 2 && ((id[0] >= 'a' && id[0] <= 'z') || (id[0] >= 'A' && id[0] <= 'Z')) && id[1] == ':' && id[2] == '/'
}

// withRuleValidation keeps source-rule validation and consumption inside the
// canonical configuration transaction, including a newly selected Always.
func (m Mode) withRuleValidation(decision policy.Decision, a *session.ApprovalAuthorization, ruleID string) policy.Decision {
	baseValidate := decision.Validate
	decision.Validate = func(ctx context.Context, optionID string) (func(), error) {
		var baseRelease func()
		if baseValidate != nil {
			var err error
			baseRelease, err = baseValidate(ctx, optionID)
			if err != nil {
				return nil, err
			}
		}
		persistence, _ := a.Choice(optionID)
		if ruleID == "" && persistence != session.AuthorizationPolicy {
			return baseRelease, nil
		}
		if m.Rules == nil {
			if baseRelease != nil {
				baseRelease()
			}
			return nil, errors.New("policy configuration is unavailable")
		}
		rules, release, err := m.Rules(ctx)
		cleanup := func() {
			if release != nil {
				release()
			}
			if baseRelease != nil {
				baseRelease()
			}
		}
		if err != nil {
			cleanup()
			return nil, err
		}
		want := a.Scope
		want.SessionID, want.SessionIncarnation = "", ""
		for _, rule := range rules {
			if !rule.Revoked && rule.Scope == want && (ruleID == "" || rule.ID == ruleID) {
				return cleanup, nil
			}
		}
		cleanup()
		return nil, errors.New("policy source rule was revoked before dispatch")
	}
	return decision
}
