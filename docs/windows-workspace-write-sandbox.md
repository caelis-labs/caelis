# Windows Workspace-Write Sandbox Simplification

This document defines the current Windows sandbox direction for Caelis. It
supersedes `docs/windows-elevated-sandbox.md` as the implementation target. The
old elevated design is useful only as legacy context for cleanup and migration.

## Decision

Windows sandboxing should provide one practical boundary:

- Commands run as the current Windows user.
- Commands can read anything the current user can read.
- Commands can use the network normally.
- Commands can write only to the workspace and explicitly configured writable
  roots.

The implementation should be a direct current-user `WRITE_RESTRICTED` token
backend with synthetic write SIDs and ACLs on writable roots. It should not use
local sandbox users, an elevated setup service, a command runner account,
Windows Firewall rules, WFP, profile hiding, or read ACL machinery.

The default Windows route is the new workspace-write sandbox after the backend
has proven usable. Explicit host execution remains available and must not pay
any Windows sandbox startup, setup, status, or cleanup cost.

## Product Goals

- Preserve normal command behavior. PowerShell, cmd, git, go, npm, Python,
  TTY sessions, stdin/stdout/stderr, timeouts, and cancellation should work the
  same way as host execution unless a write escapes the allowed roots.
- Keep sandbox overhead effectively invisible. Warm sandboxed commands should
  add only a small constant overhead over host process creation. The backend must
  avoid recursive workspace scans and broad ACL refreshes.
- Fail closed for the sandbox route. If a command explicitly asks for the Windows
  sandbox and the write boundary cannot be prepared, do not fall back to host.
- Keep Windows-specific complexity isolated under Windows build tags and
  Windows implementation packages.
- Remove TUI `/sandbox` lifecycle commands. Command execution repairs workspace
  ACL state lazily, while CLI `caelis sandbox reset` and `caelis sandbox clean`
  discover and remove new and legacy sandbox artifacts.

## Non-Goals

- No read isolation.
- No hidden-path read enforcement.
- No network isolation or proxy enforcement.
- No Windows Sandbox VM, WSL, Docker, or container dependency.
- No offline/online sandbox identities.
- No local user or group creation for normal execution.
- No DPAPI-stored sandbox passwords.
- No WFP or Windows Firewall policy.
- No elevated helper in normal setup or command execution.
- No TUI startup prompt, startup setup check, or blocking readiness warning.
- No generalized Windows path policy engine beyond writable roots and
  deny-write carveouts.

## Codex Reference Findings

Codex has two relevant Windows families:

- The legacy unelevated path directly spawns the child with a restricted token.
  It derives the token from the current user, attaches synthetic capability SIDs,
  and applies ACLs to writable roots.
- The elevated path creates sandbox users, stores credentials, refreshes ACLs,
  launches a command runner under a sandbox account, and installs network rules.

Caelis should copy the useful legacy idea, not the elevated system:

- Keep current-user token derivation.
- Keep per-workspace or per-write-root synthetic SIDs.
- Keep direct process launch.
- Reject or ignore read/network controls at the Windows policy boundary instead
  of pretending they are enforced.
- Do not include the real user SID as a restricting SID for filesystem writes.
  The ordinary token already contains the user SID; putting it in the restricted
  SID list would allow every user-writable path.
- Avoid adding broad restricting SIDs such as `Everyone` unless a specific Win32
  handle/default-DACL issue proves it is required. If such a SID is required for
  PowerShell, CLR, or IPC compatibility, keep the set minimal and cover common
  filesystem escape paths with integration tests.

## Security Model

The target security property is:

For filesystem writes, Windows access checks must require both:

- the current user's normal token SIDs can write the object; and
- one active Caelis synthetic write SID can write the object.

This is achieved with `CreateRestrictedToken` using `WRITE_RESTRICTED`.

The restricted token should include only the active Caelis synthetic write SID
set as filesystem-authorizing restricting SIDs. The current user SID, local
Users group, Administrators group, and broad well-known SIDs must not be used as
filesystem-authorizing restricting SIDs.

The restricted token must also set a default DACL for child-created kernel
objects such as anonymous pipes, events, and shared sections. That DACL should
grant the active synthetic SIDs plus narrow compatibility SIDs used in the
restricting list, without adding the current user or `Users` as filesystem
authorizing restricting SIDs.

Allowed roots receive an inheritable allow ACE for the active synthetic write
SID. Deny-write carveouts receive an inheritable deny ACE for the same SID.
Read access is intentionally left to the normal current-user token.

If the ACL state is missing, stale, or cannot be applied, sandbox execution must
fail before launching the child. It must not silently run on host.

## Policy Shape

The Windows workspace-write backend consumes only:

- `sandbox.Config.CWD`
- `sandbox.Config.WritableRoots`
- `sandbox.Config.ReadOnlySubpaths`, as deny-write carveouts only when the
  target path already exists
- `sandbox.Constraints.Permission`
- `sandbox.Constraints.PathRules` with `read_write`, as extra writable roots

The backend must not claim support for:

- read roots as a security boundary;
- hidden path read denial;
- network disabled mode.

Default Windows policy normalization should not manufacture hard read
requirements. Network-disabled intent is currently normalized to the online
restricted-token implementation because no offline Windows path exists yet. If
future callers require hard read isolation or enforced network isolation, that
request should fail as unsupported for this backend rather than being reported
as enforced.

## Backend Contract

The new backend should be named by capability, not by the obsolete setup model.
Preferred canonical naming is `windows` or `windows-restricted-token`.
Existing aliases such as `windows-elevated`, `windows_elevated`, and `elevated`
may remain accepted during migration, but they must resolve to the new simple
backend or to legacy cleanup commands, not to a resurrected elevated runner.

`Describe` should report:

- `Backend`: the canonical Windows backend.
- `Isolation`: `process`.
- `FileSystem`: true.
- `CommandExec`: true.
- `AsyncSessions`: true if implemented through the same direct spawn path.
- `TTY`: true only when ConPTY works through the restricted token path.
- `NetworkControl`: false.
- `PathPolicy`: false for read/hidden policy; true only if the descriptor can
  clearly express write-root enforcement.

`Status` should be cheap. It may report whether the current workspace ACL
manifest appears fresh, but it must not inspect broad filesystem state or prompt
the user at startup.

## Runtime Flow

For a sandboxed command:

1. Normalize the command directory and writable roots.
2. Load or create the per-workspace capability SID store under the Caelis sandbox
   state directory.
3. Compute the active write-root SID set and deny-write carveouts.
4. Check the small ACL manifest for the same workspace root, policy hash, SID
   set, write roots, and deny-write paths.
5. If stale, apply only the required root-level inheritable ACE changes.
6. Prepare the runtime-owned command environment under the Caelis sandbox state
   directory, not under the workspace.
7. Create a restricted primary token from the current process token.
8. Spawn the command directly with `CreateProcessAsUserW` and a minimal
   PowerShell-compatible environment.
9. Attach stdio/ConPTY handles directly and keep cancellation/timeout semantics
   equivalent to host execution.
10. Return command results with sandbox route/backend metadata.

There is no runner process as another user. A small internal launcher function
is fine; a separate command-runner protocol is not.

## ACL State

The new implementation should maintain a compact manifest, for example:

- version;
- workspace root;
- policy hash;
- synthetic SID values;
- write roots;
- deny-write paths;
- ACEs added by Caelis;
- updated timestamp.

Setup and command execution use the same idempotent `ensure` operation. Setup is
only a pre-warm path. Command execution may lazily ensure the ACL state if it is
missing or stale.

Clean uses the manifest to remove Caelis-added ACEs. Missing paths and already
removed ACEs are ignored.

## Cleanup Compatibility

`caelis sandbox reset` and `caelis sandbox clean` should remain broader than the
new runtime. They are the compatibility surface for discovering and removing old
Windows sandbox leftovers.

Clean should attempt, best effort:

- remove new workspace-write ACL ACEs recorded in the new manifest;
- remove old capability SID ACLs recorded by previous state files;
- remove old `.sandbox` runner directories, helper binaries, setup progress,
  setup markers, and workspace setup records;
- detect old local sandbox users and groups;
- detect old Windows Firewall or WFP rules;
- detect old DPAPI sandbox credential files;
- detect old runner cwd junctions or temporary runner state.

Clean should not require elevation for the new implementation. If legacy local
users, groups, firewall rules, or protected files require elevation, clean should
report the exact leftovers and continue with non-elevated cleanup.

Clean must never run automatically before normal commands.

## Code Removal Targets

Remove or quarantine from the normal execution path:

- elevated setup orchestration;
- local sandbox user and group provisioning;
- offline/online network identity selection;
- DPAPI password storage;
- WFP and Windows Firewall rule management;
- setup helper command protocol;
- command runner IPC protocol;
- runner-client transport;
- runner account launch logic;
- read-root ACL refresh;
- deny-read ACL state;
- profile hiding;
- world-writable scans required only because broad SIDs were added;
- cwd junction workarounds tied to sandbox-user read ACLs;
- TUI startup checks and prompts for Windows sandbox setup.

Keep or rebuild narrowly:

- Windows path normalization and case-insensitive dedupe;
- capability SID generation and persistence;
- ACL add/revoke helpers;
- restricted token creation;
- direct process spawning and handle inheritance;
- ConPTY support if it works without broadening write access;
- job-object kill-on-close or equivalent cleanup;
- compact state manifest for ACL cleanup.

## Non-Windows Isolation

This project must not regress non-Windows behavior:

- New implementation files must be behind `//go:build windows`.
- Non-Windows stubs must remain tiny and inert.
- Public sandbox contracts should change only when required for backend-neutral
  truthfulness.
- Linux and macOS backend selection, setup hints, tests, and behavior must stay
  unchanged.
- Windows-only cleanup code must not be imported by shared packages except
  through existing sandbox runtime interfaces.

## Performance Requirements

- Explicit host Windows execution must not instantiate the Windows sandbox
  backend or run setup checks.
- Warm sandbox command startup should be a small constant overhead over host
  command startup.
- ACL ensure must be manifest-driven and bounded by the number of write roots
  and deny-write carveouts.
- No recursive ACL propagation over the workspace.
- No command HOME/TEMP/cache directories created under the workspace.
- The runtime-owned HOME/TEMP/cache directories live under the sandbox state
  directory and receive explicit bounded ACL repair for those fixed directories
  only.
- No configured writable or read-only carveout directories created just to make
  a policy path exist.
- No broad profile, PATH, Program Files, or drive-root scanning during normal
  command execution.
- No network probing during setup or command execution.

## Failure Behavior

- Explicit sandbox request plus unsupported permission mode: fail with a clear
  unsupported error.
- Explicit sandbox request plus ACL apply failure: fail before child launch.
- Explicit sandbox request plus restricted token creation failure: fail before
  child launch.
- Explicit sandbox request plus child spawn failure: return the spawn error.
- Explicit Windows host route: run on host and do not surface sandbox setup
  warnings.

## Acceptance Criteria

Unit tests:

- Windows default backend resolves to the new simple sandbox after the backend
  is available.
- Explicit Windows backend resolves to the new simple backend.
- Non-Windows router behavior is unchanged.
- Policy conversion includes only workspace/current dir and configured writable
  roots as write roots.
- Read and network controls are not reported as enforced.
- Synthetic SID store is stable per workspace and per extra write root.
- Restricted token construction never uses the current user SID as a
  filesystem-authorizing restricting SID.
- ACL ensure is idempotent and manifest-driven.
- Clean plans include both new manifest cleanup and legacy artifact discovery.

Windows integration tests:

- A sandboxed command can read normal current-user-readable paths.
- A sandboxed command can write inside the workspace.
- A sandboxed command can create pip-style unpack directories under the
  sandbox-provided `%TEMP%`.
- A sandboxed command cannot write to a user-writable path outside all allowed
  roots, such as a fresh file under `%TEMP%`.
- A sandboxed command cannot write to configured deny-write carveouts such as
  `.git`, `.codex`, `.agents`, or configured read-only subpaths.
- Network is not blocked or modified by the sandbox path. At minimum, no proxy
  variables or firewall setup are injected by the backend. Requests for
  `NetworkDisabled` currently run through the same online implementation.
- PowerShell and cmd both work.
- stdout, stderr, non-ASCII output, stdin, timeout, and cancellation match host
  semantics.
- Git, Go, npm, and Python smoke commands run when installed on the host.
- Warm repeated sandbox commands do not rerun full ACL setup.
- Sandboxed command execution prepares the current workspace without elevation
  when ACL state is missing or stale.
- `caelis sandbox reset` and `caelis sandbox clean` remove new ACL state and
  report legacy leftovers without breaking when elevation is unavailable.

Security regression tests:

- If the workspace allow ACE is removed, sandbox execution fails or repairs the
  ACE before launch; it never runs unsandboxed.
- If a deny-write carveout is present, a nested writable-root rule cannot reopen
  it unless that behavior is explicitly designed and tested.
- If an extra writable root is removed from the current policy, stale ACLs from
  that root do not grant write access in the next token.
- Result metadata identifies the sandbox backend for sandboxed commands and host
  backend for host commands.

Validation commands:

```powershell
go test ./internal/adapters/sandbox/host ./impl/sandbox/host
go test ./impl/sandbox/windows/...
git diff --check
```

On non-Windows builders:

```sh
go test ./internal/adapters/sandbox/host ./impl/sandbox/host ./impl/sandbox/windows/...
git diff --check
```

## Migration Plan

1. Add the new direct restricted-token backend behind Windows build tags.
2. Route explicit Windows sandbox requests to the new backend.
3. Switch the default Windows route to the new sandbox after proving it usable.
4. Replace setup readiness gating with lazy ACL ensure.
5. Rewrite setup to call only the lightweight ACL ensure path.
6. Rewrite clean to remove new ACL state and discover legacy elevated artifacts.
7. Delete or quarantine elevated runner/setup/network code from normal builds.
8. Remove obsolete TUI startup setup prompts.
9. Update tests and docs to stop treating `windows-elevated` as the desired
   runtime model.
