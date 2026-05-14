package filesystem

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"os"
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

func statRevision(info os.FileInfo) string {
	if info == nil {
		return ""
	}
	return fmt.Sprintf("stat:%d:%d:%s", info.Size(), info.ModTime().UTC().UnixNano(), info.Mode().String())
}

func isStatRevision(revision string) bool {
	return strings.HasPrefix(strings.TrimSpace(strings.ToLower(revision)), "stat:")
}

func revisionsMatch(expected string, actual string) bool {
	expected = strings.TrimSpace(strings.ToLower(expected))
	actual = strings.TrimSpace(strings.ToLower(actual))
	if expected == "" {
		return true
	}
	return expected == actual
}

func revisionsMatchFile(expected string, actualContent string, info os.FileInfo) bool {
	expected = strings.TrimSpace(strings.ToLower(expected))
	if expected == "" {
		return true
	}
	if isStatRevision(expected) {
		return expected == strings.TrimSpace(strings.ToLower(statRevision(info)))
	}
	return revisionsMatch(expected, textRevision(actualContent))
}

func staleRevisionError(path string) error {
	err := tool.NewError(tool.ErrorCodeStaleRevision, "tool: target "+path+" changed since it was read")
	err.Hint = "READ the file again and retry with the current revision."
	err.Retryable = true
	return err
}
