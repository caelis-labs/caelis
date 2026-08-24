package policy

import "strings"

const (
	ProfileWorkspaceWrite = "workspace-write"

	MetadataPolicyProfile    = "policy_profile"
	MetadataLegacyPolicyMode = "policy_mode"
	MetadataExtraReadRoots   = "policy_extra_read_roots"
	MetadataWritableRoots    = "policy_writable_roots"
	MetadataSandboxPolicy    = "runtime_sandbox_policy"
)

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
