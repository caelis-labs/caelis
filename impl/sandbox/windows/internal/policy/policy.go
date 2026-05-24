package policy

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/pathutil"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

type NetworkIdentity string

const (
	NetworkOffline NetworkIdentity = "offline"
	NetworkOnline  NetworkIdentity = "online"
)

const CapabilityWrite = "caelis.sandbox.write"

type Policy struct {
	ReadRoots                 []string
	WriteRoots                []string
	DenyReadPaths             []string
	DenyWritePaths            []string
	MaterializeDenyWritePaths []string
	Network                   NetworkIdentity
	CapabilitySIDs            []string
	WriteRootCapabilitySIDs   map[string]string
	FullAccess                bool
}

type Input struct {
	Config      sandbox.Config
	Constraints sandbox.Constraints
	CommandDir  string
}

func Build(input Input) Policy {
	cfg := sandbox.NormalizeConfig(input.Config)
	constraints := sandbox.NormalizeConstraints(input.Constraints)
	if constraints.Permission == "" {
		constraints.Permission = sandbox.PermissionWorkspaceWrite
	}
	if constraints.Network == "" || constraints.Network == sandbox.NetworkInherit {
		constraints.Network = sandbox.NetworkEnabled
	}
	if constraints.Permission == sandbox.PermissionFullAccess {
		return Policy{
			Network:    networkIdentity(constraints.Network),
			FullAccess: true,
		}
	}

	cwd := firstNonEmpty(input.CommandDir, cfg.CWD)
	readRoots := append([]string{}, defaultReadRoots()...)
	readRoots = append(readRoots, cwd, cfg.CWD)
	readRoots = append(readRoots, cfg.ReadableRoots...)

	writeRoots := []string{cwd, cfg.CWD}
	writeRoots = append(writeRoots, cfg.WritableRoots...)

	var denyRead []string
	var denyWrite []string
	var materializeDenyWrite []string
	var hiddenPaths []string
	for _, rule := range constraints.PathRules {
		path := pathutil.Normalize(rule.Path)
		if path == "" {
			continue
		}
		switch rule.Access {
		case sandbox.PathAccessReadOnly:
			readRoots = append(readRoots, path)
		case sandbox.PathAccessReadWrite:
			writeRoots = append(writeRoots, path)
			readRoots = append(readRoots, path)
		case sandbox.PathAccessHidden:
			hiddenPaths = append(hiddenPaths, path)
		}
	}
	for _, root := range writeRoots {
		normalizedRoot := pathutil.Normalize(root)
		if normalizedRoot == "" {
			continue
		}
		denyWrite = append(denyWrite, existingControlDirs(normalizedRoot)...)
		for _, subpath := range cfg.ReadOnlySubpaths {
			if strings.TrimSpace(subpath) == "" {
				continue
			}
			path := filepath.Join(normalizedRoot, subpath)
			denyWrite = append(denyWrite, path)
			materializeDenyWrite = append(materializeDenyWrite, path)
		}
	}
	allowedRoots := append([]string{}, readRoots...)
	allowedRoots = append(allowedRoots, writeRoots...)
	for _, path := range hiddenPaths {
		if !coveredByAnyRoot(path, allowedRoots) {
			continue
		}
		denyRead = append(denyRead, path)
		denyWrite = append(denyWrite, path)
		materializeDenyWrite = append(materializeDenyWrite, path)
	}
	for _, path := range protectedUserSecretRoots() {
		if !coveredByAnyRoot(path, allowedRoots) {
			continue
		}
		denyRead = append(denyRead, path)
		denyWrite = append(denyWrite, path)
	}

	return Policy{
		ReadRoots:                 pathutil.Dedupe(readRoots),
		WriteRoots:                pathutil.Dedupe(writeRoots),
		DenyReadPaths:             pathutil.Dedupe(denyRead),
		DenyWritePaths:            pathutil.Dedupe(denyWrite),
		MaterializeDenyWritePaths: pathutil.Dedupe(materializeDenyWrite),
		Network:                   networkIdentity(constraints.Network),
	}
}

func CommonGlobalPolicy(writeRoots []string) Policy {
	writeRoots = pathutil.CompactCovered(writeRoots)
	return Policy{
		ReadRoots:  pathutil.Dedupe(defaultReadRoots()),
		WriteRoots: writeRoots,
		Network:    NetworkOffline,
	}
}

func networkIdentity(network sandbox.Network) NetworkIdentity {
	if network == sandbox.NetworkEnabled {
		return NetworkOnline
	}
	return NetworkOffline
}

func defaultReadRoots() []string {
	roots := []string{
		`C:\Windows`,
		`C:\Program Files`,
		`C:\Program Files (x86)`,
		`C:\ProgramData`,
	}
	roots = append(roots, profileReadRoots(hostUserProfileRoot())...)
	return pathutil.Dedupe(roots)
}

func hostUserProfileRoot() string {
	if profile := strings.TrimSpace(os.Getenv("USERPROFILE")); profile != "" {
		return profile
	}
	if home, err := os.UserHomeDir(); err == nil {
		return strings.TrimSpace(home)
	}
	return ""
}

func profileReadRoots(profile string) []string {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return nil
	}
	entries, err := os.ReadDir(profile)
	if err != nil {
		return nil
	}
	roots := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if isProfileReadRootExcluded(name) {
			continue
		}
		roots = append(roots, filepath.Join(profile, name))
	}
	return roots
}

func isProfileReadRootExcluded(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return true
	}
	for _, excluded := range []string{".ssh", ".aws", ".azure", ".kube", ".docker", ".gnupg", ".npm", ".config"} {
		if strings.EqualFold(name, excluded) {
			return true
		}
	}
	return false
}

func protectedUserSecretRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	names := []string{".ssh", ".aws", ".azure", ".kube", ".docker", ".gnupg", ".npm", ".config"}
	roots := make([]string, 0, len(names))
	for _, name := range names {
		roots = append(roots, filepath.Join(home, name))
	}
	return roots
}

func existingControlDirs(root string) []string {
	root = pathutil.Normalize(root)
	if root == "" {
		return nil
	}
	names := []string{".git", ".codex", ".agents"}
	paths := make([]string, 0, len(names))
	for _, name := range names {
		path := filepath.Join(root, name)
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			paths = append(paths, path)
		}
	}
	return paths
}

func coveredByAnyRoot(path string, roots []string) bool {
	for _, root := range roots {
		if pathutil.IsUnder(path, root) {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
