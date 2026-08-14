package gatewayapp

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

const (
	runtimeDiagnosticsFilename = "runtime.jsonl"
	runtimeDiagnosticsMaxBytes = int64(2 << 20)
)

// newRuntimeDiagnosticsLogger returns a best-effort, bounded JSONL logger for
// runtime and environment failures. Call sites must use fixed classifications;
// user content, Session identities, workspace values, and full paths are not
// permitted fields.
func newRuntimeDiagnosticsLogger(storeDir string) *slog.Logger {
	path := filepath.Join(storeDir, "logs", runtimeDiagnosticsFilename)
	writer := &boundedDiagnosticWriter{path: path, maxBytes: runtimeDiagnosticsMaxBytes}
	return slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

type boundedDiagnosticWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
}

func (w *boundedDiagnosticWriter) Write(data []byte) (int, error) {
	if w == nil || w.path == "" || w.maxBytes <= 0 {
		return 0, fmt.Errorf("gatewayapp diagnostics: invalid writer configuration")
	}
	if int64(len(data)) > w.maxBytes {
		return 0, fmt.Errorf("gatewayapp diagnostics: record exceeds file limit")
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	dir := filepath.Dir(w.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return 0, err
	}
	if err := w.rotateIfNeeded(int64(len(data))); err != nil {
		return 0, err
	}
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	if err := os.Chmod(w.path, 0o600); err != nil {
		_ = file.Close()
		return 0, err
	}
	n, writeErr := file.Write(data)
	closeErr := file.Close()
	return n, errors.Join(writeErr, closeErr)
}

func (w *boundedDiagnosticWriter) rotateIfNeeded(incoming int64) error {
	info, err := os.Stat(w.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case info.Size()+incoming <= w.maxBytes:
		return nil
	}

	backup := w.path + ".1"
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(w.path, backup)
}
