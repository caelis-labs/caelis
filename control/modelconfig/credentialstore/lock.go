package credentialstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
)

var referenceGates = struct {
	sync.Mutex
	byPath map[string]*referenceGate
}{byPath: map[string]*referenceGate{}}

type referenceGate struct {
	token chan struct{}
	users int
}

type referenceLock struct {
	file    io.Closer
	release func()
	once    sync.Once
	err     error
}

func (s *Store) acquireReferenceLock(ctx context.Context, ref string) (*referenceLock, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	lockPath, err := filepath.Abs(s.lockPath(ref))
	if err != nil {
		return nil, err
	}
	if err := ensureDir(filepath.Dir(lockPath)); err != nil {
		return nil, err
	}
	release, err := acquireReferenceGate(ctx, filepath.Clean(lockPath))
	if err != nil {
		return nil, err
	}
	file, err := acquireReferenceFileLock(ctx, lockPath)
	if err != nil {
		release()
		return nil, fmt.Errorf("control/modelconfig/credentialstore: lock credential reference: %w", err)
	}
	if err := ctx.Err(); err != nil {
		closeErr := file.Close()
		release()
		return nil, errors.Join(err, closeErr)
	}
	return &referenceLock{file: file, release: release}, nil
}

func (l *referenceLock) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.file != nil {
			l.err = l.file.Close()
		}
		if l.release != nil {
			l.release()
		}
	})
	return l.err
}

func acquireReferenceGate(ctx context.Context, path string) (func(), error) {
	referenceGates.Lock()
	gate := referenceGates.byPath[path]
	if gate == nil {
		gate = &referenceGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}
		referenceGates.byPath[path] = gate
	}
	gate.users++
	referenceGates.Unlock()

	select {
	case <-ctx.Done():
		releaseReferenceGateUser(path, gate)
		return nil, ctx.Err()
	case <-gate.token:
		if err := ctx.Err(); err != nil {
			gate.token <- struct{}{}
			releaseReferenceGateUser(path, gate)
			return nil, err
		}
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			gate.token <- struct{}{}
			releaseReferenceGateUser(path, gate)
		})
	}, nil
}

func releaseReferenceGateUser(path string, gate *referenceGate) {
	referenceGates.Lock()
	defer referenceGates.Unlock()
	gate.users--
	if gate.users == 0 && referenceGates.byPath[path] == gate {
		delete(referenceGates.byPath, path)
	}
}
