package application

import (
	"encoding/json"
	"testing"
)

func TestApplicationProfileJSONRejectsUnknownAndNullNativeAuthority(t *testing.T) {
	for _, raw := range []string{
		`{"native_tools":null}`,
		`{"unexpected":"ignored"}`,
		`{"workspace":{"cwd":"/tmp","unexpected":true}}`,
		`{"permissions":{"mode":"workspace-write","unexpected":true}}`,
	} {
		var profile Profile
		if err := json.Unmarshal([]byte(raw), &profile); err == nil {
			t.Fatalf("accepted unsupported profile fields: %s", raw)
		}
	}
}

func TestApplicationWorkspaceAndPermissionsProfileValidation(t *testing.T) {
	profile := Profile{Version: "v1", Model: "configured", ToolsVersion: "v1", Execution: "workspace-write", Workspace: Workspace{CWD: "/tmp/application-notebook", Access: []WorkspaceAccess{{Path: "/tmp/extras", Mode: "read-only"}, {Path: "/tmp/output", Mode: "read-write"}}}, Permissions: Permissions{Mode: "workspace-write", ApprovalMode: "manual"}, NativeTools: []string{"Read", "Write", "RunCommand"}}
	if err := ValidateProfile(profile); err != nil {
		t.Fatalf("explicit native profile rejected: %v", err)
	}
	for _, mutate := range []struct {
		name string
		run  func(*Profile)
	}{
		{"relative cwd", func(p *Profile) { p.Workspace.CWD = "relative" }},
		{"relative access", func(p *Profile) { p.Workspace.Access[0].Path = "relative" }},
		{"unknown access mode", func(p *Profile) { p.Workspace.Access[0].Mode = "all" }},
		{"unknown native tool", func(p *Profile) { p.NativeTools = []string{"CustomExecutor"} }},
		{"duplicate native tool", func(p *Profile) { p.NativeTools = []string{"Write", "Write"} }},
		{"unsupported read only policy", func(p *Profile) { p.Permissions.Mode = "read-only" }},
		{"unsupported approval mode", func(p *Profile) { p.Permissions.ApprovalMode = "auto-review" }},
		{"native tools without execution", func(p *Profile) { p.Execution = "tools-only" }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			candidate := profile
			candidate.Workspace.Access = append([]WorkspaceAccess(nil), profile.Workspace.Access...)
			mutate.run(&candidate)
			if err := ValidateProfile(candidate); err == nil {
				t.Fatal("unsafe/unsupported profile accepted")
			}
		})
	}
	profile.Permissions.Mode = "danger-full-access"
	if err := ValidateProfile(profile); err != nil {
		t.Fatalf("explicit exceptional mode rejected: %v", err)
	}
}
