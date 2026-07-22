package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
)

// CustomConnectionID returns a stable connection identity derived from the
// complete launcher rather than only the executable basename.
func CustomConnectionID(command string, launcher Launcher) string {
	base := Slug(filepath.Base(strings.TrimSpace(command)))
	if base == "" {
		base = "agent"
	}
	return "custom-" + base + "-x" + shortDigest(LaunchFingerprint(launcher))
}

// Slug normalizes a display value into the Agent name alphabet.
func Slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var out strings.Builder
	dash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
			dash = false
		case out.Len() > 0 && !dash:
			out.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(out.String(), "-")
}

func allocateAgentID(preferred string, namespace string, backingKey string, used map[string]struct{}, allowed NameFilter) string {
	namespace = Slug(namespace)
	if namespace == "" || startsWithDigit(namespace) {
		namespace = "agent"
	}
	base := Slug(preferred)
	if base == "" {
		base = namespace + "-agent"
	}
	if startsWithDigit(base) {
		base = namespace + "-" + base
	}
	candidates := []string{
		base,
		namespace + "-" + base,
		namespace + "-" + base + "-x" + shortDigest(backingKey),
	}
	for _, candidate := range candidates {
		if agentIDAvailable(candidate, used, allowed) {
			return candidate
		}
	}
	stable := candidates[len(candidates)-1]
	for suffix := 2; suffix < 10_000; suffix++ {
		candidate := fmt.Sprintf("%s-alt%d", stable, suffix)
		if agentIDAvailable(candidate, used, allowed) {
			return candidate
		}
	}
	return ""
}

func agentIDAvailable(id string, used map[string]struct{}, allowed NameFilter) bool {
	id = NormalizeName(id)
	if !IsName(id) {
		return false
	}
	if _, exists := used[id]; exists {
		return false
	}
	return allowed == nil || allowed(id)
}

func startsWithDigit(value string) bool {
	return value != "" && value[0] >= '0' && value[0] <= '9'
}

func shortDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:4])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
