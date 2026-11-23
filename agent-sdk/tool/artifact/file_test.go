package artifact

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestFileStoreWritesRawContent(t *testing.T) {
	t.Parallel()

	store := testFileStore(t, 128, 1024*1024)
	jsonRaw := json.RawMessage(`{"result":"exact","count":2}`)
	jsonPath, err := store.Save(t.Context(), tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: jsonRaw}}})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(jsonPath) != ".json" {
		t.Fatalf("JSON artifact path = %q, want .json", jsonPath)
	}
	assertFileContent(t, jsonPath, jsonRaw)
	assertPrivateFileMode(t, jsonPath)
	if info, err := os.Stat(store.dir); err != nil {
		t.Fatalf("Stat(artifact dir) error = %v", err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		got := info.Mode().Perm()
		t.Fatalf("artifact dir mode = %o, want 700", got)
	}

	text := "first line\nsecond line"
	textPath, err := store.Save(t.Context(), tool.Result{Content: []tool.ContentBlock{tool.Text(text)}})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(textPath) != ".txt" {
		t.Fatalf("text artifact path = %q, want .txt", textPath)
	}
	assertFileContent(t, textPath, []byte(text))

	mixed := []tool.ContentBlock{
		tool.Text("alpha"),
		tool.Resource{URI: "https://example.com/a.png", MIMEType: "image/png", Name: "a"},
		tool.StructuredContent{Value: json.RawMessage(`{"beta":true}`)},
	}
	mixedPath, err := store.Save(t.Context(), tool.Result{Content: mixed})
	if err != nil || filepath.Ext(mixedPath) != ".json" {
		t.Fatalf("write(mixed) = %q/%v, want JSON artifact", mixedPath, err)
	}
	assertFileContent(t, mixedPath, []byte(`{"content":[{"type":"text","text":"alpha"},{"type":"resource","uri":"https://example.com/a.png","name":"a","mime_type":"image/png"},{"type":"structured","value":{"beta":true}}]}`))
}

func TestFileStoreCleansOldestFiles(t *testing.T) {
	t.Parallel()

	store := testFileStore(t, 2, 1024*1024)
	first, err := store.Save(t.Context(), textToolResult("first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Save(t.Context(), textToolResult("second"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(first, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("Chtimes(first) error = %v", err)
	}
	if err := os.Chtimes(second, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatalf("Chtimes(second) error = %v", err)
	}
	third, err := store.Save(t.Context(), textToolResult("third"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("oldest artifact stat error = %v, want not exist", err)
	}
	for _, path := range []string{second, third} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("retained artifact %q stat error = %v", path, err)
		}
	}
}

func TestFileStoreCleansExpiredAndOverCapacityFiles(t *testing.T) {
	t.Parallel()

	store := testFileStore(t, 8, 10)
	store.maxAge = time.Hour
	if err := os.MkdirAll(store.dir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	now := time.Now()
	store.now = func() time.Time { return now }
	expired := filepath.Join(store.dir, "001122334455.txt")
	recent := filepath.Join(store.dir, "112233445566.txt")
	for path, content := range map[string]string{expired: "old", recent: "12345678"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}
	if err := os.Chtimes(expired, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("Chtimes(expired) error = %v", err)
	}
	if err := os.Chtimes(recent, now.Add(-time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatalf("Chtimes(recent) error = %v", err)
	}
	created, err := store.Save(t.Context(), textToolResult("1234"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{expired, recent} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("evicted artifact %q stat error = %v, want not exist", path, err)
		}
	}
	assertFileContent(t, created, []byte("1234"))
}

func TestFileStoreSkipsOversizedAndUnwritableResults(t *testing.T) {
	t.Parallel()

	store := testFileStore(t, 2, 1024)
	store.fileMax = 4
	if path, err := store.Save(t.Context(), textToolResult("too large")); err == nil || path != "" {
		t.Fatalf("write(oversized) = %q/%v, want skipped", path, err)
	}

	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocked"), 0o600); err != nil {
		t.Fatalf("WriteFile(blocked) error = %v", err)
	}
	store = &FileStore{dir: blocked, maxAge: time.Hour, maxFiles: 2, maxBytes: 1024, now: time.Now}
	if path, err := store.Save(t.Context(), textToolResult("content")); err == nil || path != "" {
		t.Fatalf("write(unwritable) = %q/%v, want skipped", path, err)
	}
}

func TestFileStoreRejectsUntrustedTempParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory permissions")
	}
	for _, scenario := range []string{"writable", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			parent := filepath.Join(os.TempDir(), "caelis")
			if scenario == "symlink" {
				if err := os.Symlink(t.TempDir(), parent); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(parent, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(parent, 0o777); err != nil {
					t.Fatal(err)
				}
			}
			dir := filepath.Join(parent, "tool-results")
			ref, err := NewFileStore(dir).Save(t.Context(), textToolResult("content"))
			if err == nil || ref != "" {
				t.Fatalf("Save = %q, %v; want an error and no reference", ref, err)
			}
			if _, err := os.Lstat(dir); !os.IsNotExist(err) {
				t.Fatalf("unexpected artifact directory: %v", err)
			}
			if scenario == "writable" {
				info, err := os.Stat(parent)
				if err != nil || info.Mode().Perm() != 0o777 {
					t.Fatalf("parent permissions changed: %v, %v", info, err)
				}
			}
		})
	}
}

func TestFileStoreUsesOwnedBranchUnderSharedTemp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory permissions")
	}
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	if err := os.Chmod(temp, os.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(temp, "caelis")
	store := NewFileStore(filepath.Join(parent, "tool-results"))
	ref, err := store.Save(t.Context(), textToolResult("first"))
	if err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, ref, []byte("first"))
	t.Run("foreign owner with read-only access for peers", func(t *testing.T) {
		if os.Geteuid() != 0 {
			t.Skip("changing the fixture owner requires root")
		}
		if err := os.Chmod(parent, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(parent, 65534, -1); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chown(parent, os.Geteuid(), -1); err != nil {
				t.Error(err)
			}
		})
		if ref, err := store.Save(t.Context(), textToolResult("second")); err == nil || ref != "" {
			t.Fatalf("Save with foreign parent = %q, %v; want an error and no reference", ref, err)
		}
		assertFileContent(t, ref, []byte("first"))
	})
}

func TestFileStoreConcurrentWritesUseUniqueNames(t *testing.T) {
	t.Parallel()

	store := testFileStore(t, 64, 1024*1024)
	const count = 24
	paths := make(chan string, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if path, err := store.Save(t.Context(), textToolResult("concurrent")); err == nil {
				paths <- path
			}
		}()
	}
	wg.Wait()
	close(paths)
	seen := map[string]struct{}{}
	for path := range paths {
		seen[path] = struct{}{}
	}
	if len(seen) != count {
		t.Fatalf("unique paths = %d, want %d", len(seen), count)
	}
}

func testFileStore(t *testing.T, maxFiles int, maxBytes int64) *FileStore {
	t.Helper()
	return &FileStore{
		dir:      filepath.Join(t.TempDir(), "tool-results"),
		maxAge:   24 * time.Hour,
		maxFiles: maxFiles,
		maxBytes: maxBytes,
		fileMax:  maxBytes,
		now:      time.Now,
	}
}

func textToolResult(text string) tool.Result {
	return tool.Result{Content: []tool.ContentBlock{tool.Text(text)}}
}

func assertFileContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file %q content differs: got %d bytes, want %d", path, len(got), len(want))
	}
}

func assertPrivateFileMode(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %o, want 600", got)
	}
}
