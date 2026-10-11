package session

import (
	"encoding/json"
	"strings"
	"testing"
)

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
