package policy

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxGitPointerBytes = 8 * 1024

// ReadOnlyPaths resolves concrete read-only paths for one command. Git
// metadata discovery is intentionally bounded to each writable root and its
// direct child directories.
func ReadOnlyPaths(p Policy, workDir string) ([]string, error) {
	paths := make([]string, 0, len(p.ReadOnlySubpaths))
	protectGit := false
	for _, subpath := range p.ReadOnlySubpaths {
		subpath = strings.TrimSpace(subpath)
		if subpath == "" {
			continue
		}
		if !filepath.IsAbs(subpath) && filepath.Clean(subpath) == ".git" {
			protectGit = true
			continue
		}
		resolved := ResolveSandboxPath(workDir, subpath)
		if resolved != "" && !pathOverridden(resolved, p.WriteOverrides, workDir) {
			paths = append(paths, SandboxPathVariants(resolved)...)
		}
	}
	if protectGit {
		gitPaths, err := GitMetadataPaths(workDir, p.GitProtectionRoots, p.WriteOverrides)
		if err != nil {
			return nil, err
		}
		paths = append(paths, gitPaths...)
	}
	return normalizeStringList(paths), nil
}

// GitMetadataPaths discovers existing Git metadata at depth zero and one under
// each writable root. The root-level .git path is returned even when absent so
// backends capable of protecting future paths can deny its creation.
func GitMetadataPaths(workDir string, writableRoots, writeOverrides []string) ([]string, error) {
	paths := []string{}
	resolvedRoots := make([]string, 0, len(writableRoots))
	for _, rawRoot := range writableRoots {
		if root := ResolveSandboxPath(workDir, rawRoot); root != "" {
			resolvedRoots = append(resolvedRoots, root)
		}
	}
	for _, root := range normalizeStringList(resolvedRoots) {
		info, err := os.Stat(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("inspect writable root %s for Git metadata: %w", root, err)
		}
		if !info.IsDir() {
			continue
		}
		group, err := gitMetadataGroup(filepath.Join(root, ".git"), true)
		if err != nil {
			return nil, err
		}
		paths = appendGitMetadataGroup(paths, group, resolvedRoots, writeOverrides, workDir)

		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, fmt.Errorf("read writable root %s for Git metadata: %w", root, err)
		}
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
				continue
			}
			group, err := gitMetadataGroup(filepath.Join(root, entry.Name(), ".git"), false)
			if err != nil {
				return nil, err
			}
			paths = appendGitMetadataGroup(paths, group, resolvedRoots, writeOverrides, workDir)
		}
	}
	variants := make([]string, 0, len(paths))
	for _, path := range paths {
		variants = append(variants, SandboxPathVariants(path)...)
	}
	return normalizeStringList(variants), nil
}

func appendGitMetadataGroup(paths, group, writableRoots, writeOverrides []string, workDir string) []string {
	if len(group) == 0 {
		return paths
	}
	for _, protected := range group {
		if pathOverridden(protected, writeOverrides, workDir) {
			return paths
		}
	}
	for _, protected := range group {
		for _, root := range writableRoots {
			if pathIsUnder(protected, root) {
				paths = append(paths, protected)
				break
			}
		}
	}
	return paths
}

func pathOverridden(protected string, writeOverrides []string, workDir string) bool {
	protected = filepath.Clean(protected)
	for _, rawOverride := range writeOverrides {
		override := ResolveSandboxPath(workDir, rawOverride)
		if override != "" && pathIsUnder(override, protected) {
			return true
		}
	}
	return false
}

func gitMetadataGroup(entryPath string, includeMissing bool) ([]string, error) {
	entryPath = filepath.Clean(entryPath)
	info, err := os.Lstat(entryPath)
	if err != nil {
		if os.IsNotExist(err) {
			if includeMissing {
				return []string{entryPath}, nil
			}
			return nil, nil
		}
		return nil, fmt.Errorf("inspect Git metadata path %s: %w", entryPath, err)
	}
	paths := []string{entryPath}
	switch {
	case info.IsDir():
		common, err := gitCommonDir(entryPath)
		if err != nil {
			return nil, err
		}
		return append(paths, common...), nil
	case info.Mode()&os.ModeSymlink != 0:
		resolved, err := filepath.EvalSymlinks(entryPath)
		if err != nil {
			return nil, fmt.Errorf("resolve Git metadata symlink %s: %w", entryPath, err)
		}
		resolved = filepath.Clean(resolved)
		common, err := gitCommonDir(resolved)
		if err != nil {
			return nil, err
		}
		return append(paths, append([]string{resolved}, common...)...), nil
	case info.Mode().IsRegular():
		gitDir, err := readGitDirPointer(entryPath)
		if err != nil {
			return nil, err
		}
		common, err := gitCommonDir(gitDir)
		if err != nil {
			return nil, err
		}
		return append(paths, append([]string{gitDir}, common...)...), nil
	default:
		return paths, nil
	}
}

func readGitDirPointer(path string) (string, error) {
	line, err := readSmallLine(path)
	if err != nil {
		return "", fmt.Errorf("read Git metadata pointer %s: %w", path, err)
	}
	prefix, value, ok := strings.Cut(line, ":")
	if !ok || !strings.EqualFold(strings.TrimSpace(prefix), "gitdir") || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("git metadata pointer %s is malformed", path)
	}
	value = strings.TrimSpace(value)
	if !filepath.IsAbs(value) {
		value = filepath.Join(filepath.Dir(path), value)
	}
	return filepath.Clean(value), nil
}

func gitCommonDir(gitDir string) ([]string, error) {
	commonPath := filepath.Join(gitDir, "commondir")
	info, err := os.Stat(commonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect Git commondir pointer %s: %w", commonPath, err)
	}
	if !info.Mode().IsRegular() {
		return nil, nil
	}
	value, err := readSmallLine(commonPath)
	if err != nil {
		return nil, fmt.Errorf("read Git commondir pointer %s: %w", commonPath, err)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("git commondir pointer %s is empty", commonPath)
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(gitDir, value)
	}
	return []string{filepath.Clean(value)}, nil
}

func readSmallLine(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	reader := bufio.NewReader(io.LimitReader(file, maxGitPointerBytes))
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
