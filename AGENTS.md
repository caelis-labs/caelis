# AGENTS.md

## Working Style
- Preserve unrelated user changes; check the worktree before broad edits.
- Avoid import aliases unless they disambiguate or match local convention.
- Read nearby docs, package comments, and tests before editing unfamiliar code. For session, gateway, ACP, replay, runtime, control, or surface work, read `docs/architecture.md` first, then the linked normative document for that boundary.

## Architecture and Placement
- Target direction: presentation surfaces -> Control -> Agent Runtime / SDK. Assign ownership by semantics; physical package movement is not an architecture goal.
- Surfaces render ACP-shaped `eventstream.Envelope` values and collect input. They must not own model, tool, sandbox, policy, persistence, replay, or runtime semantics.
- Control owns product orchestration: Agent assembly, lifecycle, permissions, system Agents, endpoint selection, and handoff authorization. Agents may suggest a transition but must not commit one.
- `agent-sdk/*` owns reusable Runtime contracts and implementations. It must not depend on `control/*`, `app/*`, `surfaces/*`, `protocol/acp/*`, product-host `ports/*`, or repository `internal/*` outside the SDK tree.
- Root `protocol/acp/*` owns product wire transport, compatibility, and projection. Reusable normalized ACP semantics may live in the SDK; product wire types must not flow inward.
- Put stable product capabilities in coherent `control/*` packages, reusable contracts in `agent-sdk/*`, and concrete composition glue in the Host-owned private implementation tree. Do not recreate the retired `ports/*` tree or turn a private facade or Surface API into a second product API.
- Treat current package locations as migration evidence, not future precedent. Long-term, `app/*` owns Host composition, private concrete components, and thin transport or in-process adapters; it is not a second product-semantics layer.
- Keep process-lifetime Host authorities, activated Session Runtime instances, and stateless Runtime assembly factories as distinct concepts. Adapters depend on focused Control contracts rather than a concrete Host, and lower layers never depend outward on composition.
- `control/appserver` owns the aggregate product client contract, including the independently delivered Task observation capability. Session feed/replay, approval recovery, and the lifecycle write gate stay on their existing authoritative paths; Task semantics belong to `control/taskstream`. Main-Turn ingress remains private in `internal/controlclient/turningress`. Surfaces own neither stream discovery nor replay.
- `app/gatewayapp/controladapter` is Host-private server assembly, not a second product API. Its root package must not depend on the concrete `gatewayapp.Stack`; only `local.NewAppServer` may receive that Stack as the composition root. Local leaf services consume focused Host services or services selected from an authorized Session Runtime lease and bind principal-sensitive capabilities. Other production packages use `control/appserver` or focused Control contracts.
- The external-ACP `ControlPlane` owns its shared Agent registry and replacement operation. Do not expose the registry or a mutable updater through `internal/controlassembly` or another cross-package dependency bag.
- `internal/kernel` owns product Session/Turn coordination, not reusable SDK or ACP helper facades. Consume `agent-sdk` approval/usage semantics and `protocol/acp/metautil` directly instead of mirroring them.
- Session Runtime registries, instances, assemblers, and assembly dependency snapshots must not retain a concrete Host `*gatewayapp.Stack`, the Host root `runtimeComposition`, or bound methods that capture either. Capture explicit process authorities and sample mutable process configuration through an independent source instead.
- The Host `runtimeProcessConfigSource` is the sole mutable owner of process Runtime inputs. The root `activeRuntime` is an installed execution artifact, and detached Runtime values are activation-pinned snapshots; neither is a parallel publication target. Plugin configuration remains canonical AppConfig state, and detached Runtimes expose only its read projection.
- `gatewayapp.Stack` must own Runtime composition through a named private field. Do not anonymously embed `runtimeComposition` into `Stack` or export the composition's fields; cross the Host boundary only through deliberate focused service getters. Do not recreate a wide function bag such as the retired `ControlRuntimeView`, or add direct Stack mirrors for execution, configuration revision, participant-handle projection, mutable Plugin services, or methods already owned by Model, Agent, Status, Runtime, workspace, preparation, or message-delivery services.
- Keep one semantic owner, one authoritative data path, and one durable source of truth. Typed Envelope fields own identity, relation, position, approval, and resume semantics; `_meta` is display/debug unless a maintained contract says otherwise.
- Fence semantic Session writes for the complete producer lifetime. Never retry `ErrLeaseConflict` through an unfenced path; durable State repair requires an explicit revision-checked guarded mutation.
- Dynamic orchestration belongs to Control. Do not add a deterministic workflow graph/node engine or let an Agent authorize its own handoff.

## Code Quality
- Follow existing boundaries, helpers, and tests; scope edits to changed behavior.
- Add abstractions only when they remove real complexity or match an established pattern.
- Avoid growing central orchestration files. For coherent features in large/high-touch files, prefer a nearby module with docs and tests.
- When replacing a path, remove superseded code, mirrors, wrappers, tests, and docs. A required compatibility path must have a documented owner, scope, and removal condition.
- Document new exported types, interfaces, and non-obvious contracts.
- Normalize external ACP input before storage; keep transient UI/subagent traces out of durable parent context unless carried by canonical payloads.

## Validation
- Run `gofmt` on touched Go files, focused `go test` packages for changed behavior, and `git diff --check`.
- Before committing, run `make commit-check`; it includes formatting, `golangci-lint`, `arch-lint`, the SDK package-boundary gate, vet, tests, and build.
- Run `make arch-lint` after import, package ownership, gateway/eventstream, or session protocol changes.
- Run `make client-protocol-check` after changing OpenAPI, generated clients, Envelope wire shapes, or `control/appserver` JSON contracts.
- Lease, concurrency, persistence, broker, or lifecycle changes require focused `go test -race` coverage in the change that needs it; do not turn that into an unconditional release-time rerun.
- Persistence or replay changes need round-trip tests comparing rebuilt model context with runtime-produced context.
- Projection/UI reload tests do not replace model-context round-trip tests.
- UI or text-output changes should include/update golden or regression coverage and review the rendered/output diff.
- Tests should prefer whole-object/event comparisons and structured helpers over field-by-field assertions or ad hoc JSON/string digging.
- Use `make regression` when projection, TUI behavior, command execution, or ACP integration changes broadly.
- Run `make docs-links` after adding, removing, or renaming maintained documentation.
- Run `npm --prefix npm test` after changing the npm launcher, update handoff, package manifests, or release scripts. Use `make release-dry-run` only when packaging or release assembly needs direct evidence, then verify it left the tracked worktree unchanged.

## Release
- Keep release mechanics in `docs/release.md`; update that doc when the process changes.
- When asked to release, follow `docs/release.md` and verify the worktree contains only intended changes.
- Keep normative contracts and current limitations in maintained docs; keep completed implementation plans and acceptance history in Git, tests, tags, and CI rather than a permanent Roadmap document.
