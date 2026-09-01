package credentialstore

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStoreRoundTripUsesOpaqueReferenceAndOwnerOnlyFile(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := BuildReference("principal:a")
	secret := "issuer-super-secret"
	if ref == "" || strings.Contains(ref, secret) || strings.Contains(ref, "principal") || strings.Contains(ref, "grant") {
		t.Fatalf("BuildReference() = %q", ref)
	}
	if err := store.Put(context.Background(), ref, secret); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), ref)
	if err != nil || got != secret {
		t.Fatalf("Get() = %q, %v", got, err)
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || strings.Contains(entries[0].Name(), ref) || strings.Contains(entries[0].Name(), secret) {
		t.Fatalf("credential entries = %#v", entries)
	}
	info, err := os.Stat(filepath.Join(store.root, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %o", info.Mode().Perm())
	}
}

func TestStoreRejectsMalformedReferenceAndSymlink(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), "principal:a", "secret"); err == nil {
		t.Fatal("Put() accepted a non-opaque reference")
	}
	if runtime.GOOS == "windows" {
		return
	}
	ref := BuildReference("principal:a")
	if err := secureDirectory(store.root); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, store.path(ref)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), ref); err == nil {
		t.Fatal("Get() followed a credential symlink")
	}
}

func TestStoreRejectsInsecureOrSymlinkedCredentialDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("owner-only Unix directory modes are not portable to Windows")
	}
	ref := BuildReference("principal:a")

	insecure, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := insecure.Put(context.Background(), ref, "secret"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(insecure.root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := insecure.Get(context.Background(), ref); err == nil {
		t.Fatal("Get() accepted a credential directory readable by other users")
	}

	symlinked, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(symlinked.root), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "credential-target")
	if err := os.Mkdir(target, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, symlinked.root); err != nil {
		t.Fatal(err)
	}
	if err := symlinked.Put(context.Background(), ref, "secret"); err == nil {
		t.Fatal("Put() accepted a symlinked credential directory")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Fatalf("Put() changed symlink target permissions to %o", info.Mode().Perm())
	}
}
