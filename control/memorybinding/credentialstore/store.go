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

	"github.com/caelis-labs/caelis/agent-sdk/atomicfile"
)

const schemaVersion = 1

// Store owns issuer credentials below one Caelis Control state directory.
// Memory policy, Grants, capabilities, and receipt data do not belong here.
type Store struct {
	root string
	mu   sync.Mutex
}

type record struct {
	Version    int    `json:"version"`
	Reference  string `json:"reference"`
	Credential string `json:"credential"`
}

// BuildReference returns a stable opaque reference for one issuer principal.
// It contains neither the principal reference nor credential material.
func BuildReference(principalRef string) string {
	principalRef = strings.TrimSpace(principalRef)
	if principalRef == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("caelis-memory-issuer-v1\x00" + principalRef))
	return "memory-issuer:" + hex.EncodeToString(sum[:16])
}

// New constructs a Memory issuer credential store without creating it.
func New(storeDir string) (*Store, error) {
	storeDir = strings.TrimSpace(storeDir)
	if storeDir == "" {
		return nil, fmt.Errorf("control/memorybinding/credentialstore: state directory is required")
	}
	return &Store{root: filepath.Join(storeDir, "memory", "credentials")}, nil
}

// Put atomically creates or replaces one issuer credential.
func (s *Store) Put(ctx context.Context, ref, credential string) error {
	if s == nil {
		return fmt.Errorf("control/memorybinding/credentialstore: store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ref = normalizeReference(ref)
	credential = strings.TrimSpace(credential)
	if err := validateReference(ref); err != nil {
		return err
	}
	if credential == "" {
		return fmt.Errorf("control/memorybinding/credentialstore: credential is required")
	}
	data, err := json.Marshal(record{Version: schemaVersion, Reference: ref, Credential: credential})
	if err != nil {
		return fmt.Errorf("control/memorybinding/credentialstore: encode credential: %w", err)
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := secureDirectory(s.root); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(s.root, ".credential-*.tmp")
	if err != nil {
		return fmt.Errorf("control/memorybinding/credentialstore: create temporary credential: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("control/memorybinding/credentialstore: secure temporary credential: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("control/memorybinding/credentialstore: write credential: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("control/memorybinding/credentialstore: sync credential: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("control/memorybinding/credentialstore: close credential: %w", err)
	}
	if err := atomicfile.Replace(temporaryPath, s.path(ref)); err != nil {
		return fmt.Errorf("control/memorybinding/credentialstore: publish credential: %w", err)
	}
	return syncDirectory(s.root)
}

// Get resolves one opaque reference to Host-only credential bytes.
func (s *Store) Get(ctx context.Context, ref string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("control/memorybinding/credentialstore: store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ref = normalizeReference(ref)
	if err := validateReference(ref); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path(ref)
	if err := verifySecureDirectory(s.root); err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("control/memorybinding/credentialstore: credential is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return "", fmt.Errorf("control/memorybinding/credentialstore: credential permissions are not owner-only")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var stored record
	if err := json.Unmarshal(data, &stored); err != nil {
		return "", fmt.Errorf("control/memorybinding/credentialstore: decode credential")
	}
	stored.Reference = normalizeReference(stored.Reference)
	stored.Credential = strings.TrimSpace(stored.Credential)
	if stored.Version != schemaVersion || stored.Reference != ref || stored.Credential == "" {
		return "", fmt.Errorf("control/memorybinding/credentialstore: credential is invalid")
	}
	return stored.Credential, nil
}

func (s *Store) path(ref string) string {
	sum := sha256.Sum256([]byte(normalizeReference(ref)))
	return filepath.Join(s.root, hex.EncodeToString(sum[:])+".json")
}

func normalizeReference(ref string) string {
	return strings.ToLower(strings.TrimSpace(ref))
}

func validateReference(ref string) error {
	const prefix = "memory-issuer:"
	value := strings.TrimPrefix(ref, prefix)
	if value == ref || len(value) != 32 {
		return fmt.Errorf("control/memorybinding/credentialstore: issuer reference is invalid")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("control/memorybinding/credentialstore: issuer reference is invalid")
	}
	return nil
}

func secureDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("control/memorybinding/credentialstore: create credential directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("control/memorybinding/credentialstore: credential directory is not a directory")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("control/memorybinding/credentialstore: secure credential directory: %w", err)
	}
	return nil
}

func verifySecureDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("control/memorybinding/credentialstore: credential directory is not a directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		return fmt.Errorf("control/memorybinding/credentialstore: credential directory permissions are not owner-only")
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil && runtime.GOOS != "windows" && !errors.Is(err, os.ErrInvalid) {
		return fmt.Errorf("control/memorybinding/credentialstore: sync credential directory: %w", err)
	}
	return nil
}
