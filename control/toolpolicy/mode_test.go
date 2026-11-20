package toolpolicy

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

func policyFixture(t *testing.T, remote string) policy.ToolContext {
	t.Helper()
	return policy.ToolContext{
		Session: session.Session{SessionRef: session.SessionRef{SessionID: "s"}, CWD: "/workspace", CreatedAt: time.Unix(100, 0)},
		Tool: tool.Definition{Name: "dtw__" + remote, Metadata: map[string]any{
			mcp.MetadataKind: mcp.KindTool, mcp.MetadataPluginID: "desktop-world", mcp.MetadataServer: "dtw",
			mcp.MetadataTool: remote, mcp.MetadataSourceFingerprint: "per-tool-" + remote,
		}},
		Call: tool.Call{ID: "provider-call", Name: "dtw__" + remote, Input: json.RawMessage(`{"app":"com.example.A"}`)},
	}
}

func TestDesktopPolicyMatchesOnlyHostAssignedPluginAndSupportedServers(t *testing.T) {
	mode := Mode{Base: presets.WorkspaceWriteMode(), Authority: "owner"}
	for _, tc := range []struct {
		pluginID, server string
		want             bool
	}{
		{"desktop-world", "dtw", true},
		{"desktop-world", "desktop-world", true},
		{"configured", "dtw", false},
		{"configured", "desktop-world", false},
		{"desktop-world ", "dtw", false},
		{"Desktop-world", "dtw", false},
		{"desktop-world", "dtw ", false},
		{"desktop-world", "DTW", false},
		{"desktop-world", "other", false},
	} {
		t.Run(tc.pluginID+"/"+tc.server, func(t *testing.T) {
			input := policyFixture(t, "apps")
			input.Tool.Metadata[mcp.MetadataPluginID] = tc.pluginID
			input.Tool.Metadata[mcp.MetadataServer] = tc.server
			if got := IsDesktopWorld(input.Tool); got != tc.want {
				t.Fatalf("IsDesktopWorld = %v, want %v", got, tc.want)
			}
			wantAction := policy.ActionAskApproval
			if tc.want {
				wantAction = policy.ActionAllow
			}
			d, err := mode.DecideTool(t.Context(), input)
			if err != nil || d.Action != wantAction || (d.Authorization == nil) != tc.want {
				t.Fatalf("apps: %+v %v, want %s", d, err, wantAction)
			}
		})
	}
	def := policyFixture(t, "apps").Tool
	delete(def.Metadata, mcp.MetadataKind)
	if IsDesktopWorld(def) {
		t.Fatal("non-MCP definition matched")
	}
}

func TestDesktopPolicySharesOnlyExactSessionApplicationScope(t *testing.T) {
	mode := Mode{Base: presets.WorkspaceWriteMode(), Authority: "owner"}
	input := policyFixture(t, "look")
	first, err := mode.DecideTool(t.Context(), input)
	if err != nil || first.Action != policy.ActionAskApproval || len(first.Approval.Options) != 2 || first.Authorization == nil {
		t.Fatalf("first: %+v %v", first, err)
	}
	if err := first.Authorization.Validate(first.Approval); err != nil {
		t.Fatal(err)
	}
	for _, remote := range AtomicOperations() {
		current := policyFixture(t, remote)
		current.Authorizations = func(context.Context) (session.AuthorizationEvidence, error) {
			return session.AuthorizationEvidence{Consents: []session.AuthorizationScope{first.Authorization.Scope}}, nil
		}
		decision, err := mode.DecideTool(t.Context(), current)
		if err != nil || decision.Action != policy.ActionAllow || decision.Authorization.Scope != first.Authorization.Scope {
			t.Fatalf("%s: %+v %v", remote, decision, err)
		}
	}
	for name, mutate := range map[string]func(*policy.ToolContext){
		"another APP":     func(in *policy.ToolContext) { in.Call.Input = json.RawMessage(`{"app":"com.example.B"}`) },
		"another Session": func(in *policy.ToolContext) { in.Session.SessionID = "other" },
		"new incarnation": func(in *policy.ToolContext) { in.Session.CreatedAt = in.Session.CreatedAt.Add(time.Second) },
		"another server":  func(in *policy.ToolContext) { in.Tool.Metadata[mcp.MetadataServer] = "desktop-world" },
	} {
		t.Run(name, func(t *testing.T) {
			in := policyFixture(t, "click")
			in.Authorizations = func(context.Context) (session.AuthorizationEvidence, error) {
				return session.AuthorizationEvidence{Consents: []session.AuthorizationScope{first.Authorization.Scope}}, nil
			}
			mutate(&in)
			d, e := mode.DecideTool(t.Context(), in)
			if e != nil || d.Action != policy.ActionAskApproval {
				t.Fatalf("scope expanded: %+v %v", d, e)
			}
		})
	}
	input = policyFixture(t, "exec")
	input.Authorizations = func(context.Context) (session.AuthorizationEvidence, error) {
		return session.AuthorizationEvidence{Consents: []session.AuthorizationScope{first.Authorization.Scope}}, nil
	}
	if d, err := mode.DecideTool(t.Context(), input); err != nil || d.Action != policy.ActionDeny {
		t.Fatalf("operation outside the allowlist: %+v %v", d, err)
	}
}

func TestDesktopPolicyRequiresFreshApprovalForV1Consent(t *testing.T) {
	mode := Mode{Base: presets.WorkspaceWriteMode(), Authority: "owner"}
	input := policyFixture(t, "look")
	old := session.AuthorizationScope{
		Authority: "owner", Workspace: "/workspace", SessionID: "s", SessionIncarnation: input.Session.CreatedAt.UTC().Format(time.RFC3339Nano),
		SourceID: "desktop-world/dtw", SourceRevision: "package-launch-v1", PolicyRevision: "desktop-atomic-app-v1/" + mode.Name(),
		CapabilitySet: "desktop-world/atomic-app", CapabilityRevision: "actual-catalog-v1", ResourceKind: "application", Resource: "com.example.A",
	}
	input.Authorizations = func(context.Context) (session.AuthorizationEvidence, error) {
		return session.AuthorizationEvidence{Consents: []session.AuthorizationScope{old}}, nil
	}
	d, err := mode.DecideTool(t.Context(), input)
	if err != nil || d.Action != policy.ActionAskApproval || d.Authorization == nil {
		t.Fatalf("v1 consent was not replaced by a new Ask: %+v %v", d, err)
	}
	want := old
	want.SourceRevision = "desktop-atomic-app-v2"
	want.PolicyRevision = "desktop-atomic-app-v2/" + mode.Name()
	want.CapabilityRevision = "desktop-atomic-app-v2"
	if d.Authorization.Scope != want || d.Authorization.Scope.Key() == old.Key() {
		t.Fatalf("v2 scope: %+v, want %+v", d.Authorization.Scope, want)
	}
}

func TestPolicyConsentNeverWaivesDenyOrNewCallRestriction(t *testing.T) {
	input := policyFixture(t, "look")
	baseline := Mode{Base: presets.WorkspaceWriteMode(), Authority: "owner"}
	first, err := baseline.DecideTool(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.Authorizations = func(context.Context) (session.AuthorizationEvidence, error) {
		return session.AuthorizationEvidence{Consents: []session.AuthorizationScope{first.Authorization.Scope}}, nil
	}
	for _, action := range []policy.Action{policy.ActionDeny, policy.ActionAskApproval} {
		base := policy.NamedMode{ID: presets.ModeWorkspaceWrite, Decide: func(ctx context.Context, in policy.ToolContext) (policy.Decision, error) {
			if action == policy.ActionDeny {
				return policy.Decision{Action: action}, nil
			}
			return presets.MCPApprovalDecision(in, sandbox.Constraints{})
		}}
		mode := Mode{Base: base, Authority: "owner"}
		d, e := mode.DecideTool(t.Context(), input)
		if e != nil || d.Action != action {
			t.Fatalf("restriction waived: %+v %v", d, e)
		}
		input.Authorizations = nil
		d, e = mode.DecideTool(t.Context(), input)
		if e != nil || d.Action != action {
			t.Fatalf("combined restriction: %+v %v", d, e)
		}
		if action == policy.ActionAskApproval && (!d.Authorization.PerCall || len(d.Approval.Options) != 2) {
			t.Fatal("source and risk did not form one call-specific ticket")
		}
	}
}

func TestOrdinaryMCPAlwaysIsAnExactCurrentPolicyRule(t *testing.T) {
	input := policyFixture(t, "read")
	input.Tool.Metadata[mcp.MetadataPluginID] = "configured"
	var rules []Rule
	mode := Mode{Base: presets.WorkspaceWriteMode(), Authority: "owner", Rules: func(context.Context) ([]Rule, func(), error) { return rules, func() {}, nil }}
	d, err := mode.DecideTool(t.Context(), input)
	if err != nil || d.Action != policy.ActionAskApproval || len(d.Approval.Options) != 4 {
		t.Fatalf("ordinary source: %+v %v", d, err)
	}
	// A rule persisted before this migration must still match byte-for-byte.
	scope := session.AuthorizationScope{
		Authority: "owner", Workspace: "/workspace", SourceID: "configured/dtw", SourceRevision: "per-tool-read",
		PolicyRevision: "product-source-v1/" + mode.Name(), Tool: "dtw__read",
	}
	rules = []Rule{{ID: "original-rule", DecisionID: "original-decision", Scope: scope}}
	input.Session.SessionID = "next-session"
	d, err = mode.DecideTool(t.Context(), input)
	if err != nil || d.Action != policy.ActionAllow || d.Validate == nil {
		t.Fatalf("Always: %+v %v", d, err)
	}
	if release, err := d.Validate(t.Context(), ""); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
	changed := policyFixture(t, "read")
	changed.Tool.Metadata[mcp.MetadataPluginID] = "configured"
	changed.Tool.Metadata[mcp.MetadataSourceFingerprint] = "per-tool-read-updated"
	if refreshed, err := mode.DecideTool(t.Context(), changed); err != nil || refreshed.Action != policy.ActionAskApproval {
		t.Fatalf("changed fingerprint reused an exact rule: %+v %v", refreshed, err)
	}
	rules[0].Revoked = true
	if _, err := d.Validate(t.Context(), ""); err == nil {
		t.Fatal("revocation did not invalidate pending dispatch")
	}
	d, err = mode.DecideTool(t.Context(), input)
	if err != nil || d.Action != policy.ActionAskApproval {
		t.Fatalf("revoked rule: %+v %v", d, err)
	}
}

func TestDesktopApplicationIdentityMatchesDTWContract(t *testing.T) {
	for _, app := range []string{"com.example.App", "/Applications/Example.app", `C:\Program Files\Example\app.exe`, "C:/Program Files/Example/app.exe", "win:Family!App", "x11:window/123"} {
		if !ValidApplicationIdentity(app) {
			t.Fatalf("rejected %q", app)
		}
	}
	for _, app := range []string{"", " ", " com.app", "com.app\n", "relative/app.exe", "/apps/../other.app", "C:/apps/./app.exe", string(make([]byte, 4097))} {
		if ValidApplicationIdentity(app) {
			t.Fatalf("accepted %q", app)
		}
	}
}

func TestFreshAlwaysRequiresCurrentRuleBeforeDispatch(t *testing.T) {
	input := policyFixture(t, "look")
	input.Tool.Metadata[mcp.MetadataPluginID] = "configured"
	var rules []Rule
	mode := Mode{Base: presets.WorkspaceWriteMode(), Authority: "owner", Rules: func(context.Context) ([]Rule, func(), error) { return rules, func() {}, nil }}
	d, err := mode.DecideTool(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Validate(t.Context(), "allow_always"); err == nil {
		t.Fatal("uncommitted Always reached dispatch")
	}
	scope := d.Authorization.Scope
	scope.SessionID, scope.SessionIncarnation = "", ""
	rules = []Rule{{ID: "original-rule", DecisionID: "original-decision", Scope: scope}}
	release, err := d.Validate(t.Context(), "allow_always")
	if err != nil {
		t.Fatal(err)
	}
	release()
	rules[0].Revoked = true
	if _, err := d.Validate(t.Context(), "allow_always"); err == nil {
		t.Fatal("revoked fresh Always reached dispatch")
	}
	if release, err := d.Validate(t.Context(), "allow_once"); err != nil {
		t.Fatal(err)
	} else if release != nil {
		release()
	}
}
