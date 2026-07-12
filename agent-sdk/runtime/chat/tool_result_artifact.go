package chat

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const (
	defaultToolResultArtifactMaxAge   = 24 * time.Hour
	defaultToolResultArtifactMaxFiles = 128
	defaultToolResultArtifactMaxBytes = 64 * 1024 * 1024
	defaultToolResultArtifactFileMax  = 64 * 1024 * 1024
)

type toolResultArtifactStore struct {
	dir      string
	maxAge   time.Duration
	maxFiles int
	maxBytes int64
	fileMax  int64
	now      func() time.Time

	mu sync.Mutex
}

type toolResultArtifactFile struct {
	path    string
	size    int64
	modTime time.Time
}

func defaultToolResultArtifactStore() *toolResultArtifactStore {
	return &toolResultArtifactStore{
		dir:      filepath.Join(os.TempDir(), "caelis", "tool-results"),
		maxAge:   defaultToolResultArtifactMaxAge,
		maxFiles: defaultToolResultArtifactMaxFiles,
		maxBytes: defaultToolResultArtifactMaxBytes,
		fileMax:  defaultToolResultArtifactFileMax,
		now:      time.Now,
	}
}

func (s *toolResultArtifactStore) write(result tool.Result) (string, bool) {
	if s == nil {
		return "", false
	}
	content, extension, ok := rawToolResultArtifact(result.Content)
	if !ok || len(content) == 0 || (s.fileMax > 0 && int64(len(content)) > s.fileMax) {
		return "", false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ensurePrivateToolResultArtifactDir(s.dir); err != nil {
		return "", false
	}
	if err := s.cleanupLocked(int64(len(content))); err != nil {
		return "", false
	}

	target, ok := s.unusedPathLocked(extension)
	if !ok {
		return "", false
	}
	temporary, err := os.CreateTemp(s.dir, ".tool-result-*")
	if err != nil {
		return "", false
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
		return "", false
	}
	if _, err := temporary.Write(content); err != nil {
		return "", false
	}
	if err := temporary.Close(); err != nil {
		return "", false
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return "", false
	}
	written = true
	return target, true
}

func (s *toolResultArtifactStore) unusedPathLocked(extension string) (string, bool) {
	for range 4 {
		uid, err := randomToolResultArtifactID()
		if err != nil {
			return "", false
		}
		path := filepath.Join(s.dir, uid+extension)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return path, true
		}
	}
	return "", false
}

func (s *toolResultArtifactStore) remove(path string) error {
	if s == nil || filepath.Clean(filepath.Dir(path)) != filepath.Clean(s.dir) {
		return errors.New("tool result artifact path is outside store")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.Remove(path)
}

func ensurePrivateToolResultArtifactDir(dir string) error {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "." || !filepath.IsAbs(dir) {
		return errors.New("tool result artifact directory must be absolute")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	base := filepath.Clean(os.TempDir())
	boundary := filepath.Dir(dir)
	if strings.HasPrefix(dir, base+string(os.PathSeparator)) {
		boundary = base
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
	for path := dir; path != boundary && path != filepath.Dir(path); path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("tool result artifact path is not a private directory")
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (s *toolResultArtifactStore) cleanupLocked(requiredBytes int64) error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	files := make([]toolResultArtifactFile, 0, len(entries))
	var totalBytes int64
	for _, entry := range entries {
		if entry.IsDir() || !isToolResultArtifactName(entry.Name()) {
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
		files = append(files, toolResultArtifactFile{path: path, size: info.Size(), modTime: info.ModTime()})
		totalBytes += info.Size()
	}
	slices.SortFunc(files, func(a, b toolResultArtifactFile) int {
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

func rawToolResultArtifact(parts []model.Part) ([]byte, string, bool) {
	if len(parts) != 1 {
		return nil, "", false
	}
	part := parts[0]
	switch {
	case part.Text != nil:
		return []byte(part.Text.Text), ".txt", true
	case part.JSON != nil:
		return append([]byte(nil), part.JSON.Value...), ".json", true
	default:
		return nil, "", false
	}
}

func randomToolResultArtifactID() (string, error) {
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func isToolResultArtifactName(name string) bool {
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
