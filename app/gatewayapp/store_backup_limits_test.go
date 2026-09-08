package gatewayapp

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreBackupReadLimitRejectsTruncation(t *testing.T) {
	for _, value := range []string{"abc", "abcd", "abcde"} {
		raw, err := readStoreBackupLimited(strings.NewReader(value), 4)
		if len(value) > 4 {
			if !errors.Is(err, ErrStoreBackupTooLarge) || raw != nil {
				t.Fatalf("oversized read returned truncated success: %q, %v", raw, err)
			}
		} else if err != nil || string(raw) != value {
			t.Fatalf("read at/below limit = %q, %v", raw, err)
		}
	}
}

func TestStoreBackupWriteLimitIncludesZIPFinalization(t *testing.T) {
	var output bytes.Buffer
	limited := &storeBackupLimitWriter{writer: &output, remaining: 16}
	archive := zip.NewWriter(limited)
	if err := archive.Close(); !errors.Is(err, ErrStoreBackupTooLarge) {
		t.Fatalf("ZIP directory exceeded budget: %v", err)
	}
	if !limited.exceeded || output.Len() > 16 {
		t.Fatalf("limit not enforced: %+v, bytes=%d", limited, output.Len())
	}
	if _, err := limited.Write(nil); !errors.Is(err, ErrStoreBackupTooLarge) {
		t.Fatalf("oversized writer accepted later write: %v", err)
	}
}

func TestWriteStoreBackupRejectsOversizedFileBeforePublication(t *testing.T) {
	storeDir := makeStoreBackupFixture(t, "size-budget")
	path := filepath.Join(storeDir, "sessions", "oversized.events.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(storeBackupEntryMaxBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	_, err = WriteStoreBackup(t.Context(), StoreBackupOptions{
		StoreDir: storeDir,
		MemoryBackup: func(context.Context, io.Writer) error {
			t.Fatal("Memory capture ran after oversized component")
			return nil
		},
	}, &output)
	if !errors.Is(err, ErrStoreBackupTooLarge) || output.Len() != 0 {
		t.Fatalf("oversized backup = %v, published %d bytes", err, output.Len())
	}
}

func TestWriteStoreBackupRejectsMemoryAndArchiveOverrunBeforePublication(t *testing.T) {
	for _, kind := range []string{"memory", "archive"} {
		t.Run(kind, func(t *testing.T) {
			storeDir := makeStoreBackupFixture(t, "bounded-"+kind)
			limits := storeBackupLimits{archive: 1 << 20, entry: 1 << 20}
			if kind == "archive" {
				limits.archive = 16 // Even an empty ZIP central directory exceeds this.
			}
			var output bytes.Buffer
			memoryCalled := false
			_, err := writeStoreBackupLimited(t.Context(), StoreBackupOptions{
				StoreDir: storeDir,
				MemoryBackup: func(_ context.Context, writer io.Writer) error {
					memoryCalled = true
					payload := "memory"
					if kind == "memory" {
						payload = strings.Repeat("m", int(limits.entry)+1)
					}
					_, err := io.WriteString(writer, payload)
					if kind == "memory" {
						// Owner callbacks cannot suppress an observed overrun.
						return nil
					}
					return err
				},
			}, &output, limits)
			if !errors.Is(err, ErrStoreBackupTooLarge) || output.Len() != 0 {
				t.Fatalf("overrun = %v, published %d bytes", err, output.Len())
			}
			if kind == "memory" && !memoryCalled {
				t.Fatal("test failed before exercising Memory snapshot limit")
			}
		})
	}
}

func TestStoreBackupReadersRejectDeclaredOversizeBeforeOpening(t *testing.T) {
	entry := &zip.File{FileHeader: zip.FileHeader{UncompressedSize64: uint64(storeBackupEntryMaxBytes + 1)}}
	if _, err := readZipEntry(entry); !errors.Is(err, ErrStoreBackupTooLarge) {
		t.Fatalf("oversized entry = %v", err)
	}
	if _, err := ReadStoreBackupManifest(bytes.NewReader(nil), storeBackupArchiveMaxBytes+1); !errors.Is(err, ErrStoreBackupTooLarge) {
		t.Fatalf("oversized archive = %v", err)
	}
}
