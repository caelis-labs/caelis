# Changelog

## [0.75.0](https://github.com/caelis-labs/caelis/compare/v0.74.0...v0.75.0) (2026-10-30)


### ⚠ BREAKING CHANGES

* Valid legacy Application reviewer overrides remain creation evidence but no longer select execution models. Older Bot clients that compare effective reviewer-state to that historical field may reject this Core. New Bot requires runtime-agent-team-v1 and rejects older Core, including busy Hosts awaiting idle replacement. Upgrade compatible Bot and Runtime together.

### Features

* make Runtime Agent Team authoritative for all sessions ([#181](https://github.com/caelis-labs/caelis/issues/181)) ([b8132a5](https://github.com/caelis-labs/caelis/commit/b8132a5299f5133bb85d5a9c6bd155d0d1b5b5a8))


### Bug Fixes

* **acp:** complete SDK v1.5.2 and draft alpha.10 migration ([#180](https://github.com/caelis-labs/caelis/issues/180)) ([7b88f78](https://github.com/caelis-labs/caelis/commit/7b88f78427bba37506d3bf58711409e0eda9a886))
* **control:** expose only caller-resolvable approval heads ([#179](https://github.com/caelis-labs/caelis/issues/179)) ([0d87488](https://github.com/caelis-labs/caelis/commit/0d8748800bdd37064f140cf30b181fba1b3438c2))
* normalize chat history before binding request coverage ([#176](https://github.com/caelis-labs/caelis/issues/176)) ([9108d26](https://github.com/caelis-labs/caelis/commit/9108d267f64cfc5a9030e3fb21630e28d67b109a))
* **sandbox:** grant GOPATH/pkg/sumdb to sandboxed Go commands ([#178](https://github.com/caelis-labs/caelis/issues/178)) ([3821e32](https://github.com/caelis-labs/caelis/commit/3821e323b4a957895922b09503c877e87350f53e))

## [0.74.0](https://github.com/caelis-labs/caelis/compare/v0.73.3...v0.74.0) (2026-10-28)


### Features

* Preserve the actual in-context request prefix and complete tool batches during compaction. Reject truncated, filtered and unknown terminal responses without retrying or adopting an incomplete summary ([#170](https://github.com/caelis-labs/caelis/pull/170)).
* Offer independent `async_compact` and `idle_compact` opt-ins, both off by default. Idle compaction commits a checkpoint after a completed Turn remains idle for 60 seconds; the Host coordinates foreground preemption, shutdown and recovery. Saved settings apply at the next Runtime activation ([#170](https://github.com/caelis-labs/caelis/pull/170)).
* Always refresh MCP catalogs and retire unused tools after a durable canonical compact. The former `mcp_catalog_refresh` and `deferred_tool_retirement` settings are accepted and ignored for compatibility; they no longer appear in discovery or the editor ([#170](https://github.com/caelis-labs/caelis/pull/170)).
* Add cache breakpoints for the native Anthropic API and expose cache read/write presence and 5-minute/1-hour write breakdowns. Compatible routes do not acquire native breakpoints based on model names. Missing measurements remain distinct from zero, and TTL breakdowns do not increase totals ([#170](https://github.com/caelis-labs/caelis/pull/170)).

### Bug Fixes

* Keep Session status and diagnostics reads from canceling idle compaction while retaining Runtime lifetime during the read. Foreground work, configuration and shutdown retain their existing preemption authority ([#173](https://github.com/caelis-labs/caelis/pull/173)).
* Preserve the API's cache accounting basis through provider normalization. Context usage includes cache reads exactly once for native and compatible Anthropic routes and inclusive Responses usage, while retaining accurate cumulative totals and legacy-record compatibility ([#173](https://github.com/caelis-labs/caelis/pull/173)).

* Do not report a successful admission interrupt as an error when cancellation cleanup finishes before the interrupt returns. Preserve independent failures and unknown-outcome receipts ([#174](https://github.com/caelis-labs/caelis/pull/174)).

### Security

* Upgrade `golang.org/x/net` to `v0.62.0`, fixing reachable HTML parsing/rendering vulnerabilities [GO-2026-6860](https://pkg.go.dev/vuln/GO-2026-6860) and [GO-2026-6866](https://pkg.go.dev/vuln/GO-2026-6866) in WebFetch ([#170](https://github.com/caelis-labs/caelis/pull/170)).

## [0.73.3](https://github.com/caelis-labs/caelis/compare/v0.73.2...v0.73.3) (2026-10-27)


### Bug Fixes

* isolate child message outcomes and reconnect TUI output ([#169](https://github.com/caelis-labs/caelis/issues/169)) ([27c992e](https://github.com/caelis-labs/caelis/commit/27c992e643635b0fe88168ab058af4b1b3e3afa9))
* **models:** add GPT-6.1 Terra and refresh native catalog ([#167](https://github.com/caelis-labs/caelis/issues/167)) ([25f9eff](https://github.com/caelis-labs/caelis/commit/25f9efffae5a98532dffe9c36607d88073a57b31))

## [0.73.2](https://github.com/caelis-labs/caelis/compare/v0.73.1...v0.73.2) (2026-10-23)


### Bug Fixes

* Prepare images consistently across providers, preserve long-screenshot detail with overlapping crops and coordinate mappings, and reuse bounded image derivatives across turns. Keep original Session media intact and stop retrying permanent HTTP or Responses stream image errors ([#164](https://github.com/caelis-labs/caelis/pull/164)).
* Allow file edits in the sandbox's system temporary and scratch directories without redundant approval, retaining physical-path and sensitive-path protections ([#164](https://github.com/caelis-labs/caelis/pull/164)).
* Guide background servers and watchers toward immediate status reads and bounded readiness checks instead of waiting for process completion ([#164](https://github.com/caelis-labs/caelis/pull/164)).

### Security

* Require Go 1.26.10, which fixes the standard-library certificate verification issue [GO-2026-6809](https://pkg.go.dev/vuln/GO-2026-6809).

## [0.73.1](https://github.com/caelis-labs/caelis/compare/v0.73.0...v0.73.1) (2026-10-20)


### Bug Fixes

* **plugin:** trust Desktop World MCP sources across upgrades ([#163](https://github.com/caelis-labs/caelis/issues/163)) ([7514902](https://github.com/caelis-labs/caelis/commit/751490232720874a680699f5ad81090cb5e96f7a))
* **sdk:** avoid per-byte reflection in JSON validation and cloning ([#159](https://github.com/caelis-labs/caelis/issues/159)) ([a7e16c0](https://github.com/caelis-labs/caelis/commit/a7e16c09b7ca558c80641e3f0e6b1e621e64ce8c))
* **sdk:** share durable media within session events ([#162](https://github.com/caelis-labs/caelis/issues/162)) ([1e6cd9a](https://github.com/caelis-labs/caelis/commit/1e6cd9ad78070b7b5ba42c81a1416045d7a9bb0c))

## [0.73.0](https://github.com/caelis-labs/caelis/compare/v0.72.2...v0.73.0) (2026-10-20)

### Core Memory retirement

Core no longer opens the embedded Memory appliance, registers `Remember`/`Recall`, or runs Steward and Memory Verifier jobs ([#156](https://github.com/caelis-labs/caelis/pull/156)). **v0.72.2 is the last Core Memory writer; v0.73.0 is the first no-write release.** Existing Memory databases, WAL files, credentials and canonical Session history are retained without migration or deletion. Historical tool results remain readable.

Legacy `memory` configuration is preserved opaquely during unrelated saves. Retired role bindings are ignored when loading configuration and saved sets; direct bind/reset requests return HTTP 400 with a terminal rejected receipt. Compatibility readers remain until the supported upgrade floor excludes those writers and requests. Notebook, summaries, ordinary Session persistence and Bot-owned Memory retain their existing owners.

### Experimental Runtime controls

Three independent options are available through `/experimental` and the capability-negotiated Control API: background compaction, MCP catalog refresh and deferred tool retirement ([#157](https://github.com/caelis-labs/caelis/pull/157)). **All three default to off.** Saved values are Host-wide defaults applied at the next Runtime activation; an existing activation retains its effective values.

Tool retirement occurs only after a confirmed durable canonical compact. A ready summary is not a retirement signal. Configuration, catalog, permission and execution-fence checks guard checkpoint adoption; unknown writes retain their original identity and payload.

These features have deterministic Runtime, protocol, persistent replay and rendered-terminal regression coverage. Summary-model semantic fidelity, provider cache hits, monetary savings and production performance have not been established.

### Fixes and dependencies

- Drain native Windows sandbox refresh work before releasing Runtime ownership ([#152](https://github.com/caelis-labs/caelis/pull/152)).
- Update Anthropic Go SDK to 1.82.0, Gemini Go SDK to 1.75.0 and the VT test emulator to 0.1.0. Keep Gemini SDK automatic continuation disabled so additional billed generations cannot bypass the existing invocation boundary; preserve original stream errors and usage receipts ([#155](https://github.com/caelis-labs/caelis/pull/155)).

### Regression coverage

- Wait for authoritative Turn completion after TUI rendering ([#151](https://github.com/caelis-labs/caelis/pull/151)).
- Validate the current Desktop World single-exec Plugin contract with isolated fixtures ([#154](https://github.com/caelis-labs/caelis/pull/154)). This does not establish real desktop input or native GUI acceptance.

## [0.72.2](https://github.com/caelis-labs/caelis/compare/v0.72.1...v0.72.2) (2026-10-16)


### Bug Fixes

* keep CLI usable while shared Hosts upgrade ([#149](https://github.com/caelis-labs/caelis/issues/149)) ([5740fdc](https://github.com/caelis-labs/caelis/commit/5740fdcda75d966ac4873921375a24d4dfa7b486))

## [0.72.1](https://github.com/caelis-labs/caelis/compare/v0.72.0...v0.72.1) (2026-10-16)


### Bug Fixes

* tolerate npm propagation delays and publish platforms concurrently ([#147](https://github.com/caelis-labs/caelis/issues/147)) ([2aa6e5d](https://github.com/caelis-labs/caelis/commit/2aa6e5db17e616e1d54b029fbf778920494af78c))

## [0.72.0](https://github.com/caelis-labs/caelis/compare/v0.71.2...v0.72.0) (2026-10-15)


### ⚠ BREAKING CHANGES

* Remove Application registration, credentials, leases, routes and wire capabilities. Migrate Control Store schema 1/2 to 3 and reject legacy writers. Old Application clients must upgrade; a pre-unification Host needs one explicit restart after active work completes.

### Unified Control protocol

TUI and Bot now share one public OpenAPI schema, Host identity, Session/Turn owner and operation receipt contract. Explicit Session profiles retain application instructions, native tools, MCP/Skills, private working directories, workers, hot configuration, approvals, background grants and immutable resources. Unknown effects recover through their original receipts and are never replayed with replacement IDs.

**Breaking client change:** the independent Application protocol, registration, credentials, leases and capabilities are removed. Use a migrated Bot build from caelis-labs/caelis-bot#178; old Application clients must upgrade. The released v0.71.2 line is the last Application writer; this release is the first unified-only writer. CLI embedding remains Bot #165.

**Store upgrade:** Control Store schema 1/2 migrates atomically to schema 3. Original Session/call/resource identities, immutable request bytes and uncertain outcomes are retained; legacy receipts are inert. Back up the complete stopped Store before the first launch. Older binaries cannot write schema 3, and executable downgrade does not undo migration. The isolated importer can be removed when the minimum supported Store reaches v3; archived evidence has independent retention.

**Host replacement:** managed startup can replace an idle Host while Bot/TUI remain subscribed, but it preserves active work. Existing pre-unification Hosts need one explicit `caelis service restart --store-dir <Store>` after their work finishes. Installation alone does not switch a running Host. Compatible clients continue to share one Store writer.

Validation includes full Core/Bot suites, native process integration and restart, isolated macOS onboarding/recovery with a synthetic provider, and separate real DeepSeek `deepseek-flash` acceptance for files, resources, approvals, workers, background grants and request-level hot configuration. Live Fast behavior and Windows GUI acceptance are outside this verification.

### Features

* unify application surfaces on public Control sessions ([#146](https://github.com/caelis-labs/caelis/issues/146)) ([1497727](https://github.com/caelis-labs/caelis/commit/14977276308485baa3ec12453b69372fc42831d6))


### Bug Fixes

* wait for npm publication metadata propagation ([#144](https://github.com/caelis-labs/caelis/issues/144)) ([a9e69ba](https://github.com/caelis-labs/caelis/commit/a9e69ba0e0f4c3e05df94b812ec6eca1783baaec))

## [0.71.2](https://github.com/caelis-labs/caelis/compare/v0.71.1...v0.71.2) (2026-10-15)


### Bug Fixes

* **control:** converge shared schema and application receipts ([#142](https://github.com/caelis-labs/caelis/issues/142)) ([105fd49](https://github.com/caelis-labs/caelis/commit/105fd491f9ac0904c1a118add70775d96cbd6f39))
* restore Developer ID signing and recover v0.71.1 release ([#139](https://github.com/caelis-labs/caelis/issues/139)) ([40baada](https://github.com/caelis-labs/caelis/commit/40baada2990c9315d15bacab45990c4876a73b32))
* resume v0.71.1 publication from accepted signing artifacts ([#141](https://github.com/caelis-labs/caelis/issues/141)) ([bd5f84c](https://github.com/caelis-labs/caelis/commit/bd5f84c6e826e69ec293b90356817e001d7b7dde))

## [0.71.1](https://github.com/caelis-labs/caelis/compare/v0.71.0...v0.71.1) (2026-10-13)


### Bug Fixes

* **guardian:** tolerate explanatory assessment fields ([#134](https://github.com/caelis-labs/caelis/issues/134)) ([13083a3](https://github.com/caelis-labs/caelis/commit/13083a3a712c8c5574ec49f8ca833d6317d66fa6)), closes [#133](https://github.com/caelis-labs/caelis/issues/133)
* isolate extensions and make signed releases retry-safe ([#136](https://github.com/caelis-labs/caelis/issues/136)) ([08ae885](https://github.com/caelis-labs/caelis/commit/08ae8851aa094ed9ec66aced6e7f59de65d3bab3))

## [0.71.0](https://github.com/caelis-labs/caelis/compare/v0.70.0...v0.71.0) (2026-10-12)


### Features

* **cli:** expose headless local plugin lifecycle ([#131](https://github.com/caelis-labs/caelis/issues/131)) ([c493a5e](https://github.com/caelis-labs/caelis/commit/c493a5e581979d8403ad916dc5f02f389d76d3f1)), closes [#129](https://github.com/caelis-labs/caelis/issues/129)
* **model:** unify connection onboarding and separate route labels ([#130](https://github.com/caelis-labs/caelis/issues/130)) ([14128ca](https://github.com/caelis-labs/caelis/commit/14128ca9e8c53913f2d7398d566a319ec2b2dbcb))


### Bug Fixes

* **models:** align Sol context and refresh recommendations ([5ae94c2](https://github.com/caelis-labs/caelis/commit/5ae94c270de4e9b2ace088591812e50e585cfaaf))
* **models:** refresh context defaults and provider catalogs ([a850c50](https://github.com/caelis-labs/caelis/commit/a850c509bb3ecec4a0004546b50a53a1c2939307))
* **models:** refresh routed metadata and thinking contracts ([ab7a269](https://github.com/caelis-labs/caelis/commit/ab7a2698d6d6cdf81e21149386aee0a37de98023))
* resolve connection review findings and trim legacy support ([2af0787](https://github.com/caelis-labs/caelis/commit/2af07870ef2923a07bf775ba37bfed1114e3a13f))
* **tui:** avoid duplicate child summaries and require model checks ([#132](https://github.com/caelis-labs/caelis/issues/132)) ([24608a3](https://github.com/caelis-labs/caelis/commit/24608a394f0087b9a19fd5256a74e73d9712b430))

## [0.70.0](https://github.com/caelis-labs/caelis/compare/v0.69.1...v0.70.0) (2026-10-11)


### Features

* add pinned ACP v2 and shared Application projections ([dc4cbc8](https://github.com/caelis-labs/caelis/commit/dc4cbc8a39286ba923d1a92562a3a950d90231f5))


### Bug Fixes

* preserve draft session revisions and structured tool projections ([bff506c](https://github.com/caelis-labs/caelis/commit/bff506c2f09cf6987be21b5e9b9a1d9c218821cf))

## [0.69.1](https://github.com/caelis-labs/caelis/compare/v0.69.0...v0.69.1) (2026-10-10)


### Bug Fixes

* **runtime:** require scoped approval for MCP calls ([#121](https://github.com/caelis-labs/caelis/issues/121)) ([93fc125](https://github.com/caelis-labs/caelis/commit/93fc1257b9dd1d5db407fdb01f1600c44e5a764a))

## [0.69.0](https://github.com/caelis-labs/caelis/compare/v0.68.0...v0.69.0) (2026-10-09)


### Features

* **application:** complete Turn from terminal callback result ([#117](https://github.com/caelis-labs/caelis/issues/117)) ([b9923a9](https://github.com/caelis-labs/caelis/commit/b9923a9a819ffe08f225d0135a9385f860252fba)), closes [#116](https://github.com/caelis-labs/caelis/issues/116)
* **mcp:** expose accepted tool descriptions in status ([#114](https://github.com/caelis-labs/caelis/issues/114)) ([fc40797](https://github.com/caelis-labs/caelis/commit/fc407972e504e95e92fcc2009b1660a8a30e178a))
* **plugin:** Agent Plugins 1.0 MCP compatibility and bounded tool media ([#119](https://github.com/caelis-labs/caelis/issues/119)) ([8e5d75b](https://github.com/caelis-labs/caelis/commit/8e5d75b197ea5d398ec603b14656462a24389b4b))

  Plugin MCP results now retain bounded PNG/JPEG images and structured receipts. The official Go MCP SDK is pinned to its unreleased upstream commit `3785c500b581` (`v1.8.1-0.20260916072216-3785c500b581`) to preserve large JSON integers. MCP OAuth client sign-in remains deferred until Core has a consent callback and protected token store.


### Bug Fixes

* stream ToolSearch and cover full scoped MCP catalogs ([#111](https://github.com/caelis-labs/caelis/issues/111)) ([073a662](https://github.com/caelis-labs/caelis/commit/073a662ae2d6e39de33314dffe4d3c7a30b70684))
* **toolsearch:** handle bounded parallel schema inspections ([#118](https://github.com/caelis-labs/caelis/issues/118)) ([6cf3bc9](https://github.com/caelis-labs/caelis/commit/6cf3bc978349556099ad8c5b3da7e33f07203009))

## [0.68.0](https://github.com/caelis-labs/caelis/compare/v0.67.2...v0.68.0) (2026-10-08)


### Features

* **toolsearch:** add restricted semantic discovery ([#108](https://github.com/caelis-labs/caelis/issues/108)) ([75a8051](https://github.com/caelis-labs/caelis/commit/75a80511dfa887c406340f4c531e1963e3ab4034))

## [0.67.2](https://github.com/caelis-labs/caelis/compare/v0.67.1...v0.67.2) (2026-10-08)


### Bug Fixes

* enable native Windows application execution ([#105](https://github.com/caelis-labs/caelis/issues/105)) ([c2de3c8](https://github.com/caelis-labs/caelis/commit/c2de3c823cb6a4271a174ba5aa2ae08f5cb1c50e))

## [0.67.1](https://github.com/caelis-labs/caelis/compare/v0.67.0...v0.67.1) (2026-10-07)


### Bug Fixes

* **application:** publish atomic capability fields in v1 schema ([da36cd9](https://github.com/caelis-labs/caelis/commit/da36cd966d90e226acb8ece899beaec8cf9d6a80))

## [0.67.0](https://github.com/caelis-labs/caelis/compare/v0.66.0...v0.67.0) (2026-10-07)


### Features

* **application:** assemble explicit MCP and Skills per revision ([f9698de](https://github.com/caelis-labs/caelis/commit/f9698de1f8f5d3fafe4277b85921e9025098a850))


### Bug Fixes

* **gatewayapp:** retry idle Runtime retirement after Task read errors ([#99](https://github.com/caelis-labs/caelis/issues/99)) ([454f0e8](https://github.com/caelis-labs/caelis/commit/454f0e89aa61e0302c08f217ecf616d0c4682e1d))

## [0.66.0](https://github.com/caelis-labs/caelis/compare/v0.65.0...v0.66.0) (2026-10-02)


### Features

* **models:** add GPT-6.1 Sol and Claude Sonnet 5.5 ([#94](https://github.com/caelis-labs/caelis/issues/94)) ([51c0b19](https://github.com/caelis-labs/caelis/commit/51c0b19a9b139cffcf37a73443d91d2c290da804))


### Performance Improvements

* **application:** query indexed pending receipts in WaitCalls ([#93](https://github.com/caelis-labs/caelis/issues/93)) ([491138f](https://github.com/caelis-labs/caelis/commit/491138f8e1a10c29e0cbf656aeb7004b4b5492b4)), closes [#92](https://github.com/caelis-labs/caelis/issues/92)

## [0.65.0](https://github.com/caelis-labs/caelis/compare/v0.64.0...v0.65.0) (2026-09-28)


### ⚠ BREAKING CHANGES

* **runtime:** Unix command execution defaults to a non-login Bash shell and application commands inherit the configured user environment. Seatbelt SDK embeddings must dispatch the self-exec helper or provide a compatible HelperPath.

### Features

* **application:** expose composable Guardian auto-review ([#89](https://github.com/caelis-labs/caelis/issues/89)) ([c11e74b](https://github.com/caelis-labs/caelis/commit/c11e74b1ea44e8ff16ae3ffc440bca2bcabd762e))
* **runtime:** expose composable execution configuration ([#85](https://github.com/caelis-labs/caelis/issues/85)) ([bdebd8d](https://github.com/caelis-labs/caelis/commit/bdebd8d2d4bfe6b1bec455fca0f19da49b673c8f))


### Bug Fixes

* **runtime:** preserve invocation identity through approval review ([#91](https://github.com/caelis-labs/caelis/issues/91)) ([e8281aa](https://github.com/caelis-labs/caelis/commit/e8281aa7bb89b0ae330044eb5bd6a13916fd68fc))

## [0.64.0](https://github.com/caelis-labs/caelis/compare/v0.63.0...v0.64.0) (2026-09-28)


### Features

* **application:** support multimodal callback results ([#82](https://github.com/caelis-labs/caelis/issues/82)) ([369cd58](https://github.com/caelis-labs/caelis/commit/369cd58b6d26cfd43057e0393fcab7d7c2c84cd8))

## [0.63.0](https://github.com/caelis-labs/caelis/compare/v0.62.0...v0.63.0) (2026-09-27)


### Features

* **control:** expose scoped application model capabilities ([#80](https://github.com/caelis-labs/caelis/issues/80)) ([f3298a8](https://github.com/caelis-labs/caelis/commit/f3298a842315781702f6c8948a6c12d019711b40))


### Bug Fixes

* report yielded command exits without consuming task results ([#78](https://github.com/caelis-labs/caelis/issues/78)) ([dd1cb96](https://github.com/caelis-labs/caelis/commit/dd1cb96d58b6355a9b3eb8b6eddba530e911ae4b))

## [0.62.0](https://github.com/caelis-labs/caelis/compare/v0.61.0...v0.62.0) (2026-09-24)


### Features

* expose interactive Host model settings to clients ([#77](https://github.com/caelis-labs/caelis/issues/77)) ([395fe10](https://github.com/caelis-labs/caelis/commit/395fe102c53e0740b59f8138d81aac684c3d040f))
* share native Worker sessions across Bot and TUI ([#75](https://github.com/caelis-labs/caelis/issues/75)) ([276159e](https://github.com/caelis-labs/caelis/commit/276159e01ccfb588a65153db9195f570d10d0273))

## [0.61.0](https://github.com/caelis-labs/caelis/compare/v0.60.1...v0.61.0) (2026-09-23)


### ⚠ BREAKING CHANGES

* **application:** Remove legacy Bot Mode and its CLI/TUI, dedicated APIs, tools and scheduler without compatibility entry points. Applications own identity, Notebook, business tools and scheduling. Application Store schema 1 to 2 preserves bindings, Sessions, operation digests and receipts, callbacks and resource bytes; old native Sessions retain RunCommand/Task plus the resource bridge. Schema-1 binaries cannot open schema 2: stop the Host and keep a complete protected Store backup before upgrade when rollback is required. This migration is independent of embedded Memory. Installing a new binary does not activate an already-running Host. CWD/inherit/execution/permissions remain creation-bound; model/effort/tier/instructions/tools support hot configuration. Real-model and Fast positive-path acceptance remain pending. See docs/application-runtime.md and docs/release.md.

### Features

* **application:** replace Bot mode with generic application runtime ([#72](https://github.com/caelis-labs/caelis/issues/72)) ([4a3c059](https://github.com/caelis-labs/caelis/commit/4a3c05964d6d240ec55414e189205930588ef099))
* **bot:** add owned work and scoped desktop capabilities ([#67](https://github.com/caelis-labs/caelis/issues/67)) ([f3019f2](https://github.com/caelis-labs/caelis/commit/f3019f24bb412d39a9e0146d270837adf26d591b))
* **models:** add Claude Opus 5.5 and retire Opus 5 recommendations ([#71](https://github.com/caelis-labs/caelis/issues/71)) ([5aebbc6](https://github.com/caelis-labs/caelis/commit/5aebbc6a750b8b2d472a0987fbf2e0942062c6d9))
* **models:** support GPT-6 Sol and Luna ([#69](https://github.com/caelis-labs/caelis/issues/69)) ([e8f568e](https://github.com/caelis-labs/caelis/commit/e8f568e568114dfa56252672baad5551d3d9034b))


### Bug Fixes

* **application:** preserve native receipts and recover archive completion ([#73](https://github.com/caelis-labs/caelis/issues/73)) ([12d4924](https://github.com/caelis-labs/caelis/commit/12d49240feee60f841b9e69f27a77ce7ffb1a82f))
* **resume:** match equivalent workspace paths when listing sessions ([#70](https://github.com/caelis-labs/caelis/issues/70)) ([b4e2e72](https://github.com/caelis-labs/caelis/commit/b4e2e72fc4a2c1dced71cfc905852981aac6416e))

## [0.60.1](https://github.com/caelis-labs/caelis/compare/v0.60.0...v0.60.1) (2026-09-22)


### Bug Fixes

* **models:** refresh MiMo, Grok, and DeepSeek catalogs ([#65](https://github.com/caelis-labs/caelis/issues/65)) ([af85843](https://github.com/caelis-labs/caelis/commit/af8584342b65c383a9dee70aa82edeee474afcee))

## [0.60.0](https://github.com/caelis-labs/caelis/compare/v0.59.0...v0.60.0) (2026-09-21)


### Features

* add Antigravity ACP and background participant workflows ([#62](https://github.com/caelis-labs/caelis/issues/62)) ([f7c2b4b](https://github.com/caelis-labs/caelis/commit/f7c2b4bdd56f4bcfd78f9956ed246cb5e587e16d))

## [0.59.0](https://github.com/caelis-labs/caelis/compare/v0.58.0...v0.59.0) (2026-09-20)


### Features

* add Bot notebooks and streamline Jev screening ([#59](https://github.com/caelis-labs/caelis/issues/59)) ([84392cd](https://github.com/caelis-labs/caelis/commit/84392cd5e94e00fba45d6f301ac5fa4b677a948a))
* integrate Jev judgments with ToolSearch, Guardian and Memory ([#56](https://github.com/caelis-labs/caelis/issues/56)) ([f1ce9b4](https://github.com/caelis-labs/caelis/commit/f1ce9b44e728127a81b3a483e4295e885d1917d7))


### Bug Fixes

* gate Guardian screening on missing facts and visible violations ([#61](https://github.com/caelis-labs/caelis/issues/61)) ([eecc1e0](https://github.com/caelis-labs/caelis/commit/eecc1e0d90635ce3e284bbdfc92b846ce9f1d6ee))
* scope collaboration message display to each agent pane ([#60](https://github.com/caelis-labs/caelis/issues/60)) ([7034ad0](https://github.com/caelis-labs/caelis/commit/7034ad0f9df8fd7c0b00afeb0be1c2e5a6e4def5))
* **tui:** speed up resume and align agent message interactions ([#58](https://github.com/caelis-labs/caelis/issues/58)) ([0b1e0f2](https://github.com/caelis-labs/caelis/commit/0b1e0f2413378fcbcfb5a8bb45e110010ad5da56))

## [0.58.0](https://github.com/caelis-labs/caelis/compare/v0.57.0...v0.58.0) (2026-09-19)


### Features

* enable ACP controller collaboration and per-agent Fast mode ([#54](https://github.com/caelis-labs/caelis/issues/54)) ([5812568](https://github.com/caelis-labs/caelis/commit/58125688a5409a7bde199298d7d4c18d76e9574c))
* **tui:** unify slash interactions across work and bot modes ([#51](https://github.com/caelis-labs/caelis/issues/51)) ([cd13382](https://github.com/caelis-labs/caelis/commit/cd133824d8bef500f6c92751c522e2abddc969c9))


### Bug Fixes

* **acp:** preserve tool names with SDK v1.4.0 ([#53](https://github.com/caelis-labs/caelis/issues/53)) ([7ebba29](https://github.com/caelis-labs/caelis/commit/7ebba290d88649aeb16de2bd213b9446f3786925))
* address release review lifecycle and transcript regressions ([#55](https://github.com/caelis-labs/caelis/issues/55)) ([e386c27](https://github.com/caelis-labs/caelis/commit/e386c27050e55d43f70f6bd2df11fee94602d09a))

## [0.57.0](https://github.com/caelis-labs/caelis/compare/v0.56.1...v0.57.0) (2026-09-17)


### Features

* **bot:** add persistent tool-free Bot mode ([#44](https://github.com/caelis-labs/caelis/issues/44)) ([2a8e218](https://github.com/caelis-labs/caelis/commit/2a8e218b3bda89eaf35f3fe7227def8ab8e608b4))
* **tui:** split Agent message row clicks and bracket approval outcomes ([#47](https://github.com/caelis-labs/caelis/issues/47)) ([94eedbd](https://github.com/caelis-labs/caelis/commit/94eedbd2d0db0c6fb8a5a3315fecda7fdb23f404))


### Bug Fixes

* bound stream retention and restore approval replay ([#45](https://github.com/caelis-labs/caelis/issues/45)) ([f71bc03](https://github.com/caelis-labs/caelis/commit/f71bc0302902c740039a85e03d1315d4b21ac5a8))

## [0.56.1](https://github.com/caelis-labs/caelis/compare/v0.56.0...v0.56.1) (2026-09-15)


### Bug Fixes

* **ci:** stabilize ACP retry tests and remove redundant gates ([#43](https://github.com/caelis-labs/caelis/issues/43)) ([e6a176f](https://github.com/caelis-labs/caelis/commit/e6a176fe2aaea1c0a6244a10a7c995b505a2d1a1))
* prepare provider reviewers before ACP startup ([#40](https://github.com/caelis-labs/caelis/issues/40)) ([7b012ab](https://github.com/caelis-labs/caelis/commit/7b012ab36832f9d5464fab7ed7504936611aa360))
