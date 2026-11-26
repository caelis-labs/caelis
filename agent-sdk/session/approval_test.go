package session

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestApprovalReviewReplayExcludesUnreviewedJournalAndModelContext(t *testing.T) {
	for _, tc := range []struct {
		name             string
		status           ApprovalRecordStatus
		review           string
		approved, replay bool
	}{
		{"approved", ApprovalRecordResolved, "approved", true, true},
		{"denied", ApprovalRecordResolved, "denied: outside scope", false, true},
		{"manual", ApprovalRecordResolved, "", true, false},
		{"pending", ApprovalRecordPending, "reviewing", false, false},
		{"cancelled", ApprovalRecordCancelled, "cancelled", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := &Event{Seq: 1, Type: EventTypeLifecycle, Visibility: VisibilityJournal,
				Approval: &ApprovalRecord{
					ToolCallID: "call-1", Status: tc.status, Approved: tc.approved, ReviewText: tc.review,
				},
			}
			if IsClientReplayEvent(event) != tc.replay {
				t.Fatalf("replay = %v, want %v", IsClientReplayEvent(event), tc.replay)
			}
			page := PageEvents([]*Event{event}, EventPageRequest{Visibility: EventPageClientReplay})
			if (len(page.Events) == 1) != tc.replay || page.NextSeq != 1 {
				t.Fatalf("client replay page = %#v", page)
			}
			if IsCanonicalHistoryEvent(event) || IsMainInvocationVisibleEvent(event) {
				t.Fatal("approval journal entered model context")
			}
		})
	}
}

func TestApprovalSubjectBoundsAndDropsMarkedValues(t *testing.T) {
	subject := &ApprovalSubject{Kind: "file", Tool: KnownApprovalField("Write"), Operation: KnownApprovalField("write"), Scope: KnownApprovalField("this invocation"),
		Effect: ApprovalField{State: ApprovalFieldKnown, Text: "SECRET-MUST-NOT-COPY", Sensitive: true}}
	for range 20 {
		subject.Targets = append(subject.Targets, KnownApprovalField(strings.Repeat("界", 600)))
	}
	for range 40 {
		subject.Grants = append(subject.Grants, ApprovalGrant{OptionID: "display-only", Scope: KnownApprovalField("once")})
	}
	copy := CloneApprovalSubject(subject)
	raw, err := json.Marshal(copy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "SECRET-MUST-NOT-COPY") || copy.Effect.State != ApprovalFieldOpaque || !copy.Effect.Sensitive {
		t.Fatalf("sensitive display = %s", raw)
	}
	if len(copy.Targets) != 16 || !copy.TargetsTruncated || len(copy.Grants) != 32 || !copy.GrantsTruncated || len([]rune(copy.Targets[0].Text)) != 512 || copy.Targets[0].State != ApprovalFieldTruncated {
		t.Fatalf("unbounded subject = %+v", copy)
	}
	copy.Targets[0].Text = "detached"
	if subject.Targets[0].Text == "detached" {
		t.Fatal("clone aliases producer fields")
	}
}

func TestApprovalSubjectJoinsOnlyOriginalOptions(t *testing.T) {
	permission := &ProtocolApproval{ToolCall: ProtocolToolCall{Name: "custom"}, Options: []ProtocolApprovalOption{{ID: "native-session", Kind: "allow_once"}, {ID: "native-reject", Kind: "reject_once"}},
		Subject: &ApprovalSubject{Kind: "app_access", Grants: []ApprovalGrant{{OptionID: "invented", Scope: KnownApprovalField("workspace")}, {OptionID: "native-session", Scope: KnownApprovalField("session")}}}}
	got := ApprovalSubjectOf(permission)
	if len(got.Grants) != 2 || got.Grants[0].OptionID != "native-session" || got.Grants[0].Scope.Text != "session" || got.Grants[1].OptionID != "native-reject" || got.Grants[1].Scope.State != ApprovalFieldUnavailable {
		t.Fatalf("display invented authority or option order: %+v", got.Grants)
	}
	permission.Subject = nil
	opaque := ApprovalSubjectOf(permission)
	if opaque.Kind != "opaque" || opaque.Operation.State != ApprovalFieldOpaque || opaque.Grants[0].Scope.State != ApprovalFieldOpaque {
		t.Fatalf("unknown input interpreted: %+v", opaque)
	}
}

func TestRemoteApprovalHintWithdrawsDerivedSecretAndOpaqueText(t *testing.T) {
	for _, sensitive := range []bool{false, true} {
		name := "opaque"
		if sensitive {
			name = "sensitive"
		}
		t.Run(name, func(t *testing.T) {
			marker := "REMOTE-DERIVED-PRIVATE-VALUE"
			field := KnownApprovalField(marker)
			hint := &ApprovalSubject{Kind: "command", Title: &field, Description: &field,
				Tool: field, Operation: field, Effect: field, Scope: field,
				Targets: []ApprovalField{field, {State: ApprovalFieldKnown, Text: marker, Sensitive: sensitive}},
				Grants:  []ApprovalGrant{{OptionID: "native-choice", Scope: field}}, OpaqueInput: !sensitive}
			permission := &ProtocolApproval{Context: &PermissionContext{DisplaySubjectHint: hint},
				Options: []ProtocolApprovalOption{{ID: "native-choice", Kind: "reject_once"}}}
			public := ApprovalDisplaySubjectOf(permission)
			raw, err := json.Marshal(public)
			if err != nil || strings.Contains(string(raw), marker) || !public.Reported || public.Grants[0].OptionID != "native-choice" || public.Title.State != ApprovalFieldOpaque {
				t.Fatalf("remote derived text escaped or changed native identity: %s, %v", raw, err)
			}
			if hint.Title.Text != marker || hint.Targets[0].Text != marker || ApprovalSubjectOf(permission).Reported {
				t.Fatal("presentation repair mutated private canonical facts")
			}
		})
	}
}

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
