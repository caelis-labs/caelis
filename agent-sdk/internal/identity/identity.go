// Package identity creates process-independent durable SDK identifiers.
package identity

import (
	"crypto/rand"
	"strings"
)

// New returns an identifier with at least 128 bits of cryptographic randomness.
func New(prefix string) string {
	token := strings.ToLower(rand.Text())
	if prefix = strings.TrimSpace(prefix); prefix != "" {
		return prefix + "-" + token
	}
	return token
}
