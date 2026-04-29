package sandbox

import (
	"strings"
)

const SandboxPermissionDeniedMessage = "Sandbox permission denied. Use a writable workspace path or request elevated permissions."

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
	raw := sandboxPermissionRawDetail(result, err)
	if !IsSandboxPermissionDeniedText(raw) {
		return "", false
	}
	if raw == "" {
		return SandboxPermissionDeniedMessage, true
	}
	return SandboxPermissionDeniedMessage + "\n" + raw, true
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
	patterns := []string{
		"read-only file system",
		"只读文件系统",
		"permission denied",
		"operation not permitted",
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
