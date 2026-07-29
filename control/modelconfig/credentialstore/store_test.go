package credentialstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStoreRoundTripUsesOpaqueReferenceAndSecureFile(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := BuildReference("openai", "openai@default")
	secret := "sk-super-secret"
	if ref == "" || strings.Contains(ref, secret) || strings.Contains(ref, "openai") {
		t.Fatalf("BuildReference() = %q", ref)
	}
	if err := store.Put(context.Background(), ref, secret); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if got != secret {
		t.Fatalf("Get() = %q", got)
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || strings.Contains(entries[0].Name(), secret) || strings.Contains(entries[0].Name(), ref) {
		t.Fatalf("credential files = %#v", entries)
	}
	info, err := os.Stat(filepath.Join(store.root, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %o", info.Mode().Perm())
	}
}

func TestStoreMarksLegacyEnvironmentCredentialInvalidAndReplaceable(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ref := BuildReference("deepseek", "deepseek@default")
	if err := ensureDir(store.root); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"version":1,"ref":"` + ref + `","environment":"DEEPSEEK_API_KEY"}`)
	if err := os.WriteFile(store.path(ref), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), ref); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("Get(legacy environment credential) error = %v, want replaceable invalid credential", err)
	}
	if err := store.Put(context.Background(), ref, "replacement-secret"); err != nil {
		t.Fatalf("Put(replacement credential) error = %v", err)
	}
	if got, err := store.Get(context.Background(), ref); err != nil || got != "replacement-secret" {
		t.Fatalf("Get(replacement credential) = %q, %v", got, err)
	}
}

func TestStoreRejectsSymlinkCredential(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ref := BuildReference("anthropic", "anthropic@default")
	if err := ensureDir(store.root); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte(`{"version":1,"ref":"`+ref+`","api_key":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, store.path(ref)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := store.Get(context.Background(), ref); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("Get(symlink) error = %v", err)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := BuildReference("google", "google@default")
	if err := store.Put(context.Background(), ref, "secret"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), ref); !os.IsNotExist(err) {
		t.Fatalf("Get(deleted) error = %v", err)
	}
}
