---
name: caelis-validate-change
description: Select and, when authorized, run and report the evidence required for a Caelis change. Use before commit or push, after a merge or rebase, when verifying an implementation, or when deciding which focused Go, race, architecture, protocol, persistence, product-regression, documentation, build, or release checks apply; do not repair failures unless authorized, and never claim unrun gates.
---

# Validate A Caelis Change

Match evidence to the outgoing behavior. When the request is advisory or asks
which checks apply, select and report without executing them. When validation
execution is requested or is part of an authorized implementation, run each
selected check once for the source state it proves. Never substitute a green
nearby test for the owning entry path or present pending and unrun checks as
passed.

## Inspect The Outgoing Change

1. Confirm the repository, branch, worktree, and exact base and head. Include
   staged, unstaged, untracked, generated, and deleted paths when they are part
   of the requested change.
2. After a retarget, merge, rebase, generated-file refresh, or test-driven edit,
   recalculate the affected scope and invalidate evidence whose source changed.
3. Read applicable `AGENTS.md`, current build entrypoints, CI workflows, and
   repository task definitions. Discover present gate names and ownership rather
   than relying on remembered commands or documentation filenames.

## Select Evidence

Select verification for Go formatting, focused owning tests, and diff
whitespace. Apply a formatter only when edits are authorized, and execute the
selected checks only under the authority established above. Add the narrowest
evidence that covers each affected rule:

- imports, package placement, gateways, event streams, or ownership: the current
  architecture and package-boundary gates;
- public wire values, generated clients, Envelope shapes, or JSON contracts:
  the current protocol generation and compatibility gates;
- leases, concurrency, persistence, brokers, processes, or lifecycle: focused
  race coverage under the real contention or shutdown condition;
- persistence, replay, compaction, or resume: round-trip rebuilt model context
  against runtime-produced context, not only projection or UI reload;
- TUI, projection, command execution, ACP integration, prompts, diagnostics, or
  visible text: owning golden/regression coverage and review of rendered output;
- exported packages, binaries, configuration, packaging, or startup paths: the
  relevant build or installed-entry smoke;
- maintained prose moves or links: the current documentation/link gate.

Use production-shaped paths with real workspaces, processes, Stores, Control
clients, and physical terminal or native-platform evidence when the contract
depends on them. Cross-compilation is not native Windows evidence.

## Respect Checkpoints

- During implementation, prefer focused checks and add broader gates only when
  the affected boundary makes them credible.
- Before committing, run the aggregate gate required by the current repository
  instructions. Inspect files changed by formatters or generators before using
  its result.
- Before pushing, do not rerun unchanged passing evidence solely because a
  commit was created; verify that it still applies to the exact outgoing head.
- After rewritten history, re-read remote heads, review state, and affected
  checks. Use lease-protected pushes only when the user authorized the rewrite;
  never use raw force.
- For a release, follow the current release authority and exact-SHA CI model.
  Do not invent a local release suite or repeat broad gates merely for a tag.

## Handle And Report Results

Stop on a relevant failure unless the user authorized a fix. For a suspected
environment failure, record the exact command and error, prove the environment
constraint, and rerun unchanged in an appropriate environment before calling
the product broken or the check passed.

Report the resolved scope and head, commands actually run, what each proves,
pass/fail/pending state, invalidated earlier evidence, and residual physical,
platform, credential, or CI limitations.
