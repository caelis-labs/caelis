package gatewayapp

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

func openControlStoreDatabase(storeDir string) (*sql.DB, string, error) {
	path := controlStoreDatabasePath(storeDir)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", fmt.Errorf("gatewayapp: create Control store directory: %w", err)
	}
	if err := requireSecureControlStoreDirectory(dir); err != nil {
		return nil, "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, "", fmt.Errorf("gatewayapp: secure Control store directory: %w", err)
	}
	if err := rejectUnsafeControlStoreFile(path); err != nil {
		return nil, "", err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, "", fmt.Errorf("gatewayapp: open Control store database: %w", err)
	}
	db.SetMaxOpenConns(1)
	closeWith := func(err error) (*sql.DB, string, error) {
		return nil, "", errors.Join(err, db.Close())
	}
	for _, statement := range []string{
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA synchronous = FULL`,
		`PRAGMA foreign_keys = ON`,
	} {
		if _, err := db.Exec(statement); err != nil {
			return closeWith(fmt.Errorf("gatewayapp: configure Control store database: %w", err))
		}
	}
	if err := rejectUnsafeControlStoreFile(path); err != nil {
		return closeWith(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return closeWith(fmt.Errorf("gatewayapp: secure Control store database: %w", err))
	}
	return db, path, nil
}

func requireSecureControlStoreDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("gatewayapp: inspect Control store directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("gatewayapp: Control store directory is not a secure directory")
	}
	return nil
}

func rejectUnsafeControlStoreFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("gatewayapp: inspect Control store file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("gatewayapp: Control store file is not a secure regular file")
	}
	return nil
}
