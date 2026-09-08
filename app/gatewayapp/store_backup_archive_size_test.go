package gatewayapp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyStoreBackupLimitedRejectsOverrun(t *testing.T) {
	var dst bytes.Buffer
	n, err := copyStoreBackupLimited(&dst, bytes.NewReader([]byte("hello")), 4)
	if !errors.Is(err, ErrStoreBackupTooLarge) {
		t.Fatalf("copyStoreBackupLimited() error = %v, want %v", err, ErrStoreBackupTooLarge)
	}
	if n != 5 {
		t.Fatalf("copyStoreBackupLimited() n = %d, want actual bytes written 5", n)
	}
}

func TestCopyStoreBackupLimitedAcceptsExactLimit(t *testing.T) {
	var dst bytes.Buffer
	n, err := copyStoreBackupLimited(&dst, bytes.NewReader([]byte("abcd")), 4)
	if err != nil {
		t.Fatalf("copyStoreBackupLimited() error = %v", err)
	}
	if n != 4 || dst.String() != "abcd" {
		t.Fatalf("copyStoreBackupLimited() n=%d dst=%q", n, dst.String())
	}
}

func TestWriteStoreBackupLimitedRejectsManifestOverrunBeforePublish(t *testing.T) {
	storeDir := makeStoreBackupFixture(t, "manifest-limit")
	for index := 0; index < 300; index++ {
		name := fmt.Sprintf("%03d-%s.jsonl", index, strings.Repeat("e", 80))
		if err := os.WriteFile(filepath.Join(storeDir, "sessions", name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	memoryCalled := false
	var archive bytes.Buffer
	_, err := writeStoreBackupLimited(t.Context(), StoreBackupOptions{
		StoreDir:     storeDir,
		MemoryBackup: func(context.Context, io.Writer) error { memoryCalled = true; return nil },
	}, &archive, storeBackupLimits{archive: 1 << 20, entry: 64 << 10})
	if !errors.Is(err, ErrStoreBackupTooLarge) {
		t.Fatalf("writeStoreBackupLimited() error = %v, want %v", err, ErrStoreBackupTooLarge)
	}
	if !memoryCalled {
		t.Fatal("test failed before reaching manifest encoding")
	}
	if archive.Len() != 0 {
		t.Fatalf("oversize manifest published %d bytes", archive.Len())
	}
}

func TestMaterializeStoreBackupArchiveRejectsOverrun(t *testing.T) {
	file, size, err := materializeStoreBackupArchive(bytes.NewReader([]byte("hello")), 4)
	if file != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		t.Fatalf("materializeStoreBackupArchive() file = %s", file.Name())
	}
	if size != 0 {
		t.Fatalf("materializeStoreBackupArchive() size = %d", size)
	}
	if !errors.Is(err, ErrStoreBackupTooLarge) {
		t.Fatalf("materializeStoreBackupArchive() error = %v, want %v", err, ErrStoreBackupTooLarge)
	}
}

func TestMaterializeStoreBackupArchiveAcceptsExactLimit(t *testing.T) {
	file, size, err := materializeStoreBackupArchive(bytes.NewReader([]byte("abcd")), 4)
	if err != nil {
		t.Fatalf("materializeStoreBackupArchive() error = %v", err)
	}
	name := file.Name()
	defer func() {
		_ = file.Close()
		_ = os.Remove(name)
	}()
	if size != 4 {
		t.Fatalf("materializeStoreBackupArchive() size = %d", size)
	}
	raw, err := io.ReadAll(file)
	if err != nil || string(raw) != "abcd" {
		t.Fatalf("materialized = %q err=%v", raw, err)
	}
}
