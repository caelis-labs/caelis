package plugin

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// The product pin records each platform's archive and build source separately.
// Only the current platform's complete official archive is linked into the product.
//
//go:embed builtin/desktop-world.json
var builtinDesktopWorldPin []byte

type builtinBuildSource struct {
	Commit string `json:"commit"`
	Tree   string `json:"tree"`
	Lock   string `json:"cargo_lock_sha256"`
}

type builtinPlatform struct {
	Target      string             `json:"target"`
	Archive     string             `json:"archive"`
	ArchiveHash string             `json:"archive_sha256"`
	BinaryHash  string             `json:"binary_sha256"`
	Source      builtinBuildSource `json:"source"`
}

type desktopWorldRelease struct {
	archive  builtinArchive
	platform builtinPlatform
	version  string
	files    []builtinRuntimeFile
}

type builtinRuntimeFile struct {
	path string
	mode fs.FileMode
	hash [sha256.Size]byte
}

// The executable's embedded bytes and pin are immutable. Only their expected
// digests are reused; every activation still verifies its on-disk files.
var desktopWorldBundle = sync.OnceValues(loadDesktopWorldRelease)

func loadDesktopWorldRelease() (desktopWorldRelease, error) {
	if builtinDesktopWorldTarget == "" {
		return desktopWorldRelease{}, fmt.Errorf("builtin desktop-world is unsupported on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	var release struct {
		Schema    int               `json:"schema"`
		Version   string            `json:"version"`
		Platforms []builtinPlatform `json:"platforms"`
	}
	if err := json.Unmarshal(builtinDesktopWorldPin, &release); err != nil || release.Schema != 1 || release.Version == "" {
		return desktopWorldRelease{}, fmt.Errorf("invalid builtin desktop-world release manifest")
	}
	for _, platform := range release.Platforms {
		if platform.Target != builtinDesktopWorldTarget {
			continue
		}
		sum := sha256.Sum256(builtinDesktopWorldArchive)
		if hex.EncodeToString(sum[:]) != platform.ArchiveHash {
			return desktopWorldRelease{}, fmt.Errorf("builtin desktop-world archive checksum mismatch")
		}
		bundle := desktopWorldRelease{
			archive:  builtinArchive{name: platform.Archive, data: builtinDesktopWorldArchive},
			platform: platform, version: release.Version,
		}
		if err := bundle.archive.walk(func(rel string, mode fs.FileMode, src io.Reader) error {
			if mode.IsDir() || !desktopWorldRuntimeFile(filepath.ToSlash(rel)) {
				return nil
			}
			hash, err := builtinFileHash(src)
			if err != nil {
				return err
			}
			bundle.files = append(bundle.files, builtinRuntimeFile{path: rel, mode: mode, hash: hash})
			return nil
		}); err != nil {
			return desktopWorldRelease{}, err
		}
		return bundle, nil
	}
	return desktopWorldRelease{}, fmt.Errorf("builtin desktop-world platform missing from release manifest")
}

func materializeDesktopWorld(ctx context.Context, storeDir string) (string, error) {
	bundle, err := desktopWorldBundle()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(storeDir)
	if err != nil {
		return "", err
	}
	for _, part := range []string{"builtin-plugins", "desktop-world"} {
		parent = filepath.Join(parent, part)
		if err := os.Mkdir(parent, 0o700); err != nil && !os.IsExist(err) {
			return "", err
		}
		info, err := os.Lstat(parent)
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("builtin cache must be a real private directory")
		}
	}
	root := filepath.Join(parent, bundle.platform.ArchiveHash)
	release, err := acquireManagedPluginCacheOperation(ctx, root)
	if err != nil {
		return "", err
	}
	defer release()
	if _, err := os.Lstat(root); err == nil {
		return root, verifyDesktopWorldBundle(root, bundle.files)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	tmp, err := os.MkdirTemp(parent, ".staging-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := bundle.archive.walk(func(rel string, mode fs.FileMode, src io.Reader) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dest := filepath.Join(tmp, rel)
		if mode.IsDir() {
			return os.MkdirAll(dest, 0o700)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return err
		}
		return extractBuiltinEntry(src, mode, dest)
	}); err != nil {
		return "", err
	}
	if err := verifyDesktopWorldBundle(tmp, bundle.files); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, root); err != nil {
		// Another Host may have published the same bytes. Never replace or repair
		// an existing root; readers can hold it across upgrades and rollbacks.
		if verifyErr := verifyDesktopWorldBundle(root, bundle.files); verifyErr != nil {
			return "", err
		}
	}
	return root, nil
}

func extractBuiltinEntry(src io.Reader, mode fs.FileMode, dest string) error {
	mode = 0o600 | mode.Perm()&0o111
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, src)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func verifyDesktopWorldBundle(root string, files []builtinRuntimeFile) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("builtin package root is not a real directory")
	}
	for _, file := range files {
		path := filepath.Join(root, file.path)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o111 != file.mode.Perm()&0o111) {
			return fmt.Errorf("invalid builtin file mode: %s", file.path)
		}
		actual, err := os.Open(path)
		if err != nil {
			return err
		}
		hash, err := builtinFileHash(actual)
		_ = actual.Close()
		if err != nil {
			return err
		}
		if hash != file.hash {
			return fmt.Errorf("builtin file checksum mismatch: %s", file.path)
		}
	}
	return nil
}

func builtinFileHash(r io.Reader) ([sha256.Size]byte, error) {
	hash := sha256.New()
	_, err := io.Copy(hash, r)
	var digest [sha256.Size]byte
	hash.Sum(digest[:0])
	return digest, err
}

// Runtime validation covers executable, configuration and Skill inputs. Release
// provenance is verified on the embedded archive; unrelated files and caches do
// not participate in package activation.
func desktopWorldRuntimeFile(name string) bool {
	return name == "plugin.json" || name == "mcp.json" || name == ".mcp.json" ||
		name == ".claude-plugin/plugin.json" || strings.HasPrefix(name, "bin/") || strings.HasPrefix(name, "skills/")
}

// ProductContributions assembles fixed Core capabilities separately from optional
// user plugins. It uses the ordinary package parser and the product Store for default MCP data.
// Unsupported platforms retain a diagnostic source without starting a process.
func ProductContributions(paths RuntimePaths) (Contributions, error) {
	if builtinDesktopWorldTarget == "" {
		return Contributions{Sources: []Source{{ID: "desktop-world", Status: "unsupported on " + runtime.GOOS + "/" + runtime.GOARCH}}}, nil
	}
	cacheDir := paths.ProductStoreDir
	if cacheDir == "" {
		cacheDir = paths.StoreDir
	}
	if cacheDir == "" {
		return Contributions{}, fmt.Errorf("builtin desktop-world requires the Core Store")
	}
	root, err := materializeDesktopWorld(context.Background(), cacheDir)
	if err != nil {
		return Contributions{}, fmt.Errorf("builtin desktop-world: %w", err)
	}
	paths.StoreDir = cacheDir
	out, err := ResolveContributions([]Config{{ID: "desktop-world", Root: root, Enabled: true}}, paths)
	if err == nil {
		out.Sources[0].Status = "builtin"
		if runtime.GOOS == "linux" {
			// Only the bundled local service receives the Host's desktop session.
			// The ordinary clean MCP environment still excludes unrelated variables.
			for i := range out.MCPServerSpecs {
				spec := &out.MCPServerSpecs[i]
				if spec.Env == nil {
					spec.Env = make(map[string]string)
				}
				for _, key := range []string{
					"DISPLAY", "XAUTHORITY", "XDG_SESSION_TYPE", "WAYLAND_DISPLAY",
					"DBUS_SESSION_BUS_ADDRESS", "XDG_DATA_HOME", "XDG_DATA_DIRS",
				} {
					if value, ok := os.LookupEnv(key); ok {
						spec.Env[key] = value
					}
				}
			}
		}
	}
	return selectDesktopToolset(out), err
}

// IsProductPlugin reports the fixed capability's stable identity, including
// user aliases. It selects the product-default path, not a reserved package.
func IsProductPlugin(config Config) bool {
	if strings.EqualFold(strings.TrimSpace(config.ID), "desktop-world") {
		return true
	}
	// Preserve registration identity if an aliased override later becomes
	// unreadable; selection must report it rather than activate the fallback.
	if usesManifestIdentity(config.Kind) && strings.EqualFold(strings.TrimSpace(config.Name), "desktop-world") {
		return true
	}
	installed, err := ParsePluginAvailable(config.Root)
	return err == nil && localPluginID(installed) == "desktop-world"
}

// WithProductContributions selects one paired MCP/Skill package. Explicit Session
// packages win; otherwise comparable Host and embedded versions use the newer
// package, with ties and opaque versions retaining the Host override. Defaults
// come from the same activation snapshot as other Host configuration.
func WithProductContributions(optional Contributions, paths RuntimePaths, defaults ...Config) (Contributions, error) {
	for _, source := range optional.Sources {
		if source.ID == "desktop-world" {
			if err := validateProductPair(optional); err != nil {
				return Contributions{}, err
			}
			return selectDesktopToolset(optional), nil
		}
	}
	fixed, selected, err := productOverride(defaults, paths)
	if !selected && err == nil {
		fixed, err = ProductContributions(paths)
	}
	if err != nil {
		return Contributions{}, err
	}
	fixed = selectDesktopToolset(fixed)
	optional.Sources = append(fixed.Sources, optional.Sources...)
	optional.SkillBundles = append(fixed.SkillBundles, optional.SkillBundles...)
	optional.MCPServerSpecs = append(fixed.MCPServerSpecs, optional.MCPServerSpecs...)
	return optional, nil
}

// OptionalUserConfigs routes DTW through the single product-default selection
// path without changing the user's registry, files or enabled/disabled records.
func OptionalUserConfigs(configs []Config) []Config {
	var selected []Config
	for _, configured := range configs {
		if !IsProductPlugin(configured) {
			selected = append(selected, configured)
		}
	}
	return selected
}
