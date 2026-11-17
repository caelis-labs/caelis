package configstore

import (
	"context"
	"os"
	"path/filepath"
)

// ReadForAdmission holds the existing configuration transaction boundary until
// release. Callers may consume a permission under it, then release before I/O.
// CompareAndSave, including another Store instance, cannot revoke that snapshot
// between validation and consumption. This is not an execution lease.
func (s *Store) ReadForAdmission(ctx context.Context) (AppConfig, func(), error) {
	if _, err := s.LoadContext(ctx); err != nil {
		return AppConfig{}, nil, err
	}
	if err := s.gate.LockContext(ctx); err != nil {
		return AppConfig{}, nil, err
	}
	fail := func(err error) (AppConfig, func(), error) { s.gate.Unlock(); return AppConfig{}, nil, err }
	path, err := s.snapshotPath(ctx)
	if err != nil {
		return fail(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fail(err)
	}
	lock, err := acquireFileLock(ctx, path+".lock")
	if err != nil {
		return fail(err)
	}
	release := func() { _ = lock.Close(); s.gate.Unlock() }
	data, err := readAppConfigSnapshot(ctx, path)
	if os.IsNotExist(err) {
		return AppConfig{SchemaVersion: SchemaVersionV2}, release, nil
	}
	if err != nil {
		release()
		return AppConfig{}, nil, err
	}
	doc, err := decodeCurrentAppConfig(data)
	if err != nil {
		release()
		return AppConfig{}, nil, err
	}
	return doc, release, nil
}
