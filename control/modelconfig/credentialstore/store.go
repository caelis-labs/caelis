package credentialstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const schemaVersion = 1

// Store owns API-key credentials below one Control state directory.
type Store struct {
	mu   sync.Mutex
	root string
}

type record struct {
	Version     int    `json:"version"`
	Ref         string `json:"ref"`
	APIKey      string `json:"api_key,omitempty"`
	Environment string `json:"environment,omitempty"`
}

// Source is the Control-owned source behind one opaque credential reference.
// Exactly one of APIKey or Environment is populated.
type Source struct {
	APIKey      string
	Environment string
}

// BuildReference returns a stable opaque reference for one provider endpoint.
func BuildReference(provider, providerEndpointID string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	providerEndpointID = strings.ToLower(strings.TrimSpace(providerEndpointID))
	if provider == "" || providerEndpointID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(provider + "\x00" + providerEndpointID))
	return "apikey:" + hex.EncodeToString(sum[:16])
}

// New constructs a credential store below one Control state directory.
func New(storeDir string) (*Store, error) {
	storeDir = strings.TrimSpace(storeDir)
	if storeDir == "" {
		return nil, fmt.Errorf("control/modelconfig/credentialstore: state directory is required")
	}
	return &Store{root: filepath.Join(storeDir, "providers", "credentials")}, nil
}

// Put atomically stores one API key under its opaque reference.
func (s *Store) Put(ctx context.Context, ref, apiKey string) error {
	if s == nil {
		return fmt.Errorf("control/modelconfig/credentialstore: store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ref, apiKey = strings.ToLower(strings.TrimSpace(ref)), strings.TrimSpace(apiKey)
	if ref == "" || !strings.HasPrefix(ref, "apikey:") {
		return fmt.Errorf("control/modelconfig/credentialstore: valid API-key reference is required")
	}
	if apiKey == "" {
		return fmt.Errorf("control/modelconfig/credentialstore: API key is required")
	}
	return s.putRecord(record{Version: schemaVersion, Ref: ref, APIKey: apiKey})
}

// PutEnvironment stores an environment-variable credential source behind an
// opaque reference without copying its current secret value into Control
// configuration or the credential record.
func (s *Store) PutEnvironment(ctx context.Context, ref, environment string) error {
	if s == nil {
		return fmt.Errorf("control/modelconfig/credentialstore: store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ref = strings.ToLower(strings.TrimSpace(ref))
	environment = strings.TrimSpace(environment)
	if ref == "" || !strings.HasPrefix(ref, "apikey:") {
		return fmt.Errorf("control/modelconfig/credentialstore: valid API-key reference is required")
	}
	if environment == "" {
		return fmt.Errorf("control/modelconfig/credentialstore: environment variable is required")
	}
	return s.putRecord(record{Version: schemaVersion, Ref: ref, Environment: environment})
}

func (s *Store) putRecord(stored record) error {
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("control/modelconfig/credentialstore: encode credential: %w", err)
	}
	data = append(data, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ensureDir(s.root); err != nil {
		return err
	}
	return atomicWrite(s.path(stored.Ref), data)
}

// Get returns the API key stored behind one opaque reference.
func (s *Store) Get(ctx context.Context, ref string) (string, error) {
	source, err := s.LookupSource(ctx, ref)
	if err != nil {
		return "", err
	}
	if source.APIKey != "" {
		return source.APIKey, nil
	}
	value := strings.TrimSpace(os.Getenv(source.Environment))
	if value == "" {
		return "", fmt.Errorf("control/modelconfig/credentialstore: environment variable %s is empty: %w", source.Environment, os.ErrNotExist)
	}
	return value, nil
}

// LookupSource returns the source behind an opaque reference without resolving
// an environment variable. It supports transactional replacement and rollback.
func (s *Store) LookupSource(ctx context.Context, ref string) (Source, error) {
	if s == nil {
		return Source{}, fmt.Errorf("control/modelconfig/credentialstore: store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Source{}, err
	}
	ref = strings.ToLower(strings.TrimSpace(ref))
	if ref == "" || !strings.HasPrefix(ref, "apikey:") {
		return Source{}, fmt.Errorf("control/modelconfig/credentialstore: valid API-key reference is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path(ref)
	info, err := os.Lstat(path)
	if err != nil {
		return Source{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Source{}, fmt.Errorf("control/modelconfig/credentialstore: credential path is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Source{}, err
	}
	var stored record
	if err := json.Unmarshal(data, &stored); err != nil {
		return Source{}, fmt.Errorf("control/modelconfig/credentialstore: decode credential: %w", err)
	}
	stored.Ref = strings.ToLower(strings.TrimSpace(stored.Ref))
	stored.APIKey = strings.TrimSpace(stored.APIKey)
	stored.Environment = strings.TrimSpace(stored.Environment)
	if stored.Version != schemaVersion || stored.Ref != ref || (stored.APIKey == "") == (stored.Environment == "") {
		return Source{}, fmt.Errorf("control/modelconfig/credentialstore: credential is incomplete or mismatched")
	}
	return Source{APIKey: stored.APIKey, Environment: stored.Environment}, nil
}

// Delete removes one stored credential. A missing credential is already in
// the desired state.
func (s *Store) Delete(ctx context.Context, ref string) error {
	if s == nil {
		return fmt.Errorf("control/modelconfig/credentialstore: store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ref = strings.ToLower(strings.TrimSpace(ref))
	if ref == "" || !strings.HasPrefix(ref, "apikey:") {
		return fmt.Errorf("control/modelconfig/credentialstore: valid API-key reference is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path(ref))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Store) path(ref string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(ref))))
	return filepath.Join(s.root, hex.EncodeToString(sum[:])+".json")
}

func ensureDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("control/modelconfig/credentialstore: create directory: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("control/modelconfig/credentialstore: secure directory: %w", err)
	}
	return nil
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".credential.*.tmp")
	if err != nil {
		return fmt.Errorf("control/modelconfig/credentialstore: create temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	committed = true
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}
