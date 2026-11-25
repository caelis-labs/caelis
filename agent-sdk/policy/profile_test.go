package policy

import "testing"

func TestNormalizeProfileName(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"":                     "",
		"manual":               "",
		"auto-review":          "",
		"auto_review":          "",
		"default":              ProfileWorkspaceWrite,
		"plan":                 ProfileWorkspaceWrite,
		"full_access":          ProfileWorkspaceWrite,
		"workspace_write":      ProfileWorkspaceWrite,
		"workspace-write":      ProfileWorkspaceWrite,
		"locked-down":          "locked-down",
		"TeamStrict":           "TeamStrict",
		" danger-full-access ": ProfileDangerFullAccess,
	}
	for input, want := range tests {
		if got := NormalizeProfileName(input); got != want {
			t.Fatalf("NormalizeProfileName(%q) = %q, want %q", input, got, want)
		}
		if want == "" {
			want = ProfileWorkspaceWrite
		}
		if got := NormalizeModeName(input); got != want {
			t.Fatalf("NormalizeModeName(%q) = %q, want %q", input, got, want)
		}
	}
}
