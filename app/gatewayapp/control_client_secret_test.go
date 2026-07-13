package gatewayapp

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestControlClientCursorSecretPersistsWithPrivateMode(t *testing.T) {
	directory := t.TempDir()
	first, err := loadOrCreateControlClientCursorSecret(directory)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateControlClientCursorSecret(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 32 || !bytes.Equal(first, second) {
		t.Fatalf("persistent secrets differ: first=%x second=%x", first, second)
	}
	info, err := os.Stat(filepath.Join(directory, controlClientCursorSecretFile))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("secret mode = %o, want 600", got)
	}
}
