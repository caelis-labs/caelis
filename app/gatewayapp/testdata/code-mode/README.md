# Code Mode live acceptance

`TestCodeModeLive` in [code_mode_live_test.go](../../code_mode_live_test.go)
uses a real streaming model, the ordinary SDK Runtime, native filesystem tools,
Task store and real MCP clients. It does not substitute generated scripts.

Run from the repository root with explicit task-owned absolute artifact paths:

```sh
export CAELIS_CODE_MODE_LIVE_CONFIG=/path/to/existing/config.json
export CAELIS_CODE_MODE_LIVE_CREDENTIAL_ROOT=/path/to/existing/caelis-data
export CAELIS_CODE_MODE_LIVE_CONTEXT7_ENTRY=/path/to/existing/context7-mcp/dist/index.js
export CAELIS_CODE_MODE_LIVE_MODEL=magpie@local/magpie/codex/gpt-6-luna
export CAELIS_CODE_MODE_LIVE_ARTIFACTS=/private/tmp/code-mode-acceptance/js
export CAELIS_CODE_MODE_LIVE_MODE=js_exec
CAELIS_CODE_MODE_LIVE_NO_GUI=1 go test ./app/gatewayapp -run '^TestCodeModeLive$' -v -count=1 -timeout=0
```

The first task reads synthetic 17/25 inputs, queries Context7, and generates and
executes a saved workflow producing 42. The independent `baseline` mode completes
the same data/documentation task with native calls. It has no JS file-execution
requirement; report this extra JS work when comparing actual usage.

Only the selected model, its endpoint and one API-key credential reference are
resolved. A temporary credential copy is private and deleted after validation.
No production config or Session is changed. Each model generation streams with
an independent 600-second context, no shorter HTTP/header deadline and no added
retry. The finite generation cap includes private schema-selector generations.

GUI is disabled by default. After desktop coordination allows it, build the
owned application with `build-fixture.sh /private/tmp/task-owned-fixture`, then
continue the original Session using a **new** artifact directory, the original
`CAELIS_CODE_MODE_LIVE_STATE_ROOT`, `CAELIS_CODE_MODE_LIVE_RESUME=1`,
`CAELIS_CODE_MODE_LIVE_ENABLE_GUI=1`, and an existing managed DTW root in
`CAELIS_CODE_MODE_LIVE_DTW_ROOT`. This second task edits the same workflow to 43
and permits only the fixture's exact `app.look()` / `app.capture()` body. Do not
resume a failed/unknown effect to obtain cleaner evidence. Close the owned app
and preserve original receipts when the finite task ends.

Artifacts retain prompts, exact SDK and HTTP requests, stable-prefix slices,
original invocation receipts, first events, final responses/usage, generated
files, Session/Task records and observed worker metrics. Model usage includes
main and selector calls. A stable prefix alone does not prove token savings.
