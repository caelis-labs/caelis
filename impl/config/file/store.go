// Package file contains a JSON file-backed configuration store.
package file

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/config"
)

type Store struct {
	root string
}

func New(root string) *Store {
	return &Store{root: strings.TrimSpace(root)}
}

func (s *Store) Load(ctx context.Context, key string, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if out == nil {
		return fmt.Errorf("config/file: output is required")
	}
	path, err := s.pathForKey(key)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err == nil {
		return json.Unmarshal(data, out)
	}
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *Store) Save(ctx context.Context, key string, in any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.pathForKey(key)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func (s *Store) pathForKey(key string) (string, error) {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return "", fmt.Errorf("config/file: root is required")
	}
	cleaned := strings.TrimSpace(key)
	if cleaned == "" {
		return "", fmt.Errorf("config/file: key is required")
	}
	cleaned = filepath.Clean(cleaned)
	if filepath.IsAbs(cleaned) || cleaned == "." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) || cleaned == ".." {
		return "", fmt.Errorf("config/file: invalid key %q", key)
	}
	if filepath.Ext(cleaned) == "" {
		cleaned += ".json"
	}
	return filepath.Join(s.root, cleaned), nil
}

var _ config.Store = (*Store)(nil)
