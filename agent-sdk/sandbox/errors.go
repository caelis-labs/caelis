package sandbox

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const SandboxPermissionDeniedMessage = "Sandbox permission denied. Narrow the path or operation first. If still required, retry THIS SAME command once with sandbox_permissions=require_escalated and a justification that cites this failure. Do not escalate later similar commands by habit."
const HostExecutionRequiresApprovalMessage = "Host execution requires approval. If still required, retry THIS SAME command once with sandbox_permissions=require_escalated and a non-empty justification. Do not escalate later similar commands by habit."

// CommandExitError marks a normal non-zero process exit. It distinguishes a
// command result from an infrastructure failure without relying on error text.
type CommandExitError struct {
	Err error
}

// OutputCursorAheadError reports a cursor that is beyond the output currently
// published by a session. Callers must not silently clamp this condition
// because it indicates that cursor state belongs to a different stream history.
type OutputCursorAheadError struct {
	Stream    string
	Requested int64
	Available int64
}

func (e *OutputCursorAheadError) Error() string {
	if e == nil {
		return "sandbox output cursor is ahead"
	}
	return fmt.Sprintf(
		"sandbox %s output cursor %d is ahead of available cursor %d",
		e.Stream,
		e.Requested,
		e.Available,
	)
}

// ValidateOutputCursor returns a typed error when requested points beyond
// available. Negative requested offsets are normalized to zero.
func ValidateOutputCursor(requested, available OutputCursor) error {
	requested = NormalizeOutputCursor(requested)
	available = NormalizeOutputCursor(available)
	if requested.Stdout > available.Stdout {
		return &OutputCursorAheadError{
			Stream:    "stdout",
			Requested: requested.Stdout,
			Available: available.Stdout,
		}
	}
	if requested.Stderr > available.Stderr {
		return &OutputCursorAheadError{
			Stream:    "stderr",
			Requested: requested.Stderr,
			Available: available.Stderr,
		}
	}
	return nil
}

func (e *CommandExitError) Error() string {
	if e == nil || e.Err == nil {
		return "command exited"
	}
	return e.Err.Error()
}

func (e *CommandExitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// MarkCommandExit marks err as a normal command exit for sandbox adapters that
// cannot return os/exec.ExitError directly.
func MarkCommandExit(err error) error {
	if err == nil || IsCommandExit(err) {
		return err
	}
	return &CommandExitError{Err: err}
}

// IsCommandExit reports whether err represents a completed process with a
// non-zero exit status rather than a sandbox or transport failure.
func IsCommandExit(err error) bool {
	var marked *CommandExitError
	if errors.As(err, &marked) {
		return true
	}
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

func NormalizeSandboxPermissionFailure(result CommandResult, err error) (CommandResult, error) {
	// Command execution failures must preserve their original stdout/stderr/error
	// text so agents can identify the concrete denied path and recover with a
	// narrower command, cache override, or explicit escalation request.
	return result, err
}

func NormalizeSandboxPermissionResult(result CommandResult) CommandResult {
	return result
}

func NormalizeSandboxPermissionError(err error) error {
	return err
}

func NormalizeSandboxPermissionOutput(stream string, data []byte) []byte {
	return data
}

func SandboxPermissionDetail(result CommandResult, err error) (string, bool) {
	if !isSandboxExecutionResult(result) {
		return "", false
	}
	if detail := strings.TrimSpace(result.Error); detail != "" {
		return detail, true
	}
	raw := sandboxPermissionRawDetail(result, err)
	if !IsSandboxPermissionDeniedText(raw) {
		return "", false
	}
	if IsSandboxACLRefreshDeniedText(raw) {
		return raw, true
	}
	return SandboxPermissionDeniedMessage, true
}

func isSandboxExecutionResult(result CommandResult) bool {
	if result.Route != RouteSandbox {
		return false
	}
	switch result.Backend {
	case "", BackendHost:
		return false
	default:
		return true
	}
}

func sandboxPermissionRawDetail(result CommandResult, err error) string {
	var parts []string
	appendOne := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range parts {
			if existing == value {
				return
			}
		}
		parts = append(parts, value)
	}
	appendOne(result.Stderr)
	appendOne(result.Stdout)
	if err != nil {
		appendOne(err.Error())
	}
	return strings.Join(parts, "\n")
}

func IsSandboxPermissionDeniedText(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return false
	}
	if strings.Contains(text, "access to the path") && strings.Contains(text, " is denied") {
		return true
	}
	patterns := []string{
		"read-only file system",
		"只读文件系统",
		"permission denied",
		"access is denied",
		"operation not permitted",
		"write_dac",
		"write dac",
		"acl: write",
		" dacl",
		"could not lock config file",
		"cannot lock config file",
		"unable to lock config file",
		"无法锁定配置文件",
		"eacces",
		"eperm",
	}
	for _, pattern := range patterns {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

func IsSandboxCachePathEvidenceText(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return false
	}
	for _, pattern := range sandboxCachePathEvidencePatterns {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

var sandboxCachePathEvidencePatterns = []string{
	"gocache",
	"gomodcache",
	"go/pkg/mod",
	`go\pkg\mod`,
	`cache\go-build`,
	"/cache/go-build",
	`cache\go-mod`,
	"/cache/go-mod",
	"writing stat cache",
	"pip cache",
	"pip-cache",
	`pip\cache`,
	"/pip/cache",
	`cache\pip`,
	"/cache/pip",
	"npm cache",
	"npm-cache",
	`npm\cache`,
	"/npm/cache",
	`cache\npm`,
	"/cache/npm",
	`.nuget\packages`,
	"/.nuget/packages",
	`cache\nuget\packages`,
	"/cache/nuget/packages",
	"pnpm-store",
	`cache\pnpm-store`,
	"/cache/pnpm-store",
	"yarn-cache",
	`yarn\cache`,
	"/yarn/cache",
	`cache\yarn`,
	"/cache/yarn",
}

func IsSandboxACLRefreshDeniedText(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return false
	}
	patterns := []string{
		"refresh sandbox acls",
		"acl: write",
		" dacl",
		"write_dac",
		"write dac",
	}
	for _, pattern := range patterns {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}
