package promptview

import (
	"strings"
	"testing"

	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func TestFormatSlashTableKeepsSectionsAndAlignedColumns(t *testing.T) {
	t.Parallel()

	result := controlprompt.NewTableSlashResult(" SubAgent ", controlprompt.SlashTableSnapshot{
		Title: "Subagents",
		Sections: []controlprompt.SlashTableSection{
			{Title: "Profiles", Columns: []string{"Profile", "Binding"}, Rows: [][]string{{"breeze", "Unbound"}, {"orbit", "openai/gpt"}}},
			{Title: "System Agents", Columns: []string{"Agent", "Binding"}, Rows: [][]string{{"guardian", "Main Agent default"}}},
		},
	})
	if result.Command != "subagent" || result.Kind != controlprompt.SlashCommandResultTable {
		t.Fatalf("NewTableSlashResult() = %#v", result)
	}
	want := "Subagents\n" +
		"Profiles\n" +
		"  Profile  Binding\n" +
		"  ───────  ──────────\n" +
		"  breeze   Unbound\n" +
		"  orbit    openai/gpt\n" +
		"\n" +
		"System Agents\n" +
		"  Agent     Binding\n" +
		"  ────────  ──────────────────\n" +
		"  guardian  Main Agent default"
	if got := FormatSlashResult(result); got != want {
		t.Fatalf("FormatSlashResult(table) mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestFormatSlashResultDoctorUsesSurfaceFormatter(t *testing.T) {
	t.Parallel()

	result := controlprompt.NewDoctorSlashResult(controlstatus.StatusSnapshot{
		Session:     controlstatus.StatusSession{ID: "session-1", StoreDir: "/tmp/store"},
		ModelStatus: controlstatus.StatusModel{Provider: "openai", Name: "gpt-5.6"},
		SandboxStatus: controlstatus.StatusSandbox{
			ResolvedBackend: "seatbelt",
			Route:           "sandbox",
		},
	})
	got := FormatSlashResult(result)
	for _, want := range []string{
		"doctor:",
		"ok provider/model: openai / gpt-5.6",
		"ok session: session-1",
		"ok sandbox: seatbelt",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("FormatSlashResult(doctor) = %q, want %q", got, want)
		}
	}
}
