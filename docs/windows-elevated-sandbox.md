# Windows Elevated Sandbox Design

This document records the implementation target and current design for native
Windows support and the Windows Elevated sandbox backend in Caelis.

The design follows the same dependency direction as the rest of the repository:

- `ports/sandbox` owns the stable backend-neutral contract.
- `impl/sandbox/windows` owns the Windows implementation.
- `app/gatewayapp` wires the default backend and persisted config.
- setup helpers and command runners can be built as separate binaries under
  `cmd/*`, while the normal `caelis.exe` also dispatches the internal helper
  and runner subcommands for local development and E2E validation.

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
  the elevated helper with `runas`, manages sandbox identities and marker files.
- `codex-rs/windows-sandbox-rs/src/unified_exec/backends/elevated.rs`: prepares
  elevated spawn context and talks to the command runner.
- `codex-rs/windows-sandbox-rs/src/spawn_prep.rs`: maps sandbox policy to ACLs,
  capability SIDs, environment, and command context.

## Current Caelis Implementation

The current integration layer is intended to be truthful about what is actually
enforced:

- `ports/sandbox.BackendWindowsElevated` exists as `windows-elevated`.
- Windows auto backend selection chooses `windows-elevated`.
- Explicit Unix backends are rejected on Windows.
- `app/gatewayapp/internal/sandboxpolicy` accepts `windows`, `elevated`,
  `windows-elevated`, and `host` aliases.
- `app/gatewayapp` registers `impl/sandbox/windows`.
- `impl/sandbox/windows` now provides real setup and runner paths instead of an
  unimplemented stub.
- Windows setup is explicit: normal sandbox command execution never launches a
  UAC prompt. Users initialize or refresh full setup through
  `caelis sandbox setup` or TUI `/sandbox setup`.
- Host execution remains native on Windows and uses PowerShell rather than a
  POSIX shell.

The default Windows Elevated enforcement path is:

- an offline local sandbox user and the `CaelisSandboxUsers` group;
- idempotent elevated setup with marker/error state and DPAPI-protected
  credentials;
- ACL grants/denies on read, write, hidden, and protected roots;
- offline sandbox identity with Windows Firewall outbound block rules scoped by
  `LocalUser` and split across non-loopback,
  loopback TCP, and loopback UDP traffic;
- workspace-scoped `USERPROFILE`, `HOME`, `TEMP`, `TMP`, `LOCALAPPDATA`, and
  `APPDATA` values;
- command runner IPC, PowerShell command execution, ConPTY support, Job Object
  cleanup, timeout, kill, and async session plumbing.
- non-elevated per-command ACL refresh for current read/write/deny roots, so
  approved `request_permissions` grants do not change the full setup marker and
  do not cause later commands to pop UAC.

Capability SID work follows the Codex design direction: Caelis persists random
per-workspace/per-write-root SIDs, applies matching ACLs, and attaches those
capability SIDs to the child restricted token by default. The debug escape hatch
`CAELIS_WINDOWS_SANDBOX_ATTACH_CAPS=0` disables attachment if a Windows machine
exposes a process-startup incompatibility that needs investigation.

## Goals

- Provide real Windows process isolation for default sandboxed tool execution.
- Support `sandbox.Constraints` with workspace write, full access, path rules,
  TTY, stdin, stdout/stderr streaming, timeouts, and termination.
- Use the offline sandbox identity for sandboxed commands; host networking is
  handled by approved host execution.
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
  ShellExecute, logon, profile, desktop, pipe, and Job Object calls.
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
- `internal/netpolicy`: offline identity firewall refresh.

## On-Disk State

Use the Caelis app store root as the sandbox home. By default this is under
`~/.caelis`; tests should always inject a temporary store root.

```text
<storeDir>/
  .sandbox/
    setup_marker.json
    setup_error.json
    setup_progress.json
    cap_sids.json
    logs/
  .sandbox-bin/
    caelis-windows-sandbox-<hash>.exe
  .sandbox-secrets/
    sandbox_users.json
    dpapi-protected credentials
```

Security expectations:

- `.sandbox-secrets` is readable only by the real user and Administrators.
- The command runner is materialized under `.sandbox-bin` with content hashing
  so stale binaries can be replaced safely. If `caelis-command-runner.exe` is
  available next to the main binary, sandboxed commands use that lightweight
  runner; local development can still fall back to the helper-capable
  `caelis.exe` internal runner command.
- Setup marker version changes force refresh.
- Local sandbox identity or owner changes force refresh.
- Runner hash changes are diagnostic and cause helper rematerialization when
  needed; they do not force another elevated setup prompt.
- Policy root changes force non-elevated request ACL refresh, not full setup.
- `cap_sids.json` stores random capability-style SIDs by normalized workspace
  and write root. It is state, not a secret; it should still be kept under the
  protected sandbox state directory to avoid accidental tampering.

## Windows Identities

Target local accounts and group:

- `CaelisSandboxUsers`: shared local group for sandbox identities.
- `CaelisSbxOff<hash>`: no direct outbound network by default.

The `<hash>` suffix is derived from the Caelis sandbox state root. This keeps
normal user state and development/E2E state from rotating the same global
Windows account passwords.

The elevated setup helper is responsible for:

- Creating or updating the offline local sandbox user.
- Rotating passwords when required.
- Storing credentials with DPAPI protection.
- Ensuring sandbox identities are not Administrators.
- Ensuring the real user can launch the runner but cannot read sandbox secrets.
- Refreshing account rights and profile directories.

## Setup Flow

Status and startup flow:

1. TUI startup, `/status`, and `doctor` inspect setup marker and identity
   state. Runner hash and policy hash are reported for diagnostics, but normal
   local rebuilds and workspace changes do not make setup stale.
2. If setup is missing or stale, the status is `setup_required` with a
   remediation hint. No command path launches UAC automatically.
3. The TUI shows a startup notice for Windows Elevated setup gaps and asks the
   user to run `/sandbox setup`.

Explicit setup flow:

1. The user runs `caelis sandbox setup` or TUI `/sandbox setup`.
2. The parent builds a setup request from current `sandbox.Config`, workspace,
   and the stable base Windows sandbox constraints.
3. The full setup payload uses a root-independent policy hash. Workspace roots,
   configured roots, and per-command `request_permissions` read/write roots are
   intentionally excluded from the marker freshness check.
4. The parent materializes the current helper-capable `caelis.exe` into
   `.sandbox-bin` under a hash-qualified name.
5. If the parent is already elevated, it executes setup in-process. Otherwise it
   launches the helper with ShellExecute `runas`.
6. The parent reports phase progress to the TUI while setup checks accounts,
   protects Caelis sandbox state directories, and refreshes Windows Firewall
   policy. Workspace ACLs are not part of full setup. When the helper runs in a
   separate elevated process, progress is mirrored through
   `.sandbox/setup_progress.json`.
7. The parent waits for completion and reads `setup_error.json` on failure.
8. Normal sandbox execution is allowed only after marker and secrets are
   consistent.

Normal command flow:

1. Check the stable setup marker and sandbox user secrets.
2. If setup is missing, stale, or incomplete, fail closed with guidance to run
   `/sandbox setup` or `caelis sandbox setup`.
3. Build the request-specific Windows policy, including dynamic
   `request_permissions` read/write roots and hidden/read-only path rules.
4. Refresh request ACLs without elevation. This may update workspace/read-root
   ACLs for the sandbox group and per-root capability SIDs, but it never uses
   `runas` and never displays UAC.
5. Launch the command runner as the selected sandbox identity.

Elevated helper flow:

1. Decode signed or integrity-checked setup payload.
2. Create sandbox group and users.
3. Create sandbox state directories and ACL them.
4. Apply allow ACLs for read roots and write roots.
5. Apply deny-write ACLs for hidden or protected write paths.
6. Apply deny-read ACLs for hidden paths.
7. Refresh Windows Firewall rules for the offline identity.
8. Write setup marker atomically.

Full setup must be idempotent. Per-command ACL refresh should be the normal
non-elevated path, not an elevated repair path.

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
- Attaching capability SIDs by default, with
  `CAELIS_WINDOWS_SANDBOX_ATTACH_CAPS=0` as a temporary debug override.
- Creating the process with the requested cwd and env.
- Creating a private desktop when configured.
- Creating a Job Object and assigning the process tree.
- Creating ConPTY when `TTY=true`.
- Streaming stdout/stderr and honoring stdin.
- Killing the whole job on timeout or explicit termination.

The non-TTY default runs PowerShell as the selected sandbox user and attaches
the per-root capability SIDs to a restricted child token. The token path mirrors
the relevant Codex model pieces: per-root random SIDs, user/logon and Everyone
restricting SIDs, a permissive default DACL for allowed SIDs, null device
access, and `SeChangeNotifyPrivilege`.

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
- `NetworkEnabled` and `NetworkInherit` resolve to the offline identity on
  Windows; sandbox route does not offer per-command network enablement.

Offline network enforcement currently uses persistent Windows Firewall rules
modeled after the relevant Codex firewall setup shape:

- `LocalUser` SDDL is `O:LSD:(A;;CC;;;offline-sid)`.
- Non-loopback outbound traffic is blocked for all protocols.
- Loopback TCP and loopback UDP are blocked separately so local services are not
  accidentally reachable in no-network mode.
- The sandbox environment also points common proxy variables at
  `http://127.0.0.1:9` and sets `CAELIS_SANDBOX_NETWORK=disabled`.

Codex also adds WFP filters for DNS, DNS-over-TLS, SMB, and ICMP as
defense-in-depth. Caelis can add that later if a target Windows fleet shows
firewall policy drift, but the first implementation relies on broad
per-identity Windows Firewall blocks plus the real external socket E2E below.

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
- workspace-scoped temp/home directories prepared by the runner under
  `.caelis-sandbox`
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

Current UI/prompt cleanup:

- The model-visible shell tool is named `RUN_COMMAND`; shell selection stays in
  the injected environment context rather than the tool description.
- The prompt environment context reports `powershell` on Windows instead of
  trusting POSIX-oriented `SHELL` values.
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
- `Prepare`: optional `ports/sandbox.PreparableRuntime` hook used by
  `caelis sandbox setup` and TUI `/sandbox setup` to run the full elevated setup
  flow explicitly.
- `Close`: terminate live runner transports and jobs owned by this runtime.

Fallback policy:

- Auto backend may fall back to host only when setup or backend construction is
  unavailable and the caller did not explicitly request `windows-elevated`.
- Explicit `windows-elevated` must fail closed if setup or runner spawn fails.
- A command that asks for sandbox route must not silently run on host after a
  backend-specific denial.
- Missing or stale Windows setup is a backend-specific denial, not a lazy setup
  trigger. Ordinary command execution must return actionable setup guidance
  instead of launching UAC.

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

Real-machine E2E is intentionally gated because it creates local users,
refreshes ACLs, and writes Windows Firewall policy:

```powershell
$env:CAELIS_WINDOWS_SANDBOX_E2E = '1'
$env:CAELIS_WINDOWS_SANDBOX_E2E_HELPER = 'C:\path\to\caelis.exe'
$env:CAELIS_WINDOWS_SANDBOX_E2E_STATE = 'C:\path\to\isolated\state'
go test ./impl/sandbox/windows -run TestWindowsElevatedSandboxE2E -count=1 -timeout 300s -v
```

Place `caelis-command-runner.exe` next to that helper to exercise the
lightweight command-runner path. Without it, local E2E falls back to the
helper-capable `caelis.exe` internal runner path.

Windows npm platform packages must stage `runtime/caelis.exe`,
`runtime/caelis-command-runner.exe`, and
`runtime/caelis-windows-sandbox-setup.exe` from the Windows release archive.
The launcher still starts `caelis.exe`; the sandbox runtime discovers the
sibling command runner from that install directory.

The current E2E covers workspace file write/read, PowerShell command execution,
execution of real Windows developer tools (`go.exe`, `git.exe`, `npm.cmd`, and
nested `powershell.exe`) when standard installs are present, default offline
network environment behavior, a real external TCP probe where the offline
identity is denied, hidden path denial,
capability SID restricted-token attachment, explicit setup, and async session
wait/result. It passed on the local Windows development machine on 2026-05-18
using a helper built from this worktree.

Do not use a listener bound to the same host's non-loopback address as the
primary no-network proof. On Windows that can exercise a same-machine local path
instead of the outbound path that the offline firewall rule is meant to prove.
Use a separate host or the gated external probe above.

Unit tests:

- backend normalization aliases
- Windows candidate backend selection
- path canonicalization and case-insensitive dedupe
- policy mapping from constraints to roots
- setup marker stale/current decisions
- missing setup fails closed without UAC and includes `/sandbox setup` guidance
- runner framed protocol encode/decode
- `.cmd` executable discovery in tests
- no-op directory fsync on Windows

Integration tests on Windows:

- setup creates users, group, marker, bin dir, and secrets dir
- non-elevated setup refresh is idempotent
- per-command ACL refresh runs without elevation after setup is complete
- workspace write can create and edit files inside workspace
- write outside workspace is denied
- read of hidden path is denied
- write of read-only subpath is denied
- network disabled cannot reach external endpoints
- stdin works
- stdout and stderr stream independently without TTY
- TTY session uses ConPTY and receives resize events
- timeout kills the full process tree
- terminate kills background grandchildren via Job Object
- explicit `windows-elevated` fails closed if setup is missing
- TUI startup and `/status` show setup-required guidance
- TUI `/sandbox setup` invokes the explicit setup path
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
- [x] Add a shared cross-package Windows test helper for home and executable
  suffix handling if more packages need it.
- [x] Audit remaining `os.UserHomeDir` tests for Windows env assumptions.
- [x] Audit shell-based tests for POSIX-only snippets.

### Phase 2: Win32 Foundation

- [x] Add `internal/win32` wrappers for SID lookup and string conversion.
- [x] Add token restriction wrappers.
- [x] Add capability SID wrappers.
- [x] Add ACL read/modify/write wrappers.
- [x] Add `ShellExecuteExW` wrapper for elevated helper launch.
- [x] Add logon/profile process launch wrappers.
- [x] Add named pipe or anonymous pipe transport wrappers.
- [x] Add Job Object wrapper for process tree cleanup.
- [x] Add ConPTY wrapper with resize support.
- [x] Add firewall rule refresh wrapper.

### Phase 3: Setup Helper

- [x] Create `cmd/caelis-windows-sandbox-setup`.
- [x] Define setup payload schema and version.
- [x] Materialize helper binary into `.sandbox-bin`.
- [x] Implement UAC launch from parent runtime.
- [x] Create `CaelisSandboxUsers`.
- [x] Create or update the offline sandbox user.
- [x] Store credentials with DPAPI.
- [x] ACL `.sandbox`, `.sandbox-bin`, and `.sandbox-secrets`.
- [x] Apply read/write/hidden root ACL refresh.
- [x] Apply network policy refresh.
- [x] Write setup marker atomically.
- [x] Write sanitized setup error reports.

### Phase 4: Command Runner

- [x] Create `cmd/caelis-command-runner`.
- [x] Define runner framed protocol.
- [x] Implement parent-runner handshake.
- [x] Implement restricted-token helpers and a debug-controllable capability SID
  attach path.
- [x] Persist random per-workspace/per-write-root capability SIDs and grant
  matching write-root ACLs.
- [x] Make capability SID attachment the default after validating the current
  PowerShell workload E2E on real Windows.
- [x] Expand the real-machine capability-token matrix to common Windows
  developer tools such as `go`, `git`, `npm.cmd`, and nested PowerShell.
- [x] Launch the command runner as the configured sandbox user.
- [x] Implement non-TTY pipes.
- [x] Implement ConPTY mode.
- [x] Implement stdin, interrupt, kill, and timeout.
- [x] Implement resize handling for ConPTY.
- [x] Assign children to a Job Object.
- [x] Stream stdout/stderr into `sandbox.Session`.
- [x] Return structured exit status and errors.

### Phase 5: Windows Runtime

- [x] Implement `impl/sandbox/windows.Runtime.Describe`.
- [x] Implement setup freshness checks.
- [x] Implement explicit setup orchestration.
- [x] Keep full setup marker independent from workspace roots and local runner
  hash churn so setup is normally a one-time user-triggered action.
- [x] Implement non-elevated per-command ACL refresh after setup is complete.
- [x] Implement `Run`.
- [x] Implement `Start`.
- [x] Implement live session registry.
- [x] Implement `OpenSession` and `OpenSessionRef`.
- [x] Implement `Close`.
- [x] Expose setup failure hints through `sandbox.Status`.
- [x] Ensure normal command execution fails closed instead of launching UAC when
  setup is missing or stale.
- [x] Preserve explicit-backend fail-closed behavior.

### Phase 6: Policy and Filesystem

- [x] Implement Windows path canonicalization.
- [x] Implement case-insensitive root dedupe.
- [x] Map `sandbox.Config` and `sandbox.Constraints` to Windows roots.
- [x] Support `PathAccessHidden` as deny-read and deny-write.
- [x] Protect known user secret directories by default.
- [x] Add policy-aware filesystem reads for tools.
- [x] Add tests for drive roots, UNC roots, and long path prefixes.
- [x] Add tests for short 8.3 paths where the filesystem exposes them.

### Phase 7: Offline Network Control

- [x] Define offline identity semantics.
- [x] Implement Windows Firewall setup for the offline identity, including
  non-loopback and loopback rule scopes.
- [x] Add no-network environment hardening.
- [x] Add local proxy compatibility if needed.
- [x] Add integration tests for disabled network environment mode.
- [x] Add a real socket E2E that proves offline identity outbound denial on a
  reachable external TCP endpoint.

### Phase 8: Diagnostics and UX

- [x] Add doctor checks for Windows Elevated setup.
- [x] Report setup version, marker status, helper hash, runner hash, and
  configured user names.
- [x] Add manual setup through `caelis sandbox setup`.
- [x] Add TUI `/sandbox setup`.
- [x] Show TUI startup and `/status` guidance when setup is not
  ready.
- [x] Stream `/sandbox setup` progress into the TUI viewport and suppress
  PowerShell progress UI from firewall refresh.
- [x] Add clear remediation for UAC cancellation.
- [x] Keep user-visible command transcript copy stable while exposing the
  model-visible shell tool as `RUN_COMMAND`.

### Phase 9: Extraction Decision

- [x] Keep implementation internal until setup, runner, policy, and tests are
  stable.
- [x] Extract only if another repository needs to implement Caelis sandbox
  ports.
- [x] If extracted, keep `ports/sandbox` in Caelis and publish only the concrete
  Windows backend plus helper binaries.
- [x] Version runner protocol and setup payload before extraction.

## Future Hardening

These are not required for the current acceptance target, but they are worth
tracking as the Windows fleet grows:

- Add Codex-style WFP filters for DNS, DNS-over-TLS, SMB, and ICMP if field
  validation shows Windows Firewall policy is insufficient on target machines.

## Acceptance Criteria

The current locally validated subset includes:

- setup and command runner execution through the helper-capable `caelis.exe`;
- explicit setup through `caelis sandbox setup` or TUI `/sandbox setup`, with
  no lazy UAC from normal command execution;
- workspace file creation and host-side readback;
- PowerShell command execution under the sandbox identity;
- real developer tool execution with `go.exe`, `git.exe`, `npm.cmd`, and nested
  `powershell.exe` when they are installed in standard Windows locations;
- capability SID restricted-token attachment on the default non-TTY path;
- offline network environment selection plus external socket denial;
- hidden path denial through ACL refresh;
- async session start, wait, and result collection.

The hardened Windows Elevated backend is complete when:

- A fresh Windows machine can run setup through UAC and then run sandboxed
  commands without manual account preparation.
- TUI startup clearly prompts for `/sandbox setup` when Windows setup is not
  ready.
- TUI `/sandbox setup` shows live progress during account, ACL, and firewall
  setup instead of appearing stuck.
- Dynamic `request_permissions` read/write roots are applied through
  non-elevated ACL refresh and do not force a new elevated setup prompt.
- Workspace-write commands can read required platform/tool roots and write only
  allowed roots.
- Hidden paths cannot be read by sandboxed commands.
- Read-only subpaths cannot be written by sandboxed commands.
- Network-disabled commands cannot reach external network endpoints.
- Capability SID restricted-token attachment is safe as the default for
  PowerShell and common Windows developer tools.
- Timeout and terminate clean the full process tree.
- TTY and non-TTY sessions both work.
- `go test` covers port selection, policy mapping, runner protocol, and Windows
  integration behavior.
- Explicit `windows-elevated` never silently falls back to host execution.
