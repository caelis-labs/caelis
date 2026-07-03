package filesystem

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"os"

	"github.com/caelis-labs/caelis/ports/tool"
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
	return len(revision) >= len("stat:") && revision[:len("stat:")] == "stat:"
}

func revisionsMatch(expected string, actual string) bool {
	if expected == "" {
		return true
	}
	return expected == actual
}

func revisionsMatchFile(expected string, actualContent string, info os.FileInfo) bool {
	if expected == "" {
		return true
	}
	if isStatRevision(expected) {
		return expected == statRevision(info)
	}
	return revisionsMatch(expected, textRevision(actualContent))
}

func staleRevisionError() error {
	err := tool.NewError(tool.ErrorCodeStaleRevision, "tool: target changed since it was read")
	err.Hint = "READ the file again and retry with the current revision."
	err.Retryable = true
	return err
}

func missingWriteRevisionTargetError() error {
	err := tool.NewError(tool.ErrorCodeNotFound, "tool: target does not exist; omit if_revision to create it")
	err.Hint = "WRITE new files without if_revision, or READ an existing file and retry with its current revision."
	err.Retryable = true
	return err
}
