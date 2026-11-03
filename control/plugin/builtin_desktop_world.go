package plugin

import (
	"archive/zip"
	"bytes"
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
)

// The product pin records each platform's archive and build source separately.
// Only the current platform's complete official ZIP is linked into the product.
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

func desktopWorldBundle() (*zip.Reader, builtinPlatform, string, error) {
	if builtinDesktopWorldTarget == "" {
		return nil, builtinPlatform{}, "", fmt.Errorf("builtin desktop-world is unsupported on %s/%s (requires macOS arm64 or Windows x64)", runtime.GOOS, runtime.GOARCH)
	}
	var release struct {
		Schema    int               `json:"schema"`
		Version   string            `json:"version"`
		Platforms []builtinPlatform `json:"platforms"`
	}
	if err := json.Unmarshal(builtinDesktopWorldPin, &release); err != nil || release.Schema != 1 || release.Version == "" {
		return nil, builtinPlatform{}, "", fmt.Errorf("invalid builtin desktop-world release manifest")
	}
	for _, platform := range release.Platforms {
		if platform.Target != builtinDesktopWorldTarget {
			continue
		}
		sum := sha256.Sum256(builtinDesktopWorldArchive)
		if hex.EncodeToString(sum[:]) != platform.ArchiveHash {
			return nil, platform, "", fmt.Errorf("builtin desktop-world archive checksum mismatch")
		}
		reader, err := zip.NewReader(bytes.NewReader(builtinDesktopWorldArchive), int64(len(builtinDesktopWorldArchive)))
		if err != nil {
			return nil, platform, "", err
		}
		return reader, platform, release.Version, nil
	}
	return nil, builtinPlatform{}, "", fmt.Errorf("builtin desktop-world platform missing from release manifest")
}

func materializeDesktopWorld(ctx context.Context, storeDir string) (string, error) {
	reader, platform, version, err := desktopWorldBundle()
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
	root := filepath.Join(parent, platform.ArchiveHash)
	release, err := acquireManagedPluginCacheOperation(ctx, root)
	if err != nil {
		return "", err
	}
	defer release()
	if _, err := os.Lstat(root); err == nil {
		return root, verifyDesktopWorldBundle(root, reader, platform, version)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	tmp, err := os.MkdirTemp(parent, ".staging-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	prefix := strings.TrimSuffix(platform.Archive, ".zip") + "/"
	for _, entry := range reader.File {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		rel, err := builtinEntryPath(entry, prefix)
		if err != nil {
			return "", err
		}
		if rel == "" {
			continue
		}
		dest := filepath.Join(tmp, rel)
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(dest, 0o700); err != nil {
				return "", err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return "", err
		}
		if err := extractBuiltinEntry(entry, dest); err != nil {
			return "", err
		}
	}
	if err := verifyDesktopWorldBundle(tmp, reader, platform, version); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, root); err != nil {
		// Another Host may have published the same bytes. Never replace or repair
		// an existing root; readers can hold it across upgrades and rollbacks.
		if verifyErr := verifyDesktopWorldBundle(root, reader, platform, version); verifyErr != nil {
			return "", err
		}
	}
	return root, nil
}

func builtinEntryPath(entry *zip.File, prefix string) (string, error) {
	name, ok := strings.CutPrefix(entry.Name, prefix)
	if !ok || entry.Mode()&os.ModeSymlink != 0 || (!entry.FileInfo().IsDir() && !entry.Mode().IsRegular()) || entry.UncompressedSize64 > 128<<20 {
		return "", fmt.Errorf("invalid builtin archive entry %q", entry.Name)
	}
	if name == "" {
		return "", nil
	}
	return filepath.Localize(strings.TrimSuffix(name, "/"))
}

func extractBuiltinEntry(entry *zip.File, dest string) error {
	src, err := entry.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	mode := fs.FileMode(0o600) | entry.Mode().Perm()&0o111
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

func verifyDesktopWorldBundle(root string, reader *zip.Reader, platform builtinPlatform, version string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("builtin package root is not a real directory")
	}
	prefix := strings.TrimSuffix(platform.Archive, ".zip") + "/"
	expected := map[string]bool{".": true}
	for _, entry := range reader.File {
		rel, err := builtinEntryPath(entry, prefix)
		if err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		expected[rel] = true
		for parent := filepath.Dir(rel); parent != "."; parent = filepath.Dir(parent) {
			expected[parent] = true
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		path := filepath.Join(root, rel)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o111 != entry.Mode().Perm()&0o111) {
			return fmt.Errorf("invalid builtin file mode: %s", rel)
		}
		src, err := entry.Open()
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, err = io.Copy(hash, src)
		_ = src.Close()
		if err != nil {
			return err
		}
		actual, err := os.Open(path)
		if err != nil {
			return err
		}
		actualHash := sha256.New()
		_, err = io.Copy(actualHash, actual)
		_ = actual.Close()
		if err != nil {
			return err
		}
		if !bytes.Equal(hash.Sum(nil), actualHash.Sum(nil)) {
			return fmt.Errorf("builtin file checksum mismatch: %s", rel)
		}
	}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if !expected[rel] || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected builtin package entry: %s", rel)
		}
		return nil
	}); err != nil {
		return err
	}
	var build struct {
		Version string             `json:"version"`
		Target  string             `json:"target"`
		Source  builtinBuildSource `json:"source"`
		Binary  struct {
			SHA string `json:"sha256"`
		} `json:"binary"`
	}
	data, err := os.ReadFile(filepath.Join(root, "BUILD.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &build); err != nil {
		return err
	}
	if build.Version != version || build.Target != platform.Target || build.Source != platform.Source || build.Binary.SHA != platform.BinaryHash {
		return fmt.Errorf("builtin desktop-world build provenance mismatch")
	}
	return nil
}

// ProductContributions assembles fixed Core capabilities separately from optional
// user plugins. It uses the ordinary package parser and per-Session MCP paths.
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
	out, err := ResolveContributions([]Config{{ID: "desktop-world", Root: root, Enabled: true}}, paths)
	if err == nil {
		out.Sources[0].Status = "builtin"
	}
	return out, err
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

// WithProductContributions selects one paired MCP/Skill capability: explicit
// Session package, enabled Host override, then the embedded fallback. Defaults
// come from the same activation snapshot as other Host configuration.
func WithProductContributions(optional Contributions, paths RuntimePaths, defaults ...Config) (Contributions, error) {
	for _, source := range optional.Sources {
		if source.ID == "desktop-world" {
			return optional, validateProductPair(optional)
		}
	}
	fixed, selected, err := productOverride(defaults, paths)
	if !selected && err == nil {
		fixed, err = ProductContributions(paths)
	}
	if err != nil {
		return Contributions{}, err
	}
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
