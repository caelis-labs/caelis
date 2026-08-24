package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	backendpolicy "github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/pathutil"
)

type NetworkIdentity string

const (
	NetworkOffline NetworkIdentity = "offline"
	NetworkOnline  NetworkIdentity = "online"
)

const CapabilityWrite = "caelis.sandbox.write"

type Policy struct {
	WriteRoots              []string
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

func Build(input Input) (Policy, error) {
	cfg := sandbox.NormalizeConfig(input.Config)
	constraints := sandbox.NormalizeConstraints(input.Constraints)
	if constraints.Permission == "" || constraints.Permission == sandbox.PermissionDefault {
		constraints.Permission = sandbox.PermissionWorkspaceWrite
	}
	if constraints.Permission == sandbox.PermissionFullAccess {
		return Policy{Network: effectiveWindowsNetwork(constraints.Network), FullAccess: true}, nil
	}

	cwd := firstNonEmpty(input.CommandDir, cfg.CWD)
	writeRoots := append([]string(nil), cfg.WritableRoots...)
	writeOverrides := []string{}
	for _, rule := range constraints.PathRules {
		if rule.Access != sandbox.PathAccessReadWrite {
			continue
		}
		if path := pathutil.Normalize(rule.Path); path != "" {
			writeRoots = append(writeRoots, path)
			writeOverrides = append(writeOverrides, path)
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
			if strings.TrimSpace(subpath) == "" || (!filepath.IsAbs(subpath) && filepath.Clean(subpath) == ".git") {
				continue
			}
			path := filepath.Join(normalizedRoot, subpath)
			if _, err := os.Stat(path); err != nil {
				continue
			}
			denyWrite = append(denyWrite, path)
		}
	}
	gitPaths, err := backendpolicy.GitMetadataPaths(cwd, writeRoots, writeOverrides)
	if err != nil {
		return Policy{}, fmt.Errorf("resolve protected Git metadata: %w", err)
	}
	for _, path := range gitPaths {
		if _, err := os.Lstat(path); err == nil {
			denyWrite = append(denyWrite, path)
		} else if !os.IsNotExist(err) {
			return Policy{}, fmt.Errorf("inspect protected Git metadata %s: %w", path, err)
		}
	}

	return Policy{
		WriteRoots:     writeRoots,
		DenyWritePaths: pathutil.Dedupe(denyWrite),
		Network:        effectiveWindowsNetwork(constraints.Network),
	}, nil
}

func CommonGlobalPolicy(writeRoots []string) Policy {
	return Policy{
		WriteRoots: pathutil.CompactCovered(writeRoots),
		Network:    effectiveWindowsNetwork(""),
	}
}

func effectiveWindowsNetwork(_ sandbox.Network) NetworkIdentity {
	// Windows restricted-token sandboxing is online-only today. NetworkDisabled
	// records caller intent, but offline enforcement is not implemented and
	// therefore falls back to the same online execution path.
	return NetworkOnline
}

func existingControlDirs(root string) []string {
	root = pathutil.Normalize(root)
	if root == "" {
		return nil
	}
	paths := make([]string, 0, 2)
	for _, name := range []string{".codex", ".agents"} {
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
