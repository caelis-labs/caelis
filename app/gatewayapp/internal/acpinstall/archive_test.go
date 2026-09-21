package acpinstall

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/agents"
)

func TestArchiveStreamCannotWriteBeyondDeclaredSize(t *testing.T) {
	const command = "agy_acp_server.par"
	body := []byte(strings.Repeat("x", 1<<20))
	for _, test := range []struct {
		name string
		size uint64
	}{
		{"empty declaration", 0},
		{"small declaration", 100},
		{"multiple buffers", 64 << 10},
		{"exact declaration", uint64(len(body))},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := archiveWithDeclaredSize(t, command, body, test.size)
			reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil || len(reader.File) != 1 {
				t.Fatalf("read fixture: %v", err)
			}
			file := reader.File[0]
			if file.UncompressedSize64 != test.size {
				t.Fatalf("declared size = %d, want %d", file.UncompressedSize64, test.size)
			}
			target := filepath.Join(t.TempDir(), command)
			err = extractFile(t.Context(), file, target, 0o700)
			valid := test.size == uint64(len(body))
			if valid && err != nil || !valid && !errors.Is(err, zip.ErrFormat) {
				t.Fatalf("extract valid=%v: %v", valid, err)
			}
			info, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if uint64(info.Size()) > test.size {
				t.Fatalf("wrote %d bytes beyond declared size %d", info.Size(), test.size)
			}
			t.Logf("declared=%d actual=%d written=%d", test.size, len(body), info.Size())

			parent := t.TempDir()
			dest := filepath.Join(parent, "runtime")
			hash := sha256.Sum256(data)
			installed, err := installArchive(t.Context(), agents.RuntimeInstallation{
				Directory: dest, ArchiveURL: "https://dl.google.com/runtime.zip", SHA256: hex.EncodeToString(hash[:]),
			}, command, archiveClient(data))
			if installed != valid || valid && err != nil || !valid && !errors.Is(err, zip.ErrFormat) {
				t.Fatalf("install valid=%v: installed=%v, %v", valid, installed, err)
			}
			if valid {
				got, err := os.ReadFile(filepath.Join(dest, command))
				if err != nil || !bytes.Equal(got, body) {
					t.Fatalf("valid archive contents changed: %v", err)
				}
			} else {
				entries, err := os.ReadDir(parent)
				if err != nil || len(entries) != 0 {
					t.Fatalf("invalid archive published or left staging files: %v, %v", entries, err)
				}
			}
		})
	}
}

// CreateRaw preserves a false declaration in both ZIP headers while the
// compressed data and its checksum still describe the complete payload.
func archiveWithDeclaredSize(t *testing.T, name string, body []byte, size uint64) []byte {
	t.Helper()
	var compressed, archive bytes.Buffer
	w, err := flate.NewWriter(&compressed, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(&archive)
	file, err := writer.CreateRaw(&zip.FileHeader{
		Name: name, Method: zip.Deflate, CRC32: crc32.ChecksumIEEE(body),
		CompressedSize64: uint64(compressed.Len()), UncompressedSize64: size,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(file, &compressed); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}
