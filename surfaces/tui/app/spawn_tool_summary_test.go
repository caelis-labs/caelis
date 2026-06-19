package tuiapp

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSubagentTerminalSignalLinesToolLifecycleCleanup(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	readPath := filepath.Join(root, "internal", "a.py")

	tests := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "read completed duplicate collapses",
			text: strings.Join([]string{
				"READ " + readPath,
				"READ " + readPath + " completed",
			}, "\n"),
			want: []string{"Read internal/a.py"},
		},
		{
			name: "standalone completed filtered",
			text: strings.Join([]string{
				"completed",
				"progress: scanning",
			}, "\n"),
			want: []string{"progress: scanning"},
		},
		{
			name: "failed duplicate upgrades existing line",
			text: strings.Join([]string{
				"READ " + readPath,
				"READ " + readPath + " failed",
			}, "\n"),
			want: []string{"Read internal/a.py failed"},
		},
		{
			name: "write completed duplicate collapses",
			text: strings.Join([]string{
				"WRITE " + filepath.Join(root, "spawn_demo_output.txt"),
				"WRITE " + filepath.Join(root, "spawn_demo_output.txt") + " completed",
			}, "\n"),
			want: []string{"Write spawn_demo_output.txt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := subagentTerminalSignalLines(tt.text, false)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("subagentTerminalSignalLines() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
