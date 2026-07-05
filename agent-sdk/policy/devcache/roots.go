package devcache

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// WritableRoots returns host-side developer cache roots that are safe to grant
// across workspaces for sandboxed dependency/build commands.
func WritableRoots() []string {
	roots := []string{}
	appendRoot := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" || path == "off" {
			return
		}
		roots = appendNonEmpty(roots, path)
	}
	appendEnvPath := func(key string) {
		appendRoot(os.Getenv(key))
	}

	home, _ := os.UserHomeDir()
	cacheRoot := userCacheRoot(home)
	appendRoot(cacheRoot)
	if runtime.GOOS == "darwin" && strings.TrimSpace(home) != "" {
		appendRoot(filepath.Join(home, "Library", "Caches"))
	}

	appendEnvPath("GOCACHE")
	appendEnvPath("GOMODCACHE")
	for _, gopath := range goPathRoots(home) {
		appendRoot(filepath.Join(gopath, "pkg", "mod"))
	}

	appendEnvPath("npm_config_cache")
	appendEnvPath("NPM_CONFIG_CACHE")
	appendEnvPath("YARN_CACHE_FOLDER")
	appendEnvPath("PIP_CACHE_DIR")
	appendEnvPath("UV_CACHE_DIR")

	if strings.TrimSpace(home) != "" {
		appendRoot(filepath.Join(home, ".npm"))
		appendRoot(filepath.Join(home, ".cargo", "registry"))
		appendRoot(filepath.Join(home, ".cargo", "git"))
		appendRoot(filepath.Join(home, ".m2", "repository"))
		appendRoot(filepath.Join(home, ".gradle", "caches"))
		appendRoot(filepath.Join(home, ".gradle", "wrapper"))
		appendRoot(filepath.Join(home, ".nuget", "packages"))
		appendRoot(filepath.Join(home, ".bun", "install", "cache"))
		appendRoot(filepath.Join(home, ".pnpm-store"))
		appendRoot(filepath.Join(home, ".local", "share", "pnpm", "store"))
	}
	if cacheRoot != "" {
		appendRoot(filepath.Join(cacheRoot, "go-build"))
		appendRoot(filepath.Join(cacheRoot, "pip"))
		appendRoot(filepath.Join(cacheRoot, "uv"))
		appendRoot(filepath.Join(cacheRoot, "yarn"))
		appendRoot(filepath.Join(cacheRoot, "node-gyp"))
		appendRoot(filepath.Join(cacheRoot, "pnpm"))
	}
	if cargoHome := strings.TrimSpace(os.Getenv("CARGO_HOME")); cargoHome != "" {
		appendRoot(filepath.Join(cargoHome, "registry"))
		appendRoot(filepath.Join(cargoHome, "git"))
	}
	if gradleHome := strings.TrimSpace(os.Getenv("GRADLE_USER_HOME")); gradleHome != "" {
		appendRoot(filepath.Join(gradleHome, "caches"))
		appendRoot(filepath.Join(gradleHome, "wrapper"))
	}
	return roots
}

func userCacheRoot(home string) string {
	if cache := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME")); cache != "" {
		return filepath.Clean(cache)
	}
	if cache, err := os.UserCacheDir(); err == nil && strings.TrimSpace(cache) != "" {
		return filepath.Clean(cache)
	}
	if strings.TrimSpace(home) == "" {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Caches")
	default:
		return filepath.Join(home, ".cache")
	}
}

func goPathRoots(home string) []string {
	raw := strings.TrimSpace(os.Getenv("GOPATH"))
	if raw == "" {
		if strings.TrimSpace(home) == "" {
			return nil
		}
		raw = filepath.Join(home, "go")
	}
	roots := []string{}
	for _, path := range filepath.SplitList(raw) {
		if path = strings.TrimSpace(path); path != "" {
			roots = appendNonEmpty(roots, path)
		}
	}
	return roots
}

func appendNonEmpty(dst []string, values ...string) []string {
	for _, one := range values {
		if trimmed := strings.TrimSpace(one); trimmed != "" {
			dst = append(dst, filepath.Clean(trimmed))
		}
	}
	return dst
}
