// Package presets provides built-in policy mode implementations for the Agent SDK.
//
// This package owns the reusable preset registry and workspace-write policy
// mode. Apps may register additional modes through agent-sdk/policy.Registry.
//
// Package layout:
//
//   - presets.go: mode registry and shared decision helpers
//   - filesystem_policy.go: READ/WRITE/PATCH path authorization
//   - command_policy.go: RUN_COMMAND classification (machine deny / approval)
//   - remote_script.go: remote script flows in POSIX and PowerShell commands
//   - shell_parse.go: shell tokenization helpers shared by command and git policy
//   - git_policy.go: Git subcommand classification
//
// workspace-write classification rules:
//
//   - The assembled Tool set owns capability admission. This preset is not a
//     second Tool-name allowlist; calls without a maintained risk classifier
//     are allowed under the default workspace constraints.
//   - Hard deny is reserved for machine-level catastrophic operations
//     (system/home root recursive deletes and device wipes).
//   - Built-in filesystem writes outside allowed roots ask for approval with
//     exact path grants under sandbox constraints.
//   - Destructive VCS operations, remote script execution (including PowerShell
//     download-to-Invoke-Expression flows), and out-of-root recursive deletes
//     require approval on the requested execution route.
//     Approval alone does not expand sandbox filesystem or network permissions.
package presets
