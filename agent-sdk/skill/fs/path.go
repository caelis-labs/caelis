package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RootDir resolves a skill file or directory path to the directory containing
// its SKILL.md file. It validates that both the root and SKILL.md exist.
func RootDir(path string) (string, error) {
	resolved, err := ResolvePath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	root := resolved
	if !info.IsDir() || strings.EqualFold(filepath.Base(resolved), "SKILL.md") {
		root = filepath.Dir(resolved)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !rootInfo.IsDir() {
		return "", fmt.Errorf("skill root is not a directory: %s", root)
	}
	skillPath := filepath.Join(root, "SKILL.md")
	skillInfo, err := os.Stat(skillPath)
	if err != nil {
		return "", err
	}
	if skillInfo.IsDir() {
		return "", fmt.Errorf("skill file is a directory: %s", skillPath)
	}
	return filepath.Clean(root), nil
}
