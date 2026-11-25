package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (spec ServerSpec) effectiveDataDirRoot() string {
	if spec.DataDirRoot != "" {
		return filepath.Clean(spec.DataDirRoot)
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(spec.DataDir)))
}

func ensureDataDir(root, dataDir, workDir string) error {
	if !filepath.IsAbs(root) || !filepath.IsAbs(dataDir) {
		return fmt.Errorf("data directory and its root must be absolute")
	}
	rel, inside := relativeWithin(root, dataDir)
	if !inside || rel == "." {
		return fmt.Errorf("data directory must be below its root")
	}
	if err := makePrivateChildDirs(root, rel); err != nil {
		return err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	realData, err := filepath.EvalSymlinks(dataDir)
	if err != nil {
		return err
	}
	if _, inside := relativeWithin(realRoot, realData); !inside {
		return fmt.Errorf("data directory escapes its root")
	}
	rel, inside = relativeWithin(dataDir, workDir)
	if !inside {
		return nil // The caller may use a separate, already provisioned working directory.
	}
	if err := makePrivateChildDirs(dataDir, rel); err != nil {
		return err
	}
	realWorkDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return err
	}
	if _, inside := relativeWithin(realData, realWorkDir); !inside {
		return fmt.Errorf("working directory escapes data directory")
	}
	return nil
}

// relative is the checked path returned by relativeWithin.
func makePrivateChildDirs(root, relative string) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	if relative == "." {
		return nil
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if err := os.Mkdir(current, 0o700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("private directory is not a real directory: %s", current)
		}
	}
	return nil
}

func relativeWithin(root, target string) (string, bool) {
	rel, err := filepath.Rel(root, target)
	return rel, err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
