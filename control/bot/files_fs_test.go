package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestFilesToolsRoundTrip(t *testing.T) {
	files := newTestFiles(t, t.TempDir())
	mustInitFiles(t, files)
	tools := privateFileToolSet(t, files)
	base := filesBase(t, files)

	written := filesPayload(t, filesCall(t, tools["Write"], map[string]any{
		"path": "notes/a.md", "content": "alpha\nbeta\n",
	}))
	if revision, _ := written["revision"].(string); !strings.HasPrefix(revision, "sha256:") {
		t.Fatalf("Write revision = %q, want sha256", written["revision"])
	}
	read := filesPayload(t, filesCall(t, tools["Read"], map[string]any{"path": "notes/a.md"}))
	if content, _ := read["content"].(string); !strings.Contains(content, "alpha") {
		t.Fatalf("Read content = %q, want written text", read["content"])
	}
	filesCall(t, tools["Patch"], map[string]any{
		"path": "notes/a.md", "edits": []any{map[string]any{"old": "beta", "new": "gamma"}},
	})
	grep := filesPayload(t, filesCall(t, tools["Grep"], map[string]any{"pattern": "gamma"}))
	if hits, _ := grep["hits"].([]any); len(hits) != 1 {
		t.Fatalf("Grep hits = %#v, want the patched line", grep["hits"])
	}
	glob := filesPayload(t, filesCall(t, tools["Glob"], map[string]any{"pattern": "**/*.md"}))
	matches, _ := glob["matches"].([]any)
	foundNotes := false
	for _, match := range matches {
		path, _ := match.(string)
		if !strings.HasPrefix(path, base) {
			t.Fatalf("Glob match %q is outside the files root %q", path, base)
		}
		if strings.HasSuffix(path, filepath.Join("notes", "a.md")) {
			foundNotes = true
		}
	}
	if !foundNotes {
		t.Fatalf("Glob matches = %#v, want the patched notes file", glob["matches"])
	}
	if got, err := os.ReadFile(filepath.Join(base, "notes", "a.md")); err != nil || !strings.Contains(string(got), "gamma") {
		t.Fatalf("on-disk content = %q/%v, want patched text", got, err)
	}
}

func TestFilesRejectsPathsOutsideRoot(t *testing.T) {
	storeDir := t.TempDir()
	files := newTestFiles(t, storeDir)
	mustInitFiles(t, files)
	tools := privateFileToolSet(t, files)
	for _, args := range []map[string]any{
		{"path": filepath.Join(t.TempDir(), "outside.md")},
		{"path": "../../outside.md"},
		{"path": filepath.Join(storeDir, "..", "escape.md")},
	} {
		_, err := filesCallErr(t, tools["Read"], args)
		if err == nil || !strings.Contains(err.Error(), "outside the files") {
			t.Fatalf("Read(%v) error = %v, want outside-files rejection", args["path"], err)
		}
	}
	if _, err := filesCallErr(t, tools["Write"], map[string]any{"path": filepath.Join(t.TempDir(), "escape.md"), "content": "x"}); err == nil {
		t.Fatal("Write() accepted a path outside the files")
	}
}

func TestFilesRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privilege on Windows")
	}
	files := newTestFiles(t, t.TempDir())
	mustInitFiles(t, files)
	base := filesBase(t, files)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(secret, []byte("secret\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(secret) error = %v", err)
	}
	if err := os.Symlink(secret, filepath.Join(base, "leak.md")); err != nil {
		t.Fatalf("Symlink(leak.md) error = %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "leakdir")); err != nil {
		t.Fatalf("Symlink(leakdir) error = %v", err)
	}
	tools := privateFileToolSet(t, files)
	for _, path := range []string{"leak.md", "leakdir"} {
		if _, err := filesCallErr(t, tools["Read"], map[string]any{"path": path}); err == nil {
			t.Fatalf("Read(%q) followed a symlink", path)
		}
		if _, err := filesCallErr(t, tools["Write"], map[string]any{"path": path, "content": "x"}); err == nil {
			t.Fatalf("Write(%q) followed a symlink", path)
		}
	}
	grep := filesPayload(t, filesCall(t, tools["Grep"], map[string]any{"pattern": "secret"}))
	if hits, _ := grep["hits"].([]any); len(hits) != 0 {
		t.Fatalf("Grep hits = %#v, want no symlinked content", grep["hits"])
	}
	glob := filesPayload(t, filesCall(t, tools["Glob"], map[string]any{"pattern": "*"}))
	for _, match := range glob["matches"].([]any) {
		if strings.Contains(match.(string), "leak") {
			t.Fatalf("Glob surfaced a symlink: %v", match)
		}
	}
}

func TestFilesRejectsSymlinkAncestor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privilege on Windows")
	}
	storeDir := t.TempDir()
	outside := t.TempDir()
	botsDir := filepath.Join(storeDir, "bots")
	if err := os.MkdirAll(botsDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(bots) error = %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(botsDir, testFilesID)); err != nil {
		t.Fatalf("Symlink(bot) error = %v", err)
	}
	files := newTestFiles(t, storeDir)
	if err := files.Init(context.Background()); err == nil {
		t.Fatal("Init() followed a symlinked files ancestor")
	}
	if entries, err := os.ReadDir(outside); err == nil && len(entries) != 0 {
		t.Fatalf("Init() wrote outside the store: %v", entries)
	}
}

func TestFilesWriteIsAtomicAndPrivate(t *testing.T) {
	files := newTestFiles(t, t.TempDir())
	mustInitFiles(t, files)
	tools := privateFileToolSet(t, files)
	base := filesBase(t, files)
	filesCall(t, tools["Write"], map[string]any{"path": "a.md", "content": "hello\n"})
	info, err := os.Lstat(filepath.Join(base, "a.md"))
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("written file mode = %v/%v, want regular 0600", info, err)
	}
	assertNoFilesTempFiles(t, base)
}

func TestFilesWriteFailureLeavesTargetUnchanged(t *testing.T) {
	files := newTestFiles(t, t.TempDir())
	mustInitFiles(t, files)
	tools := privateFileToolSet(t, files)
	base := filesBase(t, files)
	filesCall(t, tools["Write"], map[string]any{"path": "blocker", "content": "keep\n"})
	if _, err := filesCallErr(t, tools["Write"], map[string]any{"path": "blocker/child.md", "content": "x"}); err == nil {
		t.Fatal("Write() accepted a file path whose parent is a regular file")
	}
	if got, err := os.ReadFile(filepath.Join(base, "blocker")); err != nil || string(got) != "keep\n" {
		t.Fatalf("target after failed write = %q/%v, want unchanged", got, err)
	}
	assertNoFilesTempFiles(t, base)
}

func TestFilesBotsCannotReadWriteEnumerateOrSearchEachOther(t *testing.T) {
	store := t.TempDir()
	first := newTestFiles(t, store)
	mustInitFiles(t, first)
	second, err := NewFiles(store, Identity("owner", "second-bot"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	mustInitFiles(t, second)
	secondTools := privateFileToolSet(t, second)
	filesCall(t, secondTools["Write"], map[string]any{"path": "private.md", "content": "SECOND_BOT_ONLY"})
	otherRoot := filesBase(t, second)
	tools := privateFileToolSet(t, first)
	for name, args := range map[string]map[string]any{
		"Read":  {"path": filepath.Join(otherRoot, "private.md")},
		"Write": {"path": filepath.Join(otherRoot, "private.md"), "content": "overwrite"},
		"Patch": {"path": filepath.Join(otherRoot, "private.md"), "edits": []any{map[string]any{"old": "SECOND_BOT_ONLY", "new": "overwrite"}}},
		"Glob":  {"path": otherRoot, "pattern": "*"},
		"Grep":  {"path": otherRoot, "pattern": "SECOND_BOT_ONLY"},
	} {
		if result, err := filesCallErr(t, tools[name], args); err == nil {
			t.Fatalf("%s crossed Bot boundary: %+v", name, result)
		}
	}
	if result := filesPayload(t, filesCall(t, tools["Grep"], map[string]any{"pattern": "SECOND_BOT_ONLY"})); len(result["hits"].([]any)) != 0 {
		t.Fatalf("default search crossed Bot boundary: %+v", result)
	}
	content, err := os.ReadFile(filepath.Join(otherRoot, "private.md"))
	if err != nil || string(content) != "SECOND_BOT_ONLY" {
		t.Fatalf("other Bot note was modified: %q, %v", content, err)
	}
}

func TestFilesConcurrentWritesSerialize(t *testing.T) {
	files := newTestFiles(t, t.TempDir())
	mustInitFiles(t, files)
	tools := privateFileToolSet(t, files)
	base := filesBase(t, files)
	seed := filesPayload(t, filesCall(t, tools["Write"], map[string]any{"path": "shared.md", "content": "v0\n"}))
	revision, _ := seed["revision"].(string)
	if revision == "" {
		t.Fatal("seed write returned no revision")
	}

	const workers = 8
	inputs := make([]json.RawMessage, workers)
	for i := range inputs {
		raw, err := json.Marshal(map[string]any{
			"path": "shared.md", "content": fmt.Sprintf("v%d\n", i+1), "if_revision": revision,
		})
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		inputs[i] = raw
	}
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range inputs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = tools["Write"].Call(context.Background(), tool.Call{Input: inputs[i]})
		}(i)
	}
	wg.Wait()

	winners, stale := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			winners++
		case strings.Contains(err.Error(), "changed since it was read"):
			stale++
		default:
			t.Fatalf("unexpected concurrent Write() error = %v", err)
		}
	}
	if winners != 1 || winners+stale != workers {
		t.Fatalf("winners=%d stale=%d, want exactly one winner", winners, stale)
	}
	content, err := os.ReadFile(filepath.Join(base, "shared.md"))
	if err != nil {
		t.Fatalf("ReadFile(shared.md) error = %v", err)
	}
	if len(content) != 3 || content[0] != 'v' || content[2] != '\n' {
		t.Fatalf("final content = %q, want one complete winner", content)
	}
}

func assertNoFilesTempFiles(t *testing.T, base string) {
	t.Helper()
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return err
		}
		if strings.Contains(d.Name(), ".tmp-") {
			t.Fatalf("staging file left behind: %q", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir() error = %v", err)
	}
}
