package sandbox

import "strings"

// IsSandboxPermissionDeniedText reports whether text looks like a sandbox or
// filesystem policy denial. It is intentionally conservative and used only for
// diagnostics, not authorization.
func IsSandboxPermissionDeniedText(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return false
	}
	needles := []string{
		"permission denied",
		"operation not permitted",
		"read-only file system",
		"access is denied",
		"access denied",
		"unauthorizedaccess",
		"eperm",
		"eacces",
	}
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
