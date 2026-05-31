package policy

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/windows/internal/pathutil"
)

type NetworkIdentity string

const (
	NetworkOffline NetworkIdentity = "offline"
	NetworkOnline  NetworkIdentity = "online"
)

const CapabilityWrite = "caelis.sandbox.write"

type Policy struct {
	ReadRoots               []string
	WriteRoots              []string
	DenyReadPaths           []string
	DenyWritePaths          []string
	Network                 NetworkIdentity
	CapabilitySIDs          []string
	WriteRootCapabilitySIDs map[string]string
	FullAccess              bool
}

type Input struct {
	Config      sandbox.Config
	Constraints sandbox.Constraints
	CommandDir  string
}

func Build(input Input) Policy {
	cfg := sandbox.NormalizeConfig(input.Config)
	constraints := sandbox.NormalizeConstraints(input.Constraints)
	if constraints.Permission == "" || constraints.Permission == sandbox.PermissionDefault {
		constraints.Permission = sandbox.PermissionWorkspaceWrite
	}
	if constraints.Permission == sandbox.PermissionFullAccess {
		return Policy{Network: effectiveWindowsNetwork(constraints.Network), FullAccess: true}
	}

	cwd := firstNonEmpty(input.CommandDir, cfg.CWD)
	writeRoots := []string{cwd, cfg.CWD}
	writeRoots = append(writeRoots, cfg.WritableRoots...)
	for _, rule := range constraints.PathRules {
		if rule.Access != sandbox.PathAccessReadWrite {
			continue
		}
		if path := pathutil.Normalize(rule.Path); path != "" {
			writeRoots = append(writeRoots, path)
		}
	}
	writeRoots = pathutil.Dedupe(writeRoots)

	var denyWrite []string
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
			if _, err := os.Stat(path); err != nil {
				continue
			}
			denyWrite = append(denyWrite, path)
		}
	}

	return Policy{
		WriteRoots:     writeRoots,
		DenyWritePaths: pathutil.Dedupe(denyWrite),
		Network:        effectiveWindowsNetwork(constraints.Network),
	}
}

func CommonGlobalPolicy(writeRoots []string) Policy {
	return Policy{
		WriteRoots: pathutil.CompactCovered(writeRoots),
		Network:    effectiveWindowsNetwork(""),
	}
}

func effectiveWindowsNetwork(_ sandbox.Network) NetworkIdentity {
	// Windows restricted-token sandboxing does not expose a network-control
	// capability. NetworkDisabled remains caller intent outside this policy, and
	// restricted-token execution uses the online identity.
	return NetworkOnline
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
