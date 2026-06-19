package system

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const (
	embeddedRoot = "embedded"
	rootRelPath  = ".caelis/skills/.system"
	userRelPath  = ".caelis/skills"
)

//go:embed embedded
var embeddedSkills embed.FS

var ensureState = struct {
	sync.Mutex
	inFlight map[string]*ensureCall
}{
	inFlight: map[string]*ensureCall{},
}

type ensureCall struct {
	done chan struct{}
	err  error
}

func Root() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("skill/system: home directory is unavailable")
	}
	return filepath.Join(home, filepath.FromSlash(rootRelPath)), nil
}

func UserRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("skill/system: home directory is unavailable")
	}
	return filepath.Join(home, filepath.FromSlash(userRelPath)), nil
}

func Ensure() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	ensureState.Lock()
	if call, ok := ensureState.inFlight[root]; ok {
		ensureState.Unlock()
		<-call.done
		return root, call.err
	}
	call := &ensureCall{done: make(chan struct{})}
	ensureState.inFlight[root] = call
	ensureState.Unlock()

	call.err = syncEmbeddedDir(embeddedRoot, root, root)

	ensureState.Lock()
	delete(ensureState.inFlight, root)
	close(call.done)
	ensureState.Unlock()
	return root, call.err
}

func syncEmbeddedDir(src string, dst string, safeRoot string) error {
	if err := ensureDir(dst, safeRoot); err != nil {
		return err
	}
	entries, err := embeddedSkills.ReadDir(src)
	if err != nil {
		return err
	}
	keep := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name == "." || name == ".." || strings.TrimSpace(name) == "" {
			continue
		}
		keep[name] = struct{}{}
		srcPath := path.Join(src, name)
		dstPath := filepath.Join(dst, name)
		if entry.IsDir() {
			if err := syncEmbeddedDir(srcPath, dstPath, safeRoot); err != nil {
				return err
			}
			continue
		}
		if err := writeEmbeddedFile(srcPath, dstPath, safeRoot); err != nil {
			return err
		}
	}
	return removeStaleEntries(dst, safeRoot, keep)
}

func ensureDir(dst string, safeRoot string) error {
	dst, err := validateManagedPath(dst, safeRoot)
	if err != nil {
		return err
	}
	info, err := os.Lstat(dst)
	if err == nil {
		if linked, err := isLinkOrReparsePoint(dst, info); err != nil {
			return err
		} else if linked {
			return fmt.Errorf("skill/system: refusing to manage linked path inside system root: %s", dst)
		}
		if info.IsDir() {
			return nil
		}
		if err := os.Remove(dst); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(dst, 0o755)
}

func writeEmbeddedFile(src string, dst string, safeRoot string) error {
	dst, err := validateManagedPath(dst, safeRoot)
	if err != nil {
		return err
	}
	data, err := embeddedSkills.ReadFile(src)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(dst); err == nil && info.IsDir() {
		if linked, err := isLinkOrReparsePoint(dst, info); err != nil {
			return err
		} else if linked {
			return fmt.Errorf("skill/system: refusing to manage linked path inside system root: %s", dst)
		}
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	mode := fileModeFor(dst)
	if existing, err := os.ReadFile(dst); err == nil && bytes.Equal(existing, data) {
		// If a previous run already materialized the file, a sandbox/read-only
		// home should not make startup fail solely because mode repair failed.
		_ = os.Chmod(dst, mode)
		return nil
	}
	return writeFileAtomically(dst, data, mode)
}

func writeFileAtomically(dst string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		if runtime.GOOS != "windows" {
			return err
		}
		if removeErr := os.Remove(dst); removeErr != nil && !os.IsNotExist(removeErr) {
			return err
		}
		if renameErr := os.Rename(tmpName, dst); renameErr != nil {
			return renameErr
		}
	}
	cleanup = false
	return nil
}

func removeStaleEntries(dst string, safeRoot string, keep map[string]struct{}) error {
	entries, err := os.ReadDir(dst)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if _, ok := keep[entry.Name()]; ok {
			continue
		}
		target := filepath.Join(dst, entry.Name())
		if _, err := validateManagedPath(target, safeRoot); err != nil {
			return err
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	}
	return nil
}

func validateManagedPath(path string, root string) (string, error) {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if !withinRoot(path, root) {
		return "", fmt.Errorf("skill/system: refusing to manage path outside system root: %s", path)
	}
	if err := rejectLinkedManagedPath(path, root); err != nil {
		return "", err
	}
	return path, nil
}

func rejectLinkedManagedPath(target string, root string) error {
	current := filepath.Clean(root)
	target = filepath.Clean(target)
	for {
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		linked, err := isLinkOrReparsePoint(current, info)
		if err != nil {
			return err
		}
		if linked {
			return fmt.Errorf("skill/system: refusing to manage linked path inside system root: %s", current)
		}
		if current == target {
			return nil
		}
		rel, err := filepath.Rel(current, target)
		if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			return nil
		}
		nextPart := rel
		if idx := strings.Index(nextPart, string(filepath.Separator)); idx >= 0 {
			nextPart = nextPart[:idx]
		}
		if strings.TrimSpace(nextPart) == "" {
			return nil
		}
		current = filepath.Join(current, nextPart)
	}
}

func withinRoot(path string, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func fileModeFor(path string) os.FileMode {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".py", ".sh", ".bash":
		return 0o755
	default:
		return 0o644
	}
}
