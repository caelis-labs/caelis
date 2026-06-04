package control

import "testing"

func TestSandboxSetupStatusCheckReturnsIsolatedCopy(t *testing.T) {
	status := SandboxSetupStatus{Checks: []SandboxSetupCheck{{
		Name:    "workspace",
		Scope:   "workspace",
		Reason:  " stale marker ",
		Details: map[string]string{" path ": " /tmp/ws "},
		Counts:  map[string]int{"roots": 2},
	}}}

	check, ok := status.Check("workspace")
	if !ok {
		t.Fatal("Check(workspace) = false, want true")
	}
	if check.Reason != "stale marker" || check.Details["path"] != "/tmp/ws" {
		t.Fatalf("check = %#v, want trimmed copy", check)
	}
	check.Details["path"] = "mutated"
	check.Counts["roots"] = 9
	if got := status.Checks[0].Details[" path "]; got != " /tmp/ws " {
		t.Fatalf("source details = %q, want unchanged", got)
	}
	if got := status.Checks[0].Counts["roots"]; got != 2 {
		t.Fatalf("source counts = %d, want unchanged", got)
	}
}
