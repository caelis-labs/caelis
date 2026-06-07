# Caelis Rewrite

This directory contains the preparation material for a **complete rewrite** of
Caelis. The current codebase, after 100+ iterations, has reached basic
usability in features and UX, but carries significant technical debt:
code bloat, unclear interface/implementation boundaries, and a runtime core
that is hard to maintain.

The correct approach is **not** incremental refactoring on the current code.
The correct approach is:

1. Define clean architecture, package boundaries, and interfaces first.
2. Rewrite module by module, from the inside out.
3. Validate each module independently before integrating.

Source snapshot:

- Branch: `rewrite`
- Baseline commit: `bb0c960fcc6f82e281d468f9aca0d10c8d186a52`
- Go version: `1.25.1`

## Design References

- [adk-go-patterns.md](adk-go-patterns.md): Go architecture conventions
  extracted from Google's adk-go. Not code reference - structural patterns only.

## Contents

- [current-state.md](current-state.md): delivered features, code size, and the
  maintainability problems that motivate the rewrite.
- [architecture-boundaries.md](architecture-boundaries.md): target package
  structure, dependency rules, and domain responsibility map.
- [builtin-layering.md](builtin-layering.md): built-in implementation layering
  for tools, sandbox-backed execution, tasks, planning, skills, agents, and ACP.
- [session-runtime-contract.md](session-runtime-contract.md): the durable
  session and runtime contract that must be preserved across the rewrite.
- [phase-1-preflight.md](phase-1-preflight.md): checklist for moving from
  architecture design into any skeleton or implementation work.
- [refactor-roadmap.md](refactor-roadmap.md): phased rewrite plan with
  deliverables and exit criteria.
- [validation-plan.md](validation-plan.md): regression verification gates.

## Four-Layer Architecture

```text
Layer 1: Entry        cmd/caelis
Layer 2: Presentation  tui/  headless/  acp/server/
Layer 3: Control       gateway/  gateway/kernel/  app/  app/commands/
Layer 4: Infrastructure
  4a: ACP Protocol     acp/  acp/client/  acp/projector/  acp/terminal/
  4b: Agent Core       session/  model/  tool/  agent/  runner/
                       sandbox/  policy/  skill/  artifact/
```

Dependency direction: Infrastructure ← Control ← Presentation ← Entry.

Three presentation surfaces (TUI, headless CLI, ACP server) share the same
`gateway.Service`. They are peers, not nested layers. Future Web or App
surfaces add to Layer 2 without changing Layers 3 or 4.

`app/` constructs the shared runtime and control plane. It does not import
Presentation packages. `cmd/caelis` owns mode selection and passes the same
`gateway.Service` to the selected Presentation package.

## Principles

1. **Complete rewrite, not incremental refactor.** The current `ports/impl`
   separation adds reading cost without clarity. Interfaces and their default
   implementations should co-locate in domain packages.
2. **Four-layer architecture.** Entry, Presentation, Control, Infrastructure.
   Each layer imports only from layers below it. The entrypoint may import
   Presentation packages for mode selection, but lower layers never import
   Presentation.
3. **Phase 1 is architecture only.** No behavior implementation. Design the
   package tree, interface contracts, built-in layering, dependency rules, and
   validation gates before writing module code.
4. **Module-by-module delivery.** Each module is implemented and validated
   independently. Integration follows.
5. **Inside-out.** Core domain (session, model, tool) first. Runtime loop
   second. Gateway third. Surfaces last.
6. **Provider-neutral contracts.** Do not copy adk-go concrete APIs such as
   `genai.Content` or `genai.Schema` into Caelis architecture docs. Use Caelis
   domain types and keep provider adapters below `model/providers/`.
7. **Keep this pack current without growing it.** When a module is redesigned,
   update the owning doc in the same change. Do not add a new planning doc
   unless it replaces an existing one and is linked from this README.

## Documentation Boundaries

This directory is intentionally small:

- architecture and package ownership: `architecture-boundaries.md`
- built-in placement rules: `builtin-layering.md`
- durable runtime contract: `session-runtime-contract.md`
- phase gate: `phase-1-preflight.md`
- delivery order: `refactor-roadmap.md`
- verification matrix: `validation-plan.md`
- baseline facts: `current-state.md`
- external style reference: `adk-go-patterns.md`

Implementation notes belong in package docs or tests, not in new planning
documents under `docs/refactor/`.

## Document Cleanup

These docs replace older planning artifacts:

- `docs/agent-sdk-acp-architecture-plan.md` - removed, content absorbed into
  session-runtime-contract.md and architecture-boundaries.md.
- `docs/windows-elevated-sandbox.md` - removed, replaced by
  [../windows-workspace-write-sandbox.md](../windows-workspace-write-sandbox.md).
