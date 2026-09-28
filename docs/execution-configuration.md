# Execution Configuration

`execution-configuration-v1` in `GET /api/control/v1/initialize` advertises
creation-bound process configuration for native Sessions. Require this capability
before sending `execution_config`; older Hosts do not support it. The public
schema is [OpenAPI](../api/control/v1/openapi.json), with supported
[Go HTTP clients](../control/appserver/httpclient) and
[TypeScript types](../clients/typescript/control-v1.gen.ts).

## Scope and assembly

An ordinary `POST /sessions` request accepts `execution_config` alongside its
existing `cwd`. An [Application Session](application-runtime.md) accepts the same
object at `profile.execution_config`, alongside `profile.workspace.cwd` and
`execution: "workspace-write"`. Tools-only profiles reject non-null process
configuration. The SDK owner is `sandbox.ExecutionConfig`, supplied through
`sandbox.Config.Execution` by an embedding Host. SDK embeddings using Seatbelt
must dispatch `seatbelt.MaybeRunInternalHelper(os.Args[1:])` before their normal
argument handling, or supply a `Config.HelperPath` executable that implements
that helper. The Caelis CLI supplies this entry point. The helper receives target
environment bytes through a private inherited descriptor after confinement;
values do not enter its arguments. A mandatory read ceiling must explicitly admit
the helper executable; selecting it does not widen the ceiling.

For example, an application can select a user environment and an independent
working directory:

```json
{
  "execution": "workspace-write",
  "workspace": {"cwd": "/work/notebook"},
  "execution_config": {
    "environment": {
      "inherit": true,
      "set": {
        "HOME": "/home/alice",
        "PATH": "/home/alice/bin:/usr/local/bin:/usr/bin:/bin",
        "ZDOTDIR": "/home/alice/.config/zsh"
      },
      "unset": ["UNWANTED_VARIABLE"]
    },
    "shell": {"path": "/bin/zsh", "login": false}
  }
}
```

This is a profile fragment; creation also requires the application's model, role,
tool catalog and operation fields. There is no application-name-specific policy.
CWD never selects HOME, TMPDIR or ZDOTDIR. Relative per-command workdirs resolve
under the Session's workspace and remain subject to its existing directory
policy. Environment inheritance grants no filesystem access and does not require
full access. Shell initialization and CLI configuration files are readable only
where the selected sandbox permits them; writes still require existing grants
or per-call Host approval.

The configuration applies to native command Run, asynchronous Start, and TTY
execution, including the Host route after approval. Later input reaches the same
process; it does not reinitialize its environment. Approval, cancellation,
application leases and revocation remain independent authorities. An ordinary
Session initially selecting an external ACP controller rejects non-null
`execution_config`, because the Host cannot impose this contract on that Agent.
A later handoff retains the configuration for native work but does not configure
the external Agent's processes. MCP servers, application callbacks and ACP
endpoint processes keep their own configuration contracts.

## Defaults and precedence

- Missing or JSON `null` `execution_config` means the defaults. An empty object
  does too. Missing/null `environment` and `shell` mean their defaults.
- `environment.inherit` defaults to `true`; missing or null has the same meaning.
  `false` starts with no inherited variables. To request an empty process
  environment, use `{"environment":{"inherit":false}}`; `set: {}` alone does
  **not** disable inheritance.
- Missing/null/empty `set` and `unset` collections add no changes. An empty string
  in `set` is a present variable with an empty value, not deletion. Individual
  `set` values must be strings; a null value is invalid.
- At Runtime activation, Control snapshots the Host environment, excluding its
  private `CAELIS_CONTROL_{TOKEN,TOKEN_FILE,URL}` and
  `CAELIS_COLLABORATION_{TOKEN,URL,TOOLS}` connection fields. The SDK can instead
  receive an explicit `Config.BaseEnv`; a non-nil empty slice is an empty base.
- Resolution is: inherited base (unless disabled), configuration `unset`,
  configuration `set`, command `UnsetEnv`, command `Env`. A set wins over an unset
  in the same scope. No process-wide `os.Setenv` is involved. Environment names
  are case-insensitive on Windows. Invalid names or NUL values are rejected
  without including environment values in validation diagnostics. Unix sandbox
  launchers receive a separate empty environment; the target environment is
  installed only after confinement, so loader variables cannot run code before
  sandbox policy is active.
- Unix defaults to `/bin/bash -c`: non-interactive, non-login. Missing/null shell
  scalar fields select these defaults. Explicit `shell.path` must be an absolute
  POSIX-compatible shell executable; `login: true` selects `-lc`. Core does not
  source dotfiles itself or restore PATH after
  the shell starts. Shell-native startup rules still apply (including zsh's
  `.zshenv` and Bash's `BASH_ENV`); selecting login initialization allows profiles
  to change PATH and other variables. Core does not add a second login shell.
- Windows retains its platform PowerShell execution contract. Its native backend
  adds existing temporary/cache, Python site customization and system-directory
  defaults to the inherited base, before explicit configuration and command
  overrides. `inherit: false` discards those defaults too. Replacing or unsetting
  `PYTHONPATH` removes the bundled Python site customization; Python 3.13+ temp
  directories can then fail under the Windows restricted token because their
  private DACLs do not grant the sandbox write SID. Overriding temporary/cache
  paths likewise does not grant filesystem access to those paths. Custom shell
  paths and login initialization are rejected rather than silently ignored.
  Mandatory SDK resource-limit runtimes reject login initialization.

These rules configure the supplied environment. The shell and operating system
can subsequently create their own variables, such as PWD and SHLVL. A shell can
also require particular variables to start: Windows PowerShell needs a valid
`SystemRoot` to load its CLR. With `inherit: false` on the Windows sandbox, callers
must explicitly set `SystemRoot` (normally the Host's Windows directory); an empty
environment is passed as requested but cannot start Windows PowerShell. The
backend does not silently restore removed variables.

Clients omitting the field inherit the Host user environment and use the default
non-login shell. This includes application clients: there is no automatic HOME
redirection or fixed toolchain PATH.

## Durability and confidentiality

The explicit configuration is pinned in the canonical Session at creation and
used for both ordinary and application activation. The application profile keeps
the matching immutable creation snapshot. Authorized Session and application
configuration readback retain it across reactivation and Host restart. It is not
a hot application configuration field; changing it requires a new Session. Operation retries include it in creation identity. Existing Sessions
without a stored value follow the defaults.

The inherited environment itself is not persisted. An active Runtime keeps its
activation snapshot; a later activation, including after restart, samples that
Host's current environment and reapplies the pinned configuration. For a fixed
client-selected environment across Hosts, use `inherit: false` with an explicit
`set`. Connecting to a running Host never replaces its environment or restarts
it to adopt the connecting client's environment.

Explicit environment values are durable configuration, not a secret store. Send
only values appropriate for the Session's authorized readers and owner-only
Store; do not place credentials in examples, ordinary diagnostics or logs.
Neither the environment map nor shell configuration is model context. Commands
can of course print their own environment, and their output follows the normal
Session history and Task output contracts. Environment filtering is credential
hygiene, not process-credential isolation; the platform sandbox's existing trust
model still applies.
