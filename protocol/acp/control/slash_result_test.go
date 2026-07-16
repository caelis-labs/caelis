package control

import "testing"

func TestFormatSlashTableKeepsSectionsAndAlignedColumns(t *testing.T) {
	t.Parallel()

	result := NewTableSlashResult(" SubAgent ", SlashTableSnapshot{
		Title: "Subagents",
		Sections: []SlashTableSection{
			{Title: "Profiles", Columns: []string{"Profile", "Binding"}, Rows: [][]string{{"breeze", "Unbound"}, {"orbit", "openai/gpt"}}},
			{Title: "System Agents", Columns: []string{"Agent", "Binding"}, Rows: [][]string{{"guardian", "Main Agent default"}}},
		},
	})
	if result.Command != "subagent" || result.Kind != SlashCommandResultTable {
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
