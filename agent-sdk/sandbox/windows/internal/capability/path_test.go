package capability

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDedupeIsCaseInsensitiveOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path semantics")
	}
	paths := DedupePaths([]string{`C:\Work`, `c:\work\`, `C:/Other`})
	if len(paths) != 2 {
		t.Fatalf("DedupePaths() = %#v, want two paths", paths)
	}
}

func TestNormalizeWithBaseResolvesRelativePath(t *testing.T) {
	base := filepath.Join(string(filepath.Separator), "tmp", "workspace")
	got, err := NormalizePathWithBase(base, filepath.Join("sub", "..", "file.txt"))
	if err != nil {
		t.Fatalf("NormalizePathWithBase() error = %v", err)
	}
	want, err := filepath.Abs(filepath.Join(base, "file.txt"))
	if err != nil {
		t.Fatalf("Abs() error = %v", err)
	}
	if resolved := resolveExistingAncestor(want); resolved != "" {
		want = resolved
	}
	if got != want {
		t.Fatalf("NormalizePathWithBase() = %q, want %q", got, want)
	}
}

func TestIsUnderHandlesEqualAndNestedPaths(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "tmp", "workspace")
	if !IsPathUnder(root, root) {
		t.Fatal("IsPathUnder(equal) = false, want true")
	}
	if !IsPathUnder(filepath.Join(root, "src", "main.go"), root) {
		t.Fatal("IsPathUnder(nested) = false, want true")
	}
	if IsPathUnder(filepath.Join(string(filepath.Separator), "tmp", "workspace-other"), root) {
		t.Fatal("IsPathUnder(sibling) = true, want false")
	}
}

func TestCompactCoveredDropsNestedPaths(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "tmp", "cache")
	child := filepath.Join(root, "go-build")
	sibling := filepath.Join(string(filepath.Separator), "tmp", "cache-other")
	got := CompactCoveredPaths([]string{child, sibling, root, child})
	if len(got) != 2 {
		t.Fatalf("CompactCoveredPaths() = %#v, want two roots", got)
	}
	if !containsPathKey(got, root) {
		t.Fatalf("CompactCoveredPaths() = %#v, want parent root %q", got, root)
	}
	if containsPathKey(got, child) {
		t.Fatalf("CompactCoveredPaths() = %#v, want child root removed", got)
	}
	if !containsPathKey(got, sibling) {
		t.Fatalf("CompactCoveredPaths() = %#v, want sibling root %q", got, sibling)
	}
}

func TestKeyFoldsCaseOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path semantics")
	}
	key := PathKey(`C:\Work\File.txt`)
	if key != strings.ToLower(key) {
		t.Fatalf("PathKey() = %q, want lower-case comparison key", key)
	}
}

func TestNormalizeStripsWindowsLongPathPrefixes(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path semantics")
	}
	if got, want := stripWindowsExtendedPrefix(`\\?\UNC\server\share\file.txt`), `\\server\share\file.txt`; !strings.EqualFold(got, want) {
		t.Fatalf("stripWindowsExtendedPrefix(UNC) = %q, want %q", got, want)
	}
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "drive", path: `\\?\C:\Work\File.txt`, want: `C:\Work\File.txt`},
		{name: "unc", path: `\\?\UNC\server\share\file.txt`, want: `\\server\share\file.txt`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizePath(tt.path)
			if !strings.EqualFold(got, tt.want) {
				t.Fatalf("NormalizePath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeResolvesWindowsShortNamesWhenExposed(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows short-name semantics")
	}
	longPath := filepath.Join(t.TempDir(), "LongDirectoryNameForShortPathTest")
	if err := os.Mkdir(longPath, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	shortPath, ok := windowsShortPath(t, longPath)
	if !ok {
		t.Skip("filesystem did not expose an 8.3 short path")
	}
	got := NormalizePath(shortPath)
	want, err := filepath.EvalSymlinks(longPath)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	if !strings.EqualFold(got, filepath.Clean(want)) {
		t.Fatalf("NormalizePath(short path %q) = %q, want %q", shortPath, got, want)
	}
}

func TestNormalizeResolvesExistingAncestorForMissingShortPathChild(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows short-name semantics")
	}
	shortTemp := os.Getenv("TEMP")
	if !strings.Contains(shortTemp, "~") {
		t.Skip("TEMP does not expose a short-name path")
	}
	base := filepath.Join(shortTemp, "caelis-pathutil-"+strings.ReplaceAll(t.Name(), "/", "-"))
	if err := os.RemoveAll(base); err != nil {
		t.Fatalf("RemoveAll(base) error = %v", err)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("MkdirAll(base) error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	root := NormalizePath(base)
	missing := filepath.Join(base, "missing-dir", "child.txt")
	got := NormalizePath(missing)
	want := filepath.Join(root, "missing-dir", "child.txt")
	if !strings.EqualFold(got, want) {
		t.Fatalf("NormalizePath(missing short path child) = %q, want %q", got, want)
	}
	if !IsPathUnder(got, root) {
		t.Fatalf("NormalizePath(missing short path child) = %q, root %q; IsPathUnder = false", got, root)
	}
}

func TestIsUnderHonorsWindowsDriveAndUNCBoundaries(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path semantics")
	}
	if !IsPathUnder(`C:\work\project`, `C:\work`) {
		t.Fatal("IsPathUnder(drive child) = false, want true")
	}
	if IsPathUnder(`C:\workspace`, `C:\work`) {
		t.Fatal("IsPathUnder(drive sibling prefix) = true, want false")
	}
	if !IsPathUnder(`\\server\share\project`, `\\server\share`) {
		t.Fatal("IsPathUnder(UNC child) = false, want true")
	}
	if IsPathUnder(`\\server\share-other`, `\\server\share`) {
		t.Fatal("IsPathUnder(UNC sibling prefix) = true, want false")
	}
}

func windowsShortPath(t *testing.T, path string) (string, bool) {
	t.Helper()
	escaped := strings.ReplaceAll(path, `"`, `\"`)
	output, err := exec.Command("cmd.exe", "/d", "/c", `for %I in ("`+escaped+`") do @echo %~sI`).Output()
	if err != nil {
		t.Skipf("short path lookup failed: %v", err)
	}
	shortPath := strings.TrimSpace(string(output))
	if shortPath == "" || !strings.Contains(shortPath, "~") || strings.EqualFold(shortPath, path) {
		return "", false
	}
	if _, err := os.Stat(shortPath); err != nil {
		t.Skipf("short path %q is not stat-able: %v", shortPath, err)
	}
	return shortPath, true
}

func containsPathKey(paths []string, want string) bool {
	wantKey := PathKey(want)
	for _, path := range paths {
		if PathKey(path) == wantKey {
			return true
		}
	}
	return false
}
