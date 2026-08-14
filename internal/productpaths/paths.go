// Package productpaths owns process-independent Caelis product paths.
package productpaths

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/caelis-labs/caelis/internal/version"
)

const defaultDevProfile = "default"

// DefaultStoreDir returns the isolated default Store for the current build.
// Explicit --store-dir and CAELIS_STORE_DIR selection remains the caller's
// responsibility and always takes precedence over this default.
func DefaultStoreDir(cwd string) string {
	return DefaultStoreDirForBuild(cwd, version.BuildInfo())
}

// DefaultStoreDirForBuild resolves a default Store for a known build identity.
func DefaultStoreDirForBuild(cwd string, build version.Info) string {
	root := strings.TrimSpace(cwd)
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		root = home
	}
	if root == "" {
		root = "."
	}
	if build.BuildKind == version.BuildKindRelease {
		return filepath.Join(root, ".caelis")
	}
	return filepath.Join(root, ".caelis-dev", defaultDevProfile)
}

// ServiceRuntimeDir groups ephemeral local service coordination state inside
// one Store without mixing it into the Store root.
func ServiceRuntimeDir(storeDir string) string {
	return filepath.Join(filepath.Clean(storeDir), "runtime", "service")
}

// ServiceLogDir groups service logs inside one Store.
func ServiceLogDir(storeDir string) string {
	return filepath.Join(filepath.Clean(storeDir), "logs")
}

// ServiceInstallDir returns the platform application-data directory used to
// stage stable full caelis binaries for one canonical Store.
func ServiceInstallDir(storeDir string) (string, error) {
	storeID, err := storeIdentity(storeDir)
	if err != nil {
		return "", err
	}
	root, err := userDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "Caelis", "service", storeID), nil
}

func storeIdentity(storeDir string) (string, error) {
	storeDir = strings.TrimSpace(storeDir)
	if storeDir == "" {
		return "", errors.New("productpaths: Store directory is required")
	}
	canonical, err := filepath.Abs(storeDir)
	if err != nil {
		return "", fmt.Errorf("productpaths: resolve Store directory: %w", err)
	}
	sum := sha256.Sum256([]byte(filepath.Clean(canonical)))
	return hex.EncodeToString(sum[:8]), nil
}

func userDataDir() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", errors.New("productpaths: resolve macOS application data directory")
		}
		return filepath.Join(home, "Library", "Application Support"), nil
	case "windows":
		if root := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); root != "" {
			return root, nil
		}
		return "", errors.New("productpaths: LOCALAPPDATA is unavailable")
	default:
		if root := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); root != "" {
			return root, nil
		}
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", errors.New("productpaths: resolve user data directory")
		}
		return filepath.Join(home, ".local", "share"), nil
	}
}
