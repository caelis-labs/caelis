package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreRecoveryPrimitivesAreNotCLICommands(t *testing.T) {
	for _, command := range [][]string{
		{"backup"}, {"restore"}, {"upgrade", "prepare"},
		{"upgrade", "commit"}, {"upgrade", "rollback"},
	} {
		t.Run(strings.Join(command, "-"), func(t *testing.T) {
			storeDir := filepath.Join(t.TempDir(), "unopened-store")
			args := append(append([]string(nil), command...), "--store-dir", storeDir)
			err := run(context.Background(), args, nil, io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "unknown arguments") {
				t.Fatalf("run(%v) = %v, want unknown command", args, err)
			}
			if _, err := os.Stat(storeDir); !os.IsNotExist(err) {
				t.Fatalf("unsupported command touched Store: %v", err)
			}
		})
	}
}

func TestCLIHelpDoesNotAdvertiseStoreArchiveFlags(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--help"}, nil, io.Discard, &output); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"-input", "-output"} {
		if strings.Contains(output.String(), flag) {
			t.Fatalf("help advertises removed archive flag %s:\n%s", flag, output.String())
		}
	}
}
