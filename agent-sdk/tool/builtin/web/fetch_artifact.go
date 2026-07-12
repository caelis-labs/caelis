package web

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type fetchContentArtifact struct {
	Path string
}

type fetchArtifactFile struct {
	path    string
	size    int64
	modTime time.Time
}

func (t *FetchTool) writeContentArtifact(resp fetchResponseMeta, content string, format string) (fetchContentArtifact, error) {
	dir := strings.TrimSpace(t.cfg.ArtifactDir)
	if dir == "" {
		dir = filepath.Join(os.TempDir(), defaultArtifactRootName)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fetchContentArtifact{}, fmt.Errorf("WebFetch: create artifact directory: %w", err)
	}
	_ = t.cleanupContentArtifacts(dir, "")
	sum := sha256.Sum256([]byte(resp.finalURL + "\n" + format + "\n" + content))
	host := sanitizeArtifactNamePart(hostForArtifact(resp.finalURL))
	if host == "" {
		host = "content"
	}
	name := fmt.Sprintf("%s-%s-%s%s",
		time.Now().UTC().Format("20060102T150405.000000000Z"),
		hex.EncodeToString(sum[:])[:12],
		host,
		artifactExtension(format),
	)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fetchContentArtifact{}, fmt.Errorf("WebFetch: write artifact: %w", err)
	}
	_ = t.cleanupContentArtifacts(dir, path)
	return fetchContentArtifact{Path: path}, nil
}

func (t *FetchTool) cleanupContentArtifacts(dir string, protectPath string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	now := time.Now()
	files := make([]fetchArtifactFile, 0, len(entries))
	protectPath = filepath.Clean(protectPath)
	for _, entry := range entries {
		if entry.IsDir() || !isFetchArtifactName(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if t.cfg.ArtifactMaxAge > 0 && now.Sub(info.ModTime()) > t.cfg.ArtifactMaxAge && filepath.Clean(path) != protectPath {
			_ = os.Remove(path)
			continue
		}
		files = append(files, fetchArtifactFile{path: path, size: info.Size(), modTime: info.ModTime()})
	}
	sortFetchArtifacts(files)
	totalBytes := int64(0)
	for _, file := range files {
		totalBytes += file.size
	}
	for len(files) > t.cfg.ArtifactMaxFiles && len(files) > 0 {
		idx := firstDeletableFetchArtifact(files, protectPath)
		if idx < 0 {
			break
		}
		totalBytes -= files[idx].size
		_ = os.Remove(files[idx].path)
		files = append(files[:idx], files[idx+1:]...)
	}
	for t.cfg.ArtifactMaxBytes > 0 && totalBytes > t.cfg.ArtifactMaxBytes && len(files) > 0 {
		idx := firstDeletableFetchArtifact(files, protectPath)
		if idx < 0 {
			break
		}
		totalBytes -= files[idx].size
		_ = os.Remove(files[idx].path)
		files = append(files[:idx], files[idx+1:]...)
	}
	return nil
}

func firstDeletableFetchArtifact(files []fetchArtifactFile, protectPath string) int {
	for idx, file := range files {
		if filepath.Clean(file.path) != protectPath {
			return idx
		}
	}
	return -1
}

func sortFetchArtifacts(files []fetchArtifactFile) {
	slices.SortFunc(files, func(a, b fetchArtifactFile) int {
		if a.modTime.Equal(b.modTime) {
			return strings.Compare(a.path, b.path)
		}
		if a.modTime.Before(b.modTime) {
			return -1
		}
		return 1
	})
}

func isFetchArtifactName(name string) bool {
	ext := filepath.Ext(name)
	if ext != ".md" && ext != ".txt" && ext != ".html" {
		return false
	}
	parts := strings.SplitN(strings.TrimSuffix(name, ext), "-", 3)
	if len(parts) != 3 {
		return false
	}
	if _, err := time.Parse("20060102T150405.000000000Z", parts[0]); err != nil {
		return false
	}
	if len(parts[1]) != 12 {
		return false
	}
	if _, err := hex.DecodeString(parts[1]); err != nil {
		return false
	}
	return strings.TrimSpace(parts[2]) != ""
}

func hostForArtifact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func sanitizeArtifactNamePart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		case r == '.':
			b.WriteByte('-')
		}
		if b.Len() >= 48 {
			break
		}
	}
	return strings.Trim(b.String(), "-_")
}

func artifactExtension(format string) string {
	switch format {
	case "html":
		return ".html"
	case "text":
		return ".txt"
	default:
		return ".md"
	}
}
