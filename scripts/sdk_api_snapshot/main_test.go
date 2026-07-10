package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommittedSnapshotMatchesSupportedPackages(t *testing.T) {
	packages, err := readAllowlist(filepath.Join("..", "..", defaultAllowlist))
	if err != nil {
		t.Fatalf("readAllowlist() error = %v", err)
	}
	currentDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(currentDir, "..", ".."))
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(currentDir) })
	current, err := buildSnapshot(packages)
	if err != nil {
		t.Fatalf("buildSnapshot() error = %v", err)
	}
	want, err := os.ReadFile(defaultSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, want) {
		t.Fatal("supported API snapshot is stale")
	}
}

func TestReadAllowlistRejectsDuplicatesAndUnsortedEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "duplicate",
			text: "github.com/caelis-labs/caelis/agent-sdk\ngithub.com/caelis-labs/caelis/agent-sdk\n",
			want: "duplicate",
		},
		{
			name: "unsorted",
			text: "github.com/caelis-labs/caelis/agent-sdk/tool\ngithub.com/caelis-labs/caelis/agent-sdk/model\n",
			want: "sorted",
		},
		{
			name: "outside SDK",
			text: "github.com/caelis-labs/caelis/protocol/acp\n",
			want: "unsupported import path",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "supported.txt")
			if err := os.WriteFile(path, []byte(tc.text), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := readAllowlist(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("readAllowlist() error = %v, want %q", err, tc.want)
			}
		})
	}
}
