// Package hostownership owns the process-lifetime product Host guard for one
// durable Store. The returned Authority is an opaque proof that the caller
// actually acquired the underlying file lock; zero values cannot authorize a
// prior-Host Session fence replacement.
package hostownership

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/internal/filelock"
	"github.com/caelis-labs/caelis/internal/productpaths"
)

// Filename is the canonical product Host authority-lock filename.
const Filename = "authority.lock"

// Authority proves exclusive process ownership of one Store until Close.
type Authority struct {
	state *authorityState
}

type authorityState struct {
	storeKey string
	lease    io.Closer

	mu       sync.RWMutex
	closed   bool
	closeErr error
}

// Acquire takes the process-lifetime product Host guard for storeDir.
func Acquire(ctx context.Context, storeDir string) (*Authority, error) {
	path := Path(storeDir)
	if path == "" {
		return nil, errors.New("hostownership: store directory is required")
	}
	lease, err := filelock.Acquire(ctx, path)
	if err != nil {
		return nil, err
	}
	return &Authority{state: &authorityState{storeKey: storeKey(storeDir), lease: lease}}, nil
}

// Authorizes reports whether this still-live guard owns storeDir.
func (a *Authority) Authorizes(storeDir string) bool {
	release, ok := a.Pin(storeDir)
	if !ok {
		return false
	}
	release()
	return true
}

// Pin verifies Store ownership and prevents Close from releasing the
// underlying process lock until the returned function is called. The returned
// release function is idempotent.
func (a *Authority) Pin(storeDir string) (release func(), ok bool) {
	if a == nil || a.state == nil {
		return nil, false
	}
	state := a.state
	state.mu.RLock()
	if state.closed || state.lease == nil || state.storeKey == "" || state.storeKey != storeKey(storeDir) {
		state.mu.RUnlock()
		return nil, false
	}
	var once sync.Once
	return func() { once.Do(state.mu.RUnlock) }, true
}

// Close releases the process-lifetime product Host guard exactly once.
func (a *Authority) Close() error {
	if a == nil || a.state == nil {
		return nil
	}
	state := a.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return state.closeErr
	}
	state.closed = true
	if state.lease != nil {
		state.closeErr = state.lease.Close()
		state.lease = nil
	}
	return state.closeErr
}

// Path returns the canonical product Host authority-lock path for storeDir.
func Path(storeDir string) string {
	storeDir = strings.TrimSpace(storeDir)
	if storeDir == "" {
		return ""
	}
	return filepath.Join(productpaths.ServiceRuntimeDir(storeDir), Filename)
}

func storeKey(storeDir string) string {
	storeDir = strings.TrimSpace(storeDir)
	if storeDir == "" {
		return ""
	}
	if absolute, err := filepath.Abs(storeDir); err == nil {
		storeDir = absolute
	}
	storeDir = filepath.Clean(storeDir)
	if runtime.GOOS == "windows" {
		storeDir = strings.ToLower(storeDir)
	}
	return storeDir
}
