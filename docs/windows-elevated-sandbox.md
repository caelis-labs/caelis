# Windows Elevated Sandbox Design

This document is the implementation target for native Windows support and the
Windows Elevated sandbox backend in Caelis.

The design follows the same dependency direction as the rest of the repository:

- `ports/sandbox` owns the stable backend-neutral contract.
- `impl/sandbox/windows` owns the Windows implementation.
- `app/gatewayapp` wires the default backend and persisted config.
- setup helpers and command runners are separate binaries under `cmd/*`.

The initial backend name is `windows-elevated`. The backend is intentionally
implemented inside this repository first. It should only move to a standalone
repository after the setup protocol, runner protocol, policy mapping, and test
matrix have stabilized.

## Why Elevated First

Codex has two Windows sandbox families:

- Restricted-token or legacy execution, built around a restricted process token.
- Elevated setup plus a sandbox-user command runner, ACL refresh, capability
  SIDs, and network policy.

The legacy path is useful as a lightweight process restriction, but it cannot
make deny-read ACLs authoritative and cannot represent restricted read-only
access fully. Caelis should therefore target the Elevated path directly instead
of first cloning the legacy path.

Codex source anchors used for this direction:

- `codex-rs/core/src/windows_sandbox.rs`: resolves Disabled, RestrictedToken,
  and Elevated modes, and runs elevated setup.
- `codex-rs/core/src/unified_exec/process_manager.rs`: dispatches Elevated to
  the elevated runner backend and legacy/disabled to the legacy backend.
- `codex-rs/windows-sandbox-rs/src/setup.rs`: creates setup payloads, launches
  the elevated helper with `runas`, manages sandbox users and marker files.
- `codex-rs/windows-sandbox-rs/src/unified_exec/backends/elevated.rs`: prepares
  elevated spawn context and talks to the command runner.
- `codex-rs/windows-sandbox-rs/src/spawn_prep.rs`: maps sandbox policy to ACLs,
  capability SIDs, environment, and command context.

## Current Caelis Baseline

The first Caelis integration layer should remain truthful:

- `ports/sandbox.BackendWindowsElevated` exists as `windows-elevated`.
- Windows auto backend selection should choose `windows-elevated`.
- Explicit Unix backends should be rejected on Windows.
- `app/gatewayapp/internal/sandboxpolicy` should accept `windows`,
  `elevated`, and `windows-elevated` aliases.
- `app/gatewayapp` should register `impl/sandbox/windows`.
- Until the backend is complete, `impl/sandbox/windows` should fail with a clear
  "not implemented yet" error instead of silently using host execution as if it
  were isolated.

The host backend must compile and behave natively on Windows. It can use
PowerShell for host execution while the sandbox backend develops a stronger
runner path.

## Goals

- Provide real Windows process isolation for default sandboxed tool execution.
- Support `sandbox.Constraints` with workspace write, full access, path rules,
  TTY, stdin, stdout/stderr streaming, timeouts, and termination.
- Support both offline and online sandbox identities so network policy is not an
  environment-variable convention only.
- Preserve Caelis event semantics and terminal/session references.
- Keep Windows-only details out of `ports/*`, `kernel/*`, and generic ACP
  adapters.
- Make setup repeatable, refreshable, diagnosable, and safe to run on developer
  machines.

## Non-Goals

- Do not expose Win32 types through public ports.
- Do not require WSL, Docker, or Windows Sandbox VM.
- Do not make `windows-elevated` a separate module until the local
  implementation has stable integration tests.
- Do not claim deny-read support before ACL and runner enforcement are wired.

## Package Layout

Target package and command layout:

```text
ports/sandbox/
  sandbox.go
  runtime.go

impl/sandbox/windows/
  runtime.go
  runtime_windows.go
  runtime_unsupported.go
  internal/win32/
  internal/policy/
  internal/setupstate/
  internal/runnerproto/
  internal/runnerclient/
  internal/job/
  internal/conpty/
  internal/pathutil/
  internal/netpolicy/

cmd/caelis-windows-sandbox-setup/
  main.go

cmd/caelis-command-runner/
  main.go
```

Suggested responsibilities:

- `internal/win32`: thin, tested wrappers for token, SID, ACL, process,
  ShellExecute, logon, profile, desktop, pipe, Job Object, and WFP calls.
- `internal/policy`: conversion from `sandbox.Config` and
  `sandbox.Constraints` to Windows read/write/hidden roots.
- `internal/setupstate`: marker files, setup version, runner hash, policy hash,
  account metadata, and refresh decisions.
- `internal/runnerproto`: framed messages shared by parent and runner.
- `internal/runnerclient`: parent-side command runner launch and handshake.
- `internal/job`: process tree lifetime and termination.
- `internal/conpty`: TTY support, resize, and stream fan-out.
- `internal/pathutil`: Windows path canonicalization, drive handling, UNC paths,
  case folding, and short-name normalization.
- `internal/netpolicy`: online/offline identity selection and firewall/WFP
  refresh.

## On-Disk State

Use the Caelis app store root as the sandbox home. By default this is under
`~/.caelis`; tests should always inject a temporary store root.

```text
<storeDir>/
  .sandbox/
    setup_marker.json
    setup_error.json
    logs/
  .sandbox-bin/
    caelis-command-runner-<version-or-hash>.exe
    caelis-windows-sandbox-setup-<version-or-hash>.exe
  .sandbox-secrets/
    sandbox_users.json
    dpapi-protected credentials
```

Security expectations:

- `.sandbox-secrets` is readable only by the real user and Administrators.
- The command runner is materialized under `.sandbox-bin` with content hashing
  so stale binaries can be replaced safely.
- Setup marker version changes force refresh.
- Runner hash changes force refresh.
- Policy root changes force ACL refresh.

## Windows Identities

Target local accounts and group:

- `CaelisSandboxUsers`: shared local group for sandbox identities.
- `CaelisSandboxOffline`: no direct outbound network by default.
- `CaelisSandboxOnline`: network allowed when policy asks for it.

The elevated setup helper is responsible for:

- Creating or updating local users.
- Rotating passwords when required.
- Storing credentials with DPAPI protection.
- Ensuring sandbox users are not Administrators.
- Ensuring the real user can launch the runner but cannot read sandbox secrets.
- Refreshing account rights and profile directories.

## Setup Flow

Parent process flow:

1. Build a setup request from current `sandbox.Config`, workspace, env, and
   constraints.
2. Check setup marker, runner hash, identity state, and root policy hash.
3. If setup is stale or missing, materialize helper binaries into
   `.sandbox-bin`.
4. Launch `caelis-windows-sandbox-setup.exe` with ShellExecute `runas`.
5. Wait for completion and read `setup_error.json` on failure.
6. Continue only after marker and secrets are consistent.

Elevated helper flow:

1. Decode signed or integrity-checked setup payload.
2. Create sandbox group and users.
3. Create sandbox state directories and ACL them.
4. Apply allow ACLs for read roots and write roots.
5. Apply deny-write ACLs for hidden or protected write paths.
6. Apply deny-read ACLs for hidden paths.
7. Refresh WFP/firewall rules for offline and online identities.
8. Write setup marker atomically.

Setup must be idempotent. Refresh should be the normal path, not an exceptional
repair path.

## Command Runner Protocol

The parent process should not create sandboxed child processes directly after
Elevated setup. It should launch a command runner as the sandbox user and speak
a small framed protocol over anonymous pipes or named pipes.

Minimum messages:

- `hello`: protocol version, runner version, sandbox identity, capabilities.
- `spawn`: command, cwd, env, timeout, TTY flag, stdin-open flag, policy roots,
  capability SID strings, and optional desktop flag.
- `stdin`: raw input chunk.
- `resize`: terminal rows and columns.
- `interrupt`: graceful termination request.
- `kill`: forced termination request.
- `stdout`: stdout bytes.
- `stderr`: stderr bytes.
- `exit`: exit code, signal-like reason, duration.
- `error`: structured setup, spawn, policy, or IO failure.

The runner owns:

- Loading or receiving sandbox credentials.
- Creating the final restricted token.
- Attaching capability SIDs.
- Creating the process with the requested cwd and env.
- Creating a private desktop when configured.
- Creating a Job Object and assigning the process tree.
- Creating ConPTY when `TTY=true`.
- Streaming stdout/stderr and honoring stdin.
- Killing the whole job on timeout or explicit termination.

## Policy Mapping

Input sources:

- `sandbox.Config.CWD`
- `sandbox.Config.ReadableRoots`
- `sandbox.Config.WritableRoots`
- `sandbox.Config.ReadOnlySubpaths`
- `sandbox.Constraints.Permission`
- `sandbox.Constraints.Network`
- `sandbox.Constraints.PathRules`
- command request cwd, env, and stdin/TTY flags

Mapping rules:

- `PermissionFullAccess` should route to host execution unless the caller
  explicitly requests sandbox route with a restricted override.
- `PermissionWorkspaceWrite` should allow read access to platform defaults,
  workspace, configured readable roots, and required tool/runtime roots; write
  access to workspace, temp, configured writable roots, and approved skill
  roots.
- `PathAccessReadOnly` contributes a read root.
- `PathAccessReadWrite` contributes a write root.
- `PathAccessHidden` contributes a deny-read path and deny-write path.
- `ReadOnlySubpaths` under writable roots become deny-write paths.
- `NetworkDisabled` chooses offline identity and applies no-network env
  hardening.
- `NetworkEnabled` chooses online identity.
- `NetworkInherit` should default to disabled for sandbox route unless product
  policy explicitly grants network.

Default read roots on Windows should include only what is required:

- `C:\Windows`
- `C:\Program Files`
- `C:\Program Files (x86)`
- `C:\ProgramData`
- runner/helper directories
- the active workspace
- configured readable roots

Default write roots:

- active workspace
- temp directory for the sandbox identity
- configured writable roots
- approved skill directories already added by `sandboxpolicy.EffectiveConfig`

Protected paths:

- real user profile secrets such as `.ssh`, `.aws`, `.azure`, `.kube`,
  `.docker`, `.gnupg`, `.npm`, `.config`, and Caelis secrets.
- `.sandbox-secrets`
- setup helper payloads and markers unless explicitly required by setup.

## Native Windows Support

The Elevated backend depends on broader native Windows correctness across the
project.

Required support:

- Path logic must use `filepath` and Windows-aware canonicalization.
- Path dedupe should be case-insensitive on Windows.
- Tests that override home must set `USERPROFILE` as well as `HOME`.
- Tests that create fake executables must use `.cmd` or `.exe` on Windows.
- Directory fsync must be best-effort on Windows because ordinary directory
  handles can return access denied.
- Host shell execution should use a Windows shell, currently PowerShell.
- Managed npm adapter paths must include `.cmd` on Windows.
- File permission tests must not assume POSIX mode bits on Windows.
- Long paths and short 8.3 paths should normalize to the same policy key where
  possible.
- Drive roots and UNC roots must be treated as policy boundaries.

Future UI/prompt cleanup:

- The shell tool can remain protocol-compatible, but Windows prompts should not
  imply that every command runs in POSIX bash.
- Diagnostics should report the resolved backend, setup status, account names,
  runner version, and policy roots.

## Runtime API Behavior

`impl/sandbox/windows.Runtime` should implement:

- `Describe`: report `BackendWindowsElevated`, process isolation, command exec,
  async sessions, TTY, network control, path policy, and env policy.
- `FileSystem`: return a policy-aware filesystem for the default sandbox
  constraints.
- `FileSystemFor`: return a policy-aware filesystem for the supplied
  constraints.
- `Run`: spawn and collect output to completion.
- `Start`: spawn an async session and return `sandbox.Session`.
- `OpenSession`: reopen in-memory live sessions; persisted process reattach is
  out of scope for the first version.
- `OpenSessionRef`: validate backend and route to `OpenSession`.
- `SupportedBackends`: include host and `windows-elevated` through the composed
  runtime.
- `Status`: surface setup failure details and fallback hints.
- `Close`: terminate live runner transports and jobs owned by this runtime.

Fallback policy:

- Auto backend may fall back to host only when setup or backend construction is
  unavailable and the caller did not explicitly request `windows-elevated`.
- Explicit `windows-elevated` must fail closed if setup or runner spawn fails.
- A command that asks for sandbox route must not silently run on host after a
  backend-specific denial.

## Error Model

Use structured error wrapping so tool surfaces can distinguish:

- setup required
- setup canceled
- setup failed
- runner materialization failed
- runner handshake failed
- policy denied
- ACL refresh failed
- network policy refresh failed
- process spawn failed
- timeout
- termination failed

User-facing errors should include the failed phase and a safe remediation hint.
They must not include secrets or full DPAPI payload paths.

## Test Plan

Unit tests:

- backend normalization aliases
- Windows candidate backend selection
- path canonicalization and case-insensitive dedupe
- policy mapping from constraints to roots
- setup marker stale/current decisions
- runner framed protocol encode/decode
- `.cmd` executable discovery in tests
- no-op directory fsync on Windows

Integration tests on Windows:

- setup creates users, group, marker, bin dir, and secrets dir
- setup refresh is idempotent
- workspace write can create and edit files inside workspace
- write outside workspace is denied
- read of hidden path is denied
- write of read-only subpath is denied
- network disabled cannot reach external endpoints
- network enabled can reach a local test endpoint when allowed
- stdin works
- stdout and stderr stream independently without TTY
- TTY session uses ConPTY and receives resize events
- timeout kills the full process tree
- terminate kills background grandchildren via Job Object
- explicit `windows-elevated` fails closed if setup is missing
- auto backend fallback status includes an install/setup hint

Cross-platform tests:

- Unix host behavior remains unchanged.
- Windows-only tests use build tags or runtime skips.
- Existing `app/gatewayapp` tests pass on Windows without reading real user
  `~/.agents`.
- `go test ./ports/sandbox ./app/gatewayapp ./impl/session/file` passes on
  Windows.

## TODO

### Phase 0: Contract and Scaffolding

- [x] Add `BackendWindowsElevated`.
- [x] Select `windows-elevated` for Windows auto backend candidates.
- [x] Reject Unix sandbox backends on Windows.
- [x] Register `impl/sandbox/windows` from `app/gatewayapp`.
- [x] Add a truthful unimplemented Windows backend stub.
- [x] Split host process helpers by OS so Windows builds cleanly.

### Phase 1: Native Windows Test Compatibility

- [x] Make directory sync best-effort/no-op on Windows.
- [x] Make gateway home overrides set `USERPROFILE`.
- [x] Make ACP fake npm tests use `.cmd` on Windows.
- [x] Normalize managed ACP adapter expectations through
  `managedACPAgentBinPath`.
- [ ] Add a shared cross-package Windows test helper for home and executable
  suffix handling if more packages need it.
- [ ] Audit remaining `os.UserHomeDir` tests for Windows env assumptions.
- [ ] Audit shell-based tests for POSIX-only snippets.

### Phase 2: Win32 Foundation

- [ ] Add `internal/win32` wrappers for SID lookup and string conversion.
- [ ] Add token restriction and capability SID wrappers.
- [ ] Add ACL read/modify/write wrappers.
- [ ] Add `ShellExecuteExW` wrapper for elevated helper launch.
- [ ] Add `LogonUser` and profile loading wrappers.
- [ ] Add named pipe or anonymous pipe transport wrappers.
- [ ] Add Job Object wrapper for process tree cleanup.
- [ ] Add ConPTY wrapper with resize support.
- [ ] Add WFP/firewall rule wrapper or documented fallback.

### Phase 3: Setup Helper

- [ ] Create `cmd/caelis-windows-sandbox-setup`.
- [ ] Define setup payload schema and version.
- [ ] Materialize helper binary into `.sandbox-bin`.
- [ ] Implement UAC launch from parent runtime.
- [ ] Create `CaelisSandboxUsers`.
- [ ] Create or update offline and online users.
- [ ] Store credentials with DPAPI.
- [ ] ACL `.sandbox`, `.sandbox-bin`, and `.sandbox-secrets`.
- [ ] Apply read/write/hidden root ACL refresh.
- [ ] Apply network policy refresh.
- [ ] Write setup marker atomically.
- [ ] Write sanitized setup error reports.

### Phase 4: Command Runner

- [ ] Create `cmd/caelis-command-runner`.
- [ ] Define runner framed protocol.
- [ ] Implement parent-runner handshake.
- [ ] Spawn child process with restricted token and capability SIDs.
- [ ] Implement non-TTY pipes.
- [ ] Implement ConPTY mode.
- [ ] Implement stdin, resize, interrupt, kill, and timeout.
- [ ] Assign children to a Job Object.
- [ ] Stream stdout/stderr into `sandbox.Session`.
- [ ] Return structured exit status and errors.

### Phase 5: Windows Runtime

- [ ] Implement `impl/sandbox/windows.Runtime.Describe`.
- [ ] Implement setup freshness checks.
- [ ] Implement setup refresh orchestration.
- [ ] Implement `Run`.
- [ ] Implement `Start`.
- [ ] Implement live session registry.
- [ ] Implement `OpenSession` and `OpenSessionRef`.
- [ ] Implement `Close`.
- [ ] Expose setup failure hints through `sandbox.Status`.
- [ ] Preserve explicit-backend fail-closed behavior.

### Phase 6: Policy and Filesystem

- [ ] Implement Windows path canonicalization.
- [ ] Implement case-insensitive root dedupe.
- [ ] Map `sandbox.Config` and `sandbox.Constraints` to Windows roots.
- [ ] Support `PathAccessHidden` as deny-read and deny-write.
- [ ] Protect known user secret directories by default.
- [ ] Add policy-aware filesystem reads for tools.
- [ ] Add tests for drive roots, UNC roots, short paths, and long paths.

### Phase 7: Network Control

- [ ] Define online/offline identity semantics.
- [ ] Implement WFP/firewall setup.
- [ ] Add no-network environment hardening.
- [ ] Add local proxy compatibility if needed.
- [ ] Add integration tests for disabled and enabled network modes.

### Phase 8: Diagnostics and UX

- [ ] Add doctor checks for Windows Elevated setup.
- [ ] Report setup version, marker status, helper hash, runner hash, and user
  existence.
- [ ] Add a manual setup command if useful.
- [ ] Add clear remediation for UAC cancellation.
- [ ] Update prompts or tool labels so Windows users are not told every shell is
  POSIX bash.

### Phase 9: Extraction Decision

- [ ] Keep implementation internal until setup, runner, policy, and tests are
  stable.
- [ ] Extract only if another repository needs to implement Caelis sandbox
  ports.
- [ ] If extracted, keep `ports/sandbox` in Caelis and publish only the concrete
  Windows backend plus helper binaries.
- [ ] Version runner protocol and setup payload before extraction.

## Acceptance Criteria

The Windows Elevated backend is complete when:

- A fresh Windows machine can run setup through UAC and then run sandboxed
  commands without manual account preparation.
- Workspace-write commands can read required platform/tool roots and write only
  allowed roots.
- Hidden paths cannot be read by sandboxed commands.
- Read-only subpaths cannot be written by sandboxed commands.
- Network-disabled commands cannot reach the network.
- Timeout and terminate clean the full process tree.
- TTY and non-TTY sessions both work.
- `go test` covers port selection, policy mapping, runner protocol, and Windows
  integration behavior.
- Explicit `windows-elevated` never silently falls back to host execution.
