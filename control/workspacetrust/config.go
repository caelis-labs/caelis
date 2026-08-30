// Package workspacetrust owns the persisted decision that allows a workspace
// to contribute project-scoped configuration with host-side effects.
package workspacetrust

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// Level is the persisted trust decision for one canonical workspace.
type Level string

const (
	// Unknown means no decision has been persisted for the workspace.
	Unknown Level = "unknown"
	// Trusted allows project-scoped configuration to participate in Runtime assembly.
	Trusted Level = "trusted"
	// Untrusted keeps project-scoped configuration disabled without prompting again.
	Untrusted Level = "untrusted"
)

// Configuration maps canonical workspace paths to explicit trust decisions.
// Unknown is represented by an absent entry and is never persisted.
type Configuration map[string]Level

// Decided reports whether the level is an explicit persisted choice.
func (l Level) Decided() bool {
	return l == Trusted || l == Untrusted
}

// Lookup returns the exact decision for workspace. Parent-directory grants do
// not apply to child or sibling workspaces.
func Lookup(in Configuration, workspace string) Level {
	key := NormalizeKey(workspace)
	if key == "" {
		return Unknown
	}
	if level := in[key]; level.Decided() {
		return level
	}
	return Unknown
}

// Set returns a detached configuration with one explicit workspace decision.
func Set(in Configuration, workspace string, level Level) (Configuration, error) {
	key := NormalizeKey(workspace)
	if key == "" || !filepath.IsAbs(key) {
		return nil, fmt.Errorf("workspacetrust: canonical workspace path is required")
	}
	if !level.Decided() {
		return nil, fmt.Errorf("workspacetrust: trust level must be %q or %q", Trusted, Untrusted)
	}
	out := maps.Clone(in)
	if out == nil {
		out = make(Configuration)
	}
	out[key] = level
	return Normalize(out), nil
}

// ValidateIdentities rejects empty, relative, or colliding workspace keys
// before Normalize could fold them.
func ValidateIdentities(in Configuration) error {
	seen := make(map[string]struct{}, len(in))
	for _, raw := range slices.Sorted(maps.Keys(in)) {
		key := NormalizeKey(raw)
		if key == "" || !filepath.IsAbs(key) {
			return fmt.Errorf("workspacetrust: workspace path %q must be absolute", raw)
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("workspacetrust: duplicate workspace path %q", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// Validate checks that every persisted entry is an explicit trust decision.
func Validate(in Configuration) error {
	for _, workspace := range slices.Sorted(maps.Keys(in)) {
		if !in[workspace].Decided() {
			return fmt.Errorf("workspacetrust: workspace %q has invalid trust level %q", workspace, in[workspace])
		}
	}
	return nil
}

// Normalize returns a detached deterministic configuration. Callers loading
// untrusted input must run ValidateIdentities before normalization.
func Normalize(in Configuration) Configuration {
	if len(in) == 0 {
		return nil
	}
	out := make(Configuration, len(in))
	for _, raw := range slices.Sorted(maps.Keys(in)) {
		key := NormalizeKey(raw)
		level := in[raw]
		if key == "" {
			continue
		}
		if _, exists := out[key]; !exists {
			out[key] = level
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// NormalizeKey performs only stable string normalization. Filesystem identity
// resolution remains owned by the Host workspace resolver before persistence.
func NormalizeKey(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return ""
	}
	workspace = filepath.Clean(workspace)
	if workspace == "." {
		return ""
	}
	return workspace
}
