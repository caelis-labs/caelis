package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var storeRestoreHooks struct {
	sync.RWMutex
	rename        func(string, string) error
	syncDirectory func(string) error
	syncFile      func(string) error
	writeJournal  func(storeRestoreJournal) error
}

func storeRestoreRename(oldPath, newPath string) error {
	storeRestoreHooks.RLock()
	rename := storeRestoreHooks.rename
	storeRestoreHooks.RUnlock()
	if rename != nil {
		return rename(oldPath, newPath)
	}
	return os.Rename(oldPath, newPath)
}

func storeRestoreSyncDirectory(path string) error {
	storeRestoreHooks.RLock()
	syncDirectory := storeRestoreHooks.syncDirectory
	storeRestoreHooks.RUnlock()
	if syncDirectory != nil {
		return syncDirectory(path)
	}
	return syncStoreRestoreDirectory(path)
}

func storeRestoreSyncFile(path string) error {
	storeRestoreHooks.RLock()
	syncFile := storeRestoreHooks.syncFile
	storeRestoreHooks.RUnlock()
	if syncFile != nil {
		return syncFile(path)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("store restore path %s is not a regular file", path)
	}
	return file.Sync()
}

func syncStoreRestoreDirectories(paths ...string) error {
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "." || path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		if err := storeRestoreSyncDirectory(path); err != nil {
			return fmt.Errorf("sync Store restore directory %s: %w", path, err)
		}
	}
	return nil
}

func syncStoreRestoreTree(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("store restore path %s is a symlink", path)
	}
	if info.Mode().IsRegular() {
		return storeRestoreSyncFile(path)
	}
	if !info.IsDir() {
		return fmt.Errorf("store restore path %s is not a regular file or directory", path)
	}
	var directories []string
	err = filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || (!entryInfo.Mode().IsRegular() && !entryInfo.IsDir()) {
			return fmt.Errorf("store restore path %s is not a regular file or directory", current)
		}
		if entryInfo.IsDir() {
			directories = append(directories, current)
			return nil
		}
		return storeRestoreSyncFile(current)
	})
	if err != nil {
		return err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := storeRestoreSyncDirectory(directories[index]); err != nil {
			return err
		}
	}
	return nil
}

func syncStoreRestoreInstalledTree(storeDir string, relatives []string) error {
	for _, relative := range relatives {
		if err := syncStoreRestoreTree(filepath.Join(storeDir, relative)); err != nil {
			return fmt.Errorf("sync restored %s: %w", relative, err)
		}
	}
	return storeRestoreSyncDirectory(storeDir)
}

func rejectStoreRestoreControlSidecars(storeDir string) error {
	if err := rejectStoreSQLiteSidecars(controlStoreDatabasePath(storeDir), "Control"); err != nil {
		return fmt.Errorf("gatewayapp: restore target Control SQLite sidecar requires owner recovery: %w", err)
	}
	return nil
}

func validateStagedStoreRestore(ctx context.Context, stageDir string) error {
	// Empty Session directories are valid owner state; readStoreUpgradePreflight
	// reuses Control and Session owner validators without requiring documents.
	if _, err := readStoreUpgradePreflight(ctx, stageDir); err != nil {
		return fmt.Errorf("gatewayapp: staged Store restore failed owner validation: %w", err)
	}
	return nil
}

func refuseUnsyncedStoreRestoreCommit(storeDir string, applied []string) error {
	if len(applied) == 0 {
		return errors.New("gatewayapp: refuse Memory commit of unsynced restored Store: no applied components")
	}
	if err := syncStoreRestoreInstalledTree(storeDir, applied); err != nil {
		return fmt.Errorf("gatewayapp: refuse Memory commit of unsynced restored Store: %w", err)
	}
	return nil
}
