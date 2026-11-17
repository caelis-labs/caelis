package session

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestAuthorizationChoicesAreTypedAndIsolated(t *testing.T) {
	scope := AuthorizationScope{Authority: "host", Workspace: "/workspace", SessionID: "session", SessionIncarnation: "incarnation", SourceID: "source", SourceRevision: "revision", PolicyRevision: "policy-v1", Tool: "docs/read"}
	requirement := &ApprovalAuthorization{Schema: AuthorizationSchemaVersion, Scope: scope, CallDigest: "exact-call", Choices: []AuthorizationChoice{{OptionID: "selected", Persistence: AuthorizationSession}}}
	offered := &ProtocolApproval{Options: []ProtocolApprovalOption{{ID: "selected", Kind: "allow_once"}, {ID: "cancel", Kind: "reject_once"}}}
	if err := requirement.Validate(offered); err != nil {
		t.Fatal(err)
	}
	if _, found := requirement.Choice("allow_always"); found {
		t.Fatal("unoffered ID created authority")
	}
	clone := CloneApprovalAuthorization(requirement)
	clone.Choices[0].Persistence = AuthorizationPolicy
	if requirement.Choices[0].Persistence != AuthorizationSession {
		t.Fatal("choice alias")
	}
	raw, err := json.Marshal(ApprovalRecord{Authorization: requirement})
	if err != nil {
		t.Fatal(err)
	}
	var reloaded ApprovalRecord
	if err := json.Unmarshal(raw, &reloaded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.Authorization, requirement) {
		t.Fatalf("round trip: %#v", reloaded.Authorization)
	}
	resource := *clone
	resource.Scope.Tool = ""
	resource.Scope.CapabilitySet, resource.Scope.CapabilityRevision = "desktop-atomic", "14-tools-v1"
	resource.Scope.ResourceKind, resource.Scope.Resource = "application", "com.example.app"
	if err := resource.Validate(offered); err == nil {
		t.Fatal("APP scope accepted persistent policy choice")
	}
	resource.Choices = []AuthorizationChoice{{OptionID: "cancel", Persistence: AuthorizationSession}}
	if err := resource.Validate(offered); err == nil {
		t.Fatal("reject option created a scope")
	}
}

func TestAuthorizationScopeIdentityDoesNotBroaden(t *testing.T) {
	a := AuthorizationScope{Authority: "host", Workspace: "/a", SessionID: "s", SessionIncarnation: "i", SourceID: "source", SourceRevision: "v1", PolicyRevision: "p1", CapabilitySet: "atomic", CapabilityRevision: "c1", ResourceKind: "application", Resource: "com.app.A"}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*AuthorizationScope){
		func(s *AuthorizationScope) { s.Resource = "com.app.B" }, func(s *AuthorizationScope) { s.SessionID = "other" }, func(s *AuthorizationScope) { s.SessionIncarnation = "new" }, func(s *AuthorizationScope) { s.SourceRevision = "v2" }, func(s *AuthorizationScope) { s.CapabilityRevision = "c2" }, func(s *AuthorizationScope) { s.PolicyRevision = "p2" }, func(s *AuthorizationScope) { s.Workspace = "/b" }, func(s *AuthorizationScope) { s.Authority = "other" },
	} {
		b := a
		change(&b)
		if a.Key() == b.Key() {
			t.Fatal("scope identities collided")
		}
	}
}

func TestFutureAuthorizationSchemaCannotBecomeCurrentConsent(t *testing.T) {
	a := &ApprovalAuthorization{Schema: AuthorizationSchemaVersion + 1}
	if err := a.Validate(nil); err == nil {
		t.Fatal("future authorization schema accepted")
	}
	if err := ValidateDurableCoreEvent(&Event{Type: EventTypeLifecycle, AuthorizationRevocation: &AuthorizationRevocation{Schema: AuthorizationSchemaVersion + 1}}); err == nil {
		t.Fatal("future revocation schema accepted")
	}
}
