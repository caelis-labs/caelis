package filesystem

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func contentRevision(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func contentHasher() hash.Hash {
	return sha256.New()
}

func contentHashRevision(hasher hash.Hash) string {
	if hasher == nil {
		return ""
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil))
}

func textRevision(text string) string {
	return contentRevision([]byte(text))
}

func revisionsMatch(expected string, actual string) bool {
	expected = strings.TrimSpace(strings.ToLower(expected))
	actual = strings.TrimSpace(strings.ToLower(actual))
	if expected == "" {
		return true
	}
	return expected == actual
}

func staleRevisionError(path string) error {
	err := tool.NewError(tool.ErrorCodeStaleRevision, "tool: target "+path+" changed since it was read")
	err.Hint = "READ the file again and retry with the current revision."
	err.Retryable = true
	return err
}
