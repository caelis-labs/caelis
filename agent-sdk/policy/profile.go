package policy

import "strings"

const (
	// ProfileWorkspaceWrite is the default name for a configured policy registry.
	ProfileWorkspaceWrite = "workspace-write"
	// ProfileDangerFullAccess identifies a privileged mode that the Host must register explicitly.
	ProfileDangerFullAccess = "danger-full-access"

	MetadataPolicyProfile    = "policy_profile"
	MetadataLegacyPolicyMode = "policy_mode"
	MetadataExtraReadRoots   = "policy_extra_read_roots"
	MetadataWritableRoots    = "policy_writable_roots"
	MetadataSandboxPolicy    = "runtime_sandbox_policy"
)

// NormalizeProfileName maps known profile aliases to their canonical names and
// preserves custom names. Omitted profiles and legacy approval modes yield an
// empty name.
func NormalizeProfileName(profile string) string {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "", "manual", "auto", "auto-review", "auto_review", "autoreview":
		return ""
	case "default", "plan", "full_control", "full_access", "workspace-write", "workspace_write", "workspacewrite":
		return ProfileWorkspaceWrite
	default:
		return strings.TrimSpace(profile)
	}
}

// NormalizeModeName resolves a profile name for a registry lookup, defaulting
// omitted profiles and legacy approval modes to workspace-write.
func NormalizeModeName(profile string) string {
	if name := NormalizeProfileName(profile); name != "" {
		return name
	}
	return ProfileWorkspaceWrite
}
