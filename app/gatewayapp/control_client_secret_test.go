package gatewayapp

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	info, err := os.Stat(filepath.Join(directory, controlStoreDirectory, controlClientCursorSecretFile))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o600 {
		t.Fatalf("secret mode = %o, want 600", got)
	}
}

func TestControlClientCursorSecretMigratesAndRetiresLegacyFile(t *testing.T) {
	directory := t.TempDir()
	want := bytes.Repeat([]byte{0x5a}, 32)
	legacyPath := filepath.Join(directory, legacyControlClientCursorSecretFile)
	if err := os.WriteFile(legacyPath, want, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadOrCreateControlClientCursorSecret(directory)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("migrated secret = %x, want %x", got, want)
	}
	if _, err := os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy secret still exists: %v", err)
	}
}

func TestControlClientCursorSecretRemovesLegacyWhenCanonicalExists(t *testing.T) {
	directory := t.TempDir()
	canonical := bytes.Repeat([]byte{0x3c}, 32)
	legacy := bytes.Repeat([]byte{0x7d}, 32)
	controlDir := filepath.Join(directory, controlStoreDirectory)
	if err := os.MkdirAll(controlDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, controlClientCursorSecretFile), canonical, 0o600); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(directory, legacyControlClientCursorSecretFile)
	if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadOrCreateControlClientCursorSecret(directory)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, canonical) {
		t.Fatalf("loaded secret = %x, want canonical %x", got, canonical)
	}
	if _, err := os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired legacy secret still exists: %v", err)
	}
}

func TestControlClientCursorSecretRepairsIncompleteCanonicalFromLegacy(t *testing.T) {
	directory := t.TempDir()
	want := bytes.Repeat([]byte{0x4f}, 32)
	controlDir := filepath.Join(directory, controlStoreDirectory)
	if err := os.MkdirAll(controlDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, controlClientCursorSecretFile), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(directory, legacyControlClientCursorSecretFile)
	if err := os.WriteFile(legacyPath, want, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadOrCreateControlClientCursorSecret(directory)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("repaired secret = %x, want %x", got, want)
	}
	if _, err := os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy secret still exists: %v", err)
	}
}

func TestControlClientCursorSecretRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	controlDir := filepath.Join(directory, controlStoreDirectory)
	if err := os.MkdirAll(controlDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.key")
	want := bytes.Repeat([]byte{0x6e}, 32)
	if err := os.WriteFile(target, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(controlDir, controlClientCursorSecretFile)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := loadOrCreateControlClientCursorSecret(directory); err == nil || !strings.Contains(err.Error(), "secure regular file") {
		t.Fatalf("symlinked cursor secret error = %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("outside cursor target changed: got %x want %x", got, want)
	}
}
