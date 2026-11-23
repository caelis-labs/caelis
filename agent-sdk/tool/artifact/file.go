// Package artifact provides temporary file storage for complete tool results.
package artifact

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const (
	defaultMaxAge   = 24 * time.Hour
	defaultMaxFiles = 128
	defaultMaxBytes = 64 * 1024 * 1024
	defaultFileMax  = 64 * 1024 * 1024
)

// FileStore retains tool output in a private directory, evicting old files by age, count and size.
// Share one store between agents that use the same directory.
type FileStore struct {
	dir      string
	maxAge   time.Duration
	maxFiles int
	maxBytes int64
	fileMax  int64
	now      func() time.Time

	mu sync.Mutex
}

type artifactFile struct {
	path    string
	size    int64
	modTime time.Time
}

// NewFileStore uses a dedicated absolute directory, created on the first write.
func NewFileStore(dir string) *FileStore {
	return &FileStore{
		dir:      filepath.Clean(dir),
		maxAge:   defaultMaxAge,
		maxFiles: defaultMaxFiles,
		maxBytes: defaultMaxBytes,
		fileMax:  defaultFileMax,
		now:      time.Now,
	}
}

// Save returns the absolute path to the complete result for use in the model view.
func (s *FileStore) Save(ctx context.Context, result tool.Result) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	content, extension, ok := encodeContent(result.Content)
	if !ok || len(content) == 0 || (s.fileMax > 0 && int64(len(content)) > s.fileMax) {
		return "", errors.New("tool result is empty, unencodable or exceeds the artifact limit")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := ensurePrivateDir(s.dir); err != nil {
		return "", err
	}
	if err := s.cleanupLocked(int64(len(content))); err != nil {
		return "", err
	}

	target, err := s.unusedPathLocked(extension)
	if err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(s.dir, ".tool-result-*")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	written := false
	defer func() {
		_ = temporary.Close()
		if !written {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return "", err
	}
	if _, err := temporary.Write(content); err != nil {
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return "", err
	}
	written = true
	return target, nil
}

func (s *FileStore) unusedPathLocked(extension string) (string, error) {
	for range 4 {
		uid, err := randomID()
		if err != nil {
			return "", err
		}
		path := filepath.Join(s.dir, uid+extension)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return path, nil
		}
	}
	return "", errors.New("cannot allocate unique tool result path")
}

func ensurePrivateDir(dir string) error {
	if !filepath.IsAbs(dir) || dir == filepath.Dir(dir) || dir == filepath.Clean(os.TempDir()) {
		return errors.New("tool result artifacts require a dedicated absolute directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	base := filepath.Clean(os.TempDir())
	if strings.HasPrefix(dir, base+string(os.PathSeparator)) {
		resolvedBase, baseErr := filepath.EvalSymlinks(base)
		resolvedDir, dirErr := filepath.EvalSymlinks(dir)
		if baseErr != nil || dirErr != nil {
			return errors.New("tool result artifact directory cannot be resolved")
		}
		relative, err := filepath.Rel(base, dir)
		if err != nil || filepath.Clean(resolvedDir) != filepath.Join(filepath.Clean(resolvedBase), relative) {
			return errors.New("tool result artifact directory contains a symbolic link")
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("tool result artifact path is not a private directory")
	}
	return os.Chmod(dir, 0o700)
}

func (s *FileStore) cleanupLocked(requiredBytes int64) error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	files := make([]artifactFile, 0, len(entries))
	var totalBytes int64
	for _, entry := range entries {
		if entry.IsDir() || !isArtifactName(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		path := filepath.Join(s.dir, entry.Name())
		if s.maxAge > 0 && now.Sub(info.ModTime()) > s.maxAge {
			if os.Remove(path) == nil {
				continue
			}
		}
		files = append(files, artifactFile{path: path, size: info.Size(), modTime: info.ModTime()})
		totalBytes += info.Size()
	}
	slices.SortFunc(files, func(a, b artifactFile) int {
		if a.modTime.Equal(b.modTime) {
			return strings.Compare(a.path, b.path)
		}
		if a.modTime.Before(b.modTime) {
			return -1
		}
		return 1
	})
	for len(files) > 0 && ((s.maxFiles > 0 && len(files)+1 > s.maxFiles) || (s.maxBytes > 0 && totalBytes+requiredBytes > s.maxBytes)) {
		if err := os.Remove(files[0].path); err != nil {
			return err
		}
		totalBytes -= files[0].size
		files = files[1:]
	}
	if (s.maxFiles > 0 && len(files)+1 > s.maxFiles) || (s.maxBytes > 0 && totalBytes+requiredBytes > s.maxBytes) {
		return errors.New("tool result artifact limits cannot accommodate result")
	}
	return nil
}

func encodeContent(blocks []tool.ContentBlock) ([]byte, string, bool) {
	if len(blocks) == 0 {
		return nil, "", false
	}
	if len(blocks) == 1 {
		switch block := blocks[0].(type) {
		case tool.Text:
			return []byte(block), ".txt", true
		case tool.StructuredContent:
			return block.Value, ".json", json.Valid(block.Value)
		}
	}
	// Reuse the result codec for ordered blocks, without serializing invocation
	// identity or metadata. The model projection loses resource information.
	raw, err := json.Marshal(tool.Result{Content: blocks})
	return raw, ".json", err == nil
}

func randomID() (string, error) {
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func isArtifactName(name string) bool {
	extension := filepath.Ext(name)
	if extension != ".txt" && extension != ".json" {
		return false
	}
	stem := strings.TrimSuffix(name, extension)
	if len(stem) != 12 {
		return false
	}
	_, err := hex.DecodeString(stem)
	return err == nil
}
