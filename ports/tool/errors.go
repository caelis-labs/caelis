package tool

import (
	"errors"
	"fmt"
	"strings"
)

type ErrorCode string

const (
	ErrorCodeNotFound             ErrorCode = "not_found"
	ErrorCodePermissionDenied     ErrorCode = "permission_denied"
	ErrorCodeSandboxDenied        ErrorCode = "sandbox_denied"
	ErrorCodeSandboxUnavailable   ErrorCode = "sandbox_unavailable"
	ErrorCodeOldTextNotFound      ErrorCode = "old_text_not_found"
	ErrorCodeTooManyMatches       ErrorCode = "too_many_matches"
	ErrorCodeUnexpectedMatchCount ErrorCode = "unexpected_match_count"
	ErrorCodeStaleRevision        ErrorCode = "stale_revision"
	ErrorCodeTimeout              ErrorCode = "timeout"
	ErrorCodeOutputTruncated      ErrorCode = "output_truncated"
	ErrorCodeInvalidInput         ErrorCode = "invalid_input"
)

const (
	CommandSandboxPermissionUseDefault       = "use_default"
	CommandSandboxPermissionRequireEscalated = "require_escalated"
	CommandSandboxPermissionLegacyAdditional = "with_additional_permissions"
)

type ToolError struct {
	Code      ErrorCode
	Message   string
	Hint      string
	Retryable bool
	Err       error
}

func (e *ToolError) Error() string {
	if e == nil {
		return ""
	}
	if msg := strings.TrimSpace(e.Message); msg != "" {
		return msg
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	if e.Code != "" {
		return string(e.Code)
	}
	return "tool error"
}

func (e *ToolError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func NewError(code ErrorCode, message string) *ToolError {
	return &ToolError{Code: code, Message: strings.TrimSpace(message)}
}

func WrapError(code ErrorCode, err error, message string) *ToolError {
	return &ToolError{Code: code, Message: strings.TrimSpace(firstNonEmpty(message, errorString(err))), Err: err}
}

func RejectUnknownArgs(args map[string]any, allowed ...string) error {
	if len(args) == 0 {
		return nil
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		allowedSet[key] = struct{}{}
	}
	for key := range args {
		if _, ok := allowedSet[key]; !ok {
			return NewError(ErrorCodeInvalidInput, fmt.Sprintf("tool: arg %q is not supported", key))
		}
	}
	return nil
}

func NormalizeCommandSandboxPermission(value string, allowLegacy bool) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", CommandSandboxPermissionUseDefault:
		return CommandSandboxPermissionUseDefault, nil
	case CommandSandboxPermissionRequireEscalated:
		return CommandSandboxPermissionRequireEscalated, nil
	case CommandSandboxPermissionLegacyAdditional:
		if allowLegacy {
			return CommandSandboxPermissionUseDefault, nil
		}
	}
	return "", NewError(ErrorCodeInvalidInput, fmt.Sprintf("tool: arg %q must be %s", "sandbox_permissions", CommandSandboxPermissionAllowedValues(allowLegacy)))
}

func CommandSandboxPermissionAllowedValues(allowLegacy bool) string {
	if allowLegacy {
		return CommandSandboxPermissionUseDefault + ", " + CommandSandboxPermissionRequireEscalated + ", or " + CommandSandboxPermissionLegacyAdditional
	}
	return CommandSandboxPermissionUseDefault + " or " + CommandSandboxPermissionRequireEscalated
}

func ErrorPayload(err error) map[string]any {
	if err == nil {
		return nil
	}
	payload := map[string]any{
		"error": strings.TrimSpace(err.Error()),
	}
	var toolErr *ToolError
	if errors.As(err, &toolErr) && toolErr != nil {
		if toolErr.Code != "" {
			payload["error_code"] = string(toolErr.Code)
		}
		if hint := strings.TrimSpace(toolErr.Hint); hint != "" {
			payload["hint"] = hint
		}
		if toolErr.Retryable {
			payload["retryable"] = true
		}
		return payload
	}
	payload["error_code"] = string(classifyErrorCode(err))
	return payload
}

func classifyErrorCode(err error) ErrorCode {
	text := strings.ToLower(strings.TrimSpace(fmt.Sprint(err)))
	switch {
	case text == "":
		return ErrorCodeInvalidInput
	case strings.Contains(text, "sandbox permission denied"):
		return ErrorCodeSandboxDenied
	case strings.Contains(text, "sandbox unavailable"), strings.Contains(text, "backend unavailable"), strings.Contains(text, "not registered"):
		return ErrorCodeSandboxUnavailable
	case strings.Contains(text, "not found"), strings.Contains(text, "no such file"), strings.Contains(text, "does not exist"):
		return ErrorCodeNotFound
	case strings.Contains(text, "permission denied"), strings.Contains(text, "operation not permitted"), strings.Contains(text, "read-only file system"):
		return ErrorCodePermissionDenied
	case strings.Contains(text, "timed out"), strings.Contains(text, "timeout"):
		return ErrorCodeTimeout
	default:
		return ErrorCodeInvalidInput
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
