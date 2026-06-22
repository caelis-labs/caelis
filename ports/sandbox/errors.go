package sandbox

import (
	"strings"
)

const SandboxPermissionDeniedMessage = "Sandbox permission denied. Use a writable workspace path, narrow the operation, or retry the same necessary command with sandbox_permissions=require_escalated."

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
