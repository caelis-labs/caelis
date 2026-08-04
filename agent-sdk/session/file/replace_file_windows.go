//go:build windows

package file

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const (
	windowsSharingRetryBudget      = 350 * time.Millisecond
	windowsAccessDeniedRetryBudget = 80 * time.Millisecond
	windowsRetryInitialDelay       = 10 * time.Millisecond
	windowsRetryMaximumDelay       = 100 * time.Millisecond
)

type windowsFileRetryPolicy struct {
	budget       time.Duration
	maximumDelay time.Duration
}

type filePrimitiveError struct {
	operation fileOperation
	from      string
	to        string
	err       error
}

func (e *filePrimitiveError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.from != "" {
		return (&os.LinkError{Op: string(e.operation), Old: e.from, New: e.to, Err: e.err}).Error()
	}
	return (&os.PathError{Op: string(e.operation), Path: e.to, Err: e.err}).Error()
}

func (e *filePrimitiveError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *filePrimitiveError) DisplayMessage() string {
	return "Caelis could not update local session storage."
}

func replaceFile(ctx context.Context, logger *slog.Logger, operation fileOperation, from, to string) error {
	return retryWindowsFileOperation(ctx, logger, operation, from, to, false, func() error {
		fromPtr, err := windows.UTF16PtrFromString(from)
		if err != nil {
			return err
		}
		toPtr, err := windows.UTF16PtrFromString(to)
		if err != nil {
			return err
		}
		return windows.MoveFileEx(
			fromPtr,
			toPtr,
			windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
		)
	})
}

func removeFile(ctx context.Context, logger *slog.Logger, operation fileOperation, path string) error {
	return retryWindowsFileOperation(ctx, logger, operation, "", path, true, func() error {
		return os.Remove(path)
	})
}

func retryWindowsFileOperation(
	ctx context.Context,
	logger *slog.Logger,
	operation fileOperation,
	from string,
	to string,
	missingIsSuccess bool,
	attempt func() error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	started := time.Now()
	delay := windowsRetryInitialDelay
	attempts := 0
	lastClass := ""
	var lastCode uint64
	for {
		if err := ctx.Err(); err != nil {
			return windowsFileOperationFailure(
				ctx, logger, operation, from, to, attempts, started, lastClass, lastCode, "canceled", err,
			)
		}
		attempts++
		err := attempt()
		if err == nil || (missingIsSuccess && os.IsNotExist(err)) {
			if attempts > 1 {
				logWindowsFileOperation(
					ctx, logger, slog.LevelWarn, operation, to, lastClass, lastCode, attempts, started, "recovered",
				)
			}
			return nil
		}
		lastClass, lastCode = windowsFileError(err)
		policy, retry := windowsFileRetryPolicyFor(operation, from, to, err)
		if !retry || delay <= 0 || time.Since(started)+delay > policy.budget {
			return windowsFileOperationFailure(
				ctx, logger, operation, from, to, attempts, started, lastClass, lastCode, "failed", err,
			)
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return windowsFileOperationFailure(
				ctx,
				logger,
				operation,
				from,
				to,
				attempts,
				started,
				lastClass,
				lastCode,
				"canceled",
				errors.Join(ctx.Err(), err),
			)
		case <-timer.C:
		}
		delay *= 2
		if delay > policy.maximumDelay {
			delay = policy.maximumDelay
		}
	}
}

func windowsFileOperationFailure(
	ctx context.Context,
	logger *slog.Logger,
	operation fileOperation,
	from string,
	to string,
	attempts int,
	started time.Time,
	errorClass string,
	errorCode uint64,
	outcome string,
	err error,
) error {
	logWindowsFileOperation(ctx, logger, slog.LevelError, operation, to, errorClass, errorCode, attempts, started, outcome)
	return &filePrimitiveError{operation: operation, from: from, to: to, err: err}
}

func windowsFileRetryPolicyFor(operation fileOperation, from, to string, err error) (windowsFileRetryPolicy, bool) {
	policy := windowsFileRetryPolicy{
		budget:       windowsSharingRetryBudget,
		maximumDelay: windowsRetryMaximumDelay,
	}
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return policy, true
	}
	// ACCESS_DENIED also covers stable ACL and policy failures. Retry it only
	// for existing writable store files, and for a much shorter budget than a
	// typed sharing conflict. First-create failures therefore remain immediate.
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) || !windowsAccessDeniedRetryEligible(operation, from, to) {
		return windowsFileRetryPolicy{}, false
	}
	policy.budget = windowsAccessDeniedRetryBudget
	policy.maximumDelay = 40 * time.Millisecond
	return policy, true
}

func windowsAccessDeniedRetryEligible(operation fileOperation, from, to string) bool {
	if !windowsAccessibleParent(to) {
		return false
	}
	switch operation {
	case fileOperationReplaceDocument, fileOperationReplaceTransaction:
		return windowsWritableRegularFile(from) && windowsWritableRegularFile(to)
	case fileOperationRemoveTransaction, fileOperationRemoveRecoveryMarker:
		return windowsWritableRegularFile(to)
	default:
		return false
	}
}

func windowsAccessibleParent(path string) bool {
	info, err := os.Stat(filepath.Dir(path))
	return err == nil && info.IsDir()
}

func windowsWritableRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o200 != 0
}

func windowsFileError(err error) (string, uint64) {
	var code syscall.Errno
	_ = errors.As(err, &code)
	switch {
	case errors.Is(err, windows.ERROR_SHARING_VIOLATION):
		return "sharing_violation", uint64(code)
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return "lock_violation", uint64(code)
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return "access_denied", uint64(code)
	default:
		return "other", uint64(code)
	}
}

func logWindowsFileOperation(
	ctx context.Context,
	logger *slog.Logger,
	level slog.Level,
	operation fileOperation,
	path string,
	errorClass string,
	errorCode uint64,
	attempts int,
	started time.Time,
	outcome string,
) {
	if logger == nil {
		return
	}
	logger.LogAttrs(
		ctx,
		level,
		"session file operation",
		slog.String("component", "session_file"),
		slog.String("operation", string(operation)),
		slog.String("platform", "windows"),
		slog.String("error_class", errorClass),
		slog.Uint64("win32_code", errorCode),
		slog.Int("attempts", attempts),
		slog.Int64("elapsed_ms", time.Since(started).Milliseconds()),
		slog.String("outcome", outcome),
		slog.String("path_class", windowsPathClass(path)),
	)
}

func windowsPathClass(path string) string {
	cleaned := strings.ToLower(filepath.Clean(path))
	switch {
	case strings.HasPrefix(cleaned, `\\?\unc\`):
		return "unc"
	case strings.HasPrefix(cleaned, `\\?\`), strings.HasPrefix(cleaned, `\\.\`):
		return "local"
	case strings.HasPrefix(cleaned, `\\`):
		return "unc"
	}
	if filepath.IsAbs(cleaned) || filepath.VolumeName(cleaned) != "" {
		return "local"
	}
	return "relative"
}
