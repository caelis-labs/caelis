package gatewaydriver

import "testing"

func TestFormatWorkspaceStatusDisplayAddsBranch(t *testing.T) {
	t.Parallel()

	got := formatWorkspaceStatusDisplay(`D:\xue\code\storage`, gitWorkspaceStatus{
		Branch: "feature/windows-paths",
		Dirty:  true,
	})
	want := `D:\xue\code\storage [⎇ feature/windows-paths*]`
	if got != want {
		t.Fatalf("formatWorkspaceStatusDisplay() = %q, want %q", got, want)
	}
}

func TestParseGitWorkspaceStatusOutput(t *testing.T) {
	t.Parallel()

	status, ok := parseGitWorkspaceStatusOutput("## main...origin/main [ahead 1]\n M file.go\n")
	if !ok {
		t.Fatalf("parseGitWorkspaceStatusOutput() ok = false")
	}
	if status.Branch != "main" || !status.Dirty {
		t.Fatalf("status = %#v, want main dirty", status)
	}
}

func TestParseGitWorkspaceStatusOutputNoCommits(t *testing.T) {
	t.Parallel()

	status, ok := parseGitWorkspaceStatusOutput("## No commits yet on main\n")
	if !ok {
		t.Fatalf("parseGitWorkspaceStatusOutput() ok = false")
	}
	if status.Branch != "main" || status.Dirty {
		t.Fatalf("status = %#v, want clean main branch", status)
	}
}
