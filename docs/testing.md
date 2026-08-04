# Product Testing

Caelis product regression uses deterministic scenarios to verify both semantic
correctness and the user-visible TUI result. It complements package tests; it
does not introduce another Runtime, Control flow, projection path, or transcript
store.

## Test shape

One product scenario may record three artifacts:

1. Runtime-owned `session.Event` facts;
2. the ACP `eventstream.Envelope` values produced by the maintained projector;
3. named, normalized full terminal frames captured from a physical VT emulator.

`internal/evalharness.ProductScenario` owns only scenario execution and artifact
checks. Layer-specific setup remains beside the product entry point under test.
This keeps SDK tests independent from repository `internal/*` packages and keeps
Surface tests dependent on canonical inward contracts.

Scenarios use deterministic models, stores, clocks, and inputs. Live-provider
evaluation remains a separate opt-in signal because provider availability and
model output are not release-regression oracles.

## Gates

Run the selected deterministic product suite with:

```bash
make product-acceptance
```

Each package selector is executed through `scripts/go_test_nonempty.sh`, so a
test rename or removal cannot silently turn a covered lane into a zero-test
pass. `make regression` includes this target, and `quality.yml` runs it as an
explicit required job. The ordinary `make test` suite continues to run the same
untagged tests as part of full package coverage.

Windows persistence behavior runs on the real `windows-latest` quality job.
Cross-compilation is not accepted as evidence for file-lock, rename, or WAL
recovery behavior.

## Initial coverage

| Product behavior | Semantic observation | User observation |
| --- | --- | --- |
| Context compaction and typed notices | Runtime event order, write serialization, ACP kind and delivery | Physical TUI shows a dedicated compacting phase, one success/failure notice, and no raw lifecycle value |
| Command startup recovery | Durable repair convergence and file-store round trip | Owner state remains terminal and recoverable; broader command rendering remains in the TUI regression suite |
| ACP controller fencing | Concurrent admission and cancellation of in-flight controller/participant work | No second lifecycle authority reaches a Surface |
| Active child action summaries | Bounded concurrent summary updates and terminal override | The existing Spawn owner remains the only physical task panel |
| Control feed and wire compatibility | Reconnect under revision churn and full Envelope OpenAPI conformance | Surface clients receive typed, replayable Envelopes |
| Windows file persistence | Real Windows locking, replacement, WAL recovery, and redacted diagnostics | Startup failures retain actionable diagnostics without leaking paths or Session identity |

## Adding a scenario

1. Start from a user-observable behavior and identify its canonical semantic
   owner.
2. Drive production entry points with deterministic dependencies. Do not call a
   Surface-only shortcut when the behavior originates in Runtime or Control.
3. Compare complete event and Envelope fact sequences. Use a named full-frame
   check for user-visible behavior; do not assert only one internal widget.
4. Add the test to the narrowest `PRODUCT_*_SELECTOR` in the Makefile. The test
   name should begin with `TestProductScenario` when it is a new cross-layer
   journey.
5. Run the focused target, relevant race coverage, `make regression`, and the
   repository quality gates required by the changed boundary.

The current physical TUI scenario starts at canonical Runtime events and sends
their maintained ACP projections through a real Bubble Tea program and VT
emulator. A small installed-binary PTY smoke suite for keyboard submission,
approval input, cancellation, and process shutdown is the next coverage layer;
it should reuse the same semantic oracles rather than parse screen text as
Runtime truth.
