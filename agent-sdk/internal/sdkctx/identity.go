// Package sdkctx owns process-independent SDK identifiers, internal context
// propagation keys, lexical CWD matching, and internal test-harness hooks.
// It must remain free of imports from other agent-sdk packages.
package sdkctx

import (
	"crypto/rand"
	"strings"
)

// NewID returns an identifier with at least 128 bits of cryptographic randomness.
func NewID(prefix string) string {
	token := strings.ToLower(rand.Text())
	if prefix = strings.TrimSpace(prefix); prefix != "" {
		return prefix + "-" + token
	}
	return token
}
