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
//   - shell_parse.go: shell tokenization helpers shared by command and git policy
//   - git_policy.go: Git subcommand classification
//
// workspace-write classification rules:
//
//   - Hard deny is reserved for machine-level catastrophic operations
//     (system/home root recursive deletes, device wipes, remote pipe execution).
//   - Built-in filesystem writes outside allowed roots ask for approval with
//     exact path grants under sandbox constraints.
//   - Destructive VCS operations, Git metadata writes, and out-of-root recursive
//     deletes require approval instead of deny-and-retry tutorials.
package presets
