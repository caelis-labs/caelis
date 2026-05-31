package resources

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	embeddedSystemSkillRoot = "systemskills/embedded"
	systemSkillRootRelPath  = ".caelis/skills/.system"
)

//go:embed systemskills/embedded
var embeddedSystemSkills embed.FS

func systemSkillRoot(homeDir string) string {
	homeDir = strings.TrimSpace(homeDir)
	if homeDir == "" {
		return ""
	}
	return filepath.Join(homeDir, filepath.FromSlash(systemSkillRootRelPath))
}

func ensureSystemSkills(homeDir string) error {
	root := systemSkillRoot(homeDir)
	if root == "" {
		return fmt.Errorf("app/resources: home directory is unavailable")
	}
	return syncEmbeddedSystemSkillDir(embeddedSystemSkillRoot, root, root)
}

func syncEmbeddedSystemSkillDir(src string, dst string, safeRoot string) error {
	if err := ensureManagedSystemSkillDir(dst, safeRoot); err != nil {
		return err
	}
	entries, err := embeddedSystemSkills.ReadDir(src)
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
			if err := syncEmbeddedSystemSkillDir(srcPath, dstPath, safeRoot); err != nil {
				return err
			}
			continue
		}
		if err := writeEmbeddedSystemSkillFile(srcPath, dstPath, safeRoot); err != nil {
			return err
		}
	}
	return removeStaleSystemSkillEntries(dst, safeRoot, keep)
}

func ensureManagedSystemSkillDir(dst string, safeRoot string) error {
	dst, err := validateManagedSystemSkillPath(dst, safeRoot)
	if err != nil {
		return err
	}
	info, err := os.Lstat(dst)
	if err == nil {
		if linked, err := isLinkOrReparsePoint(dst, info); err != nil {
			return err
		} else if linked {
			return fmt.Errorf("app/resources: refusing to manage linked path inside system skill root: %s", dst)
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

func writeEmbeddedSystemSkillFile(src string, dst string, safeRoot string) error {
	dst, err := validateManagedSystemSkillPath(dst, safeRoot)
	if err != nil {
		return err
	}
	data, err := embeddedSystemSkills.ReadFile(src)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(dst); err == nil && info.IsDir() {
		if linked, err := isLinkOrReparsePoint(dst, info); err != nil {
			return err
		} else if linked {
			return fmt.Errorf("app/resources: refusing to manage linked path inside system skill root: %s", dst)
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
	mode := systemSkillFileMode(dst)
	if existing, err := os.ReadFile(dst); err == nil && bytes.Equal(existing, data) {
		_ = os.Chmod(dst, mode)
		return nil
	}
	return os.WriteFile(dst, data, mode)
}

func removeStaleSystemSkillEntries(dst string, safeRoot string, keep map[string]struct{}) error {
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
		if _, err := validateManagedSystemSkillPath(target, safeRoot); err != nil {
			return err
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	}
	return nil
}

func validateManagedSystemSkillPath(target string, root string) (string, error) {
	target = filepath.Clean(target)
	root = filepath.Clean(root)
	if !withinRoot(target, root) {
		return "", fmt.Errorf("app/resources: refusing to manage path outside system skill root: %s", target)
	}
	if err := rejectLinkedManagedSystemSkillPath(target, root); err != nil {
		return "", err
	}
	return target, nil
}

func rejectLinkedManagedSystemSkillPath(target string, root string) error {
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
			return fmt.Errorf("app/resources: refusing to manage linked path inside system skill root: %s", current)
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

func withinRoot(target string, root string) bool {
	target = filepath.Clean(target)
	root = filepath.Clean(root)
	if target == root {
		return true
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func systemSkillFileMode(path string) os.FileMode {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".py", ".sh", ".bash":
		return 0o755
	default:
		return 0o644
	}
}

func removeSkillRoot(roots []string, skip string) []string {
	skip = filepath.Clean(strings.TrimSpace(skip))
	if skip == "" || skip == "." {
		return slicesClone(roots)
	}
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		if samePath(root, skip) {
			continue
		}
		out = append(out, root)
	}
	return out
}

func samePath(left string, right string) bool {
	left = filepath.Clean(strings.TrimSpace(left))
	right = filepath.Clean(strings.TrimSpace(right))
	return left == right || strings.EqualFold(left, right)
}

func slicesClone(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
