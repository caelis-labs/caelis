This is the R8 finite acceptance harness. It has not run a real model. Inputs
are synthetic for the local MCP task; actual model/Runtime/Goja/MCP/workspace
execution is measured separately from the real external Context7 evidence.

Read RULES.md and both record/oracle snapshots. Turn 1 has 36 records; Turn 2
updates R015 to closed and appends R037 (37 total). The oracle selects the latest
record globally before filtering. It fixes cross-batch and within-batch ties,
source record/batch provenance and all output array ordering.

Preparation reads only the authorized model configuration and its endpoint and
single credential if present. It sends no request and starts no Context7 server.
The expected Context7 schemas are the retained prior source snapshot; a drift at
live startup stops before the first generation. Both routes receive the same
names and on-demand schema information, inputs, permissions and output duties.
Read/Write remain native functions in both routes; Patch is denied equally so
only Write can create the one report. JS filesystem orchestration is optional.

There are two different tasks, each with one native/JS pair and two consecutive
Turns per independent Session (four Sessions/eight Turns total). Small runs
native then JS; filter runs JS then native. This is NOT counterbalancing within
each task. Never pool the two tasks to claim stable or general benefit.

Known tool/script errors may be corrected by either model within its ORIGINAL
Run and 24-generation limit; every failed step/output token is counted. Provider
or Runtime fatal error, unknown outcome, incomplete generation or capture error
stops the study. Never replace a sample or replay unknown effects. Business or
cost target failures are retained; fixed scheduled Turns continue without retry.

Each generation streams and has its own 600-second budget. No shorter enclosing
HTTP/first-event/Go-test deadline is permitted; live uses `-timeout=0` and native
signal cancellation. No model fallback, credit adjustment, GUI, install or push.

Offline tests (safe; local synthetic MCP only):

```sh
go test ./app/gatewayapp -run '^TestCodeModeValue(FixturesOffline|PlanOffline)$' -count=1
```

After exact source commit, create a NEW private freeze directory (no network):

```sh
CAELIS_CODE_MODE_LIVE_CONFIG=/Users/xueyongzhi/.caelis/config.json \
CAELIS_CODE_MODE_VALUE_PREPARE=/private/tmp/caelis-code-mode-implementation-20261107/.tmp/code-mode-value-frozen-20261108 \
go test ./app/gatewayapp -run '^TestCodeModeValuePrepare$' -v -count=1
```

Only if the selected config has a credential reference, set the previously
coordinated `CAELIS_CODE_MODE_LIVE_CREDENTIAL_ROOT`; never enumerate credentials.
The manifest binds exact HEAD, all harness/helper hashes, data/oracle hashes,
model parameters, resource hashes, exact prompts, schemas by source snapshot,
order, finite counts, error rules and per-Turn/per-pair conditions. It records
actual provider usage only: missing components stay unavailable. The small task
targets non-increase of main/total requests, fresh and output; filter targets a
strict decrease in main/total requests and fresh, and no increase in output.
All generated JS/repair/selector tokens count. Native-zero relative deltas are
N/A; absolute deltas remain. Prefix bytes are not tokens or provider KV proof.

Do not run before the original independent review and Bot manifest readback.
The eventual explicit entry (not executed during preparation) is:

```sh
CAELIS_CODE_MODE_LIVE_CONFIG=/Users/xueyongzhi/.caelis/config.json \
CAELIS_CODE_MODE_VALUE_RUN=/private/tmp/caelis-code-mode-implementation-20261107/.tmp/code-mode-value-frozen-20261108 \
CAELIS_CODE_MODE_VALUE_MANIFEST_SHA256=EXACT_BOT_READ_BACK_HASH \
go test ./app/gatewayapp -run '^TestCodeModeValueLive$' -v -count=1 -timeout=0
```

An exclusive RUN-STARTED.json prevents replay. Every Session keeps actual SDK and
HTTP requests, first/complete streams, final Response, invocation/usage, original
Tool receipts, failures, input/output files and SDK tool-context byte snapshots.
The Context7 API relevance claim requires review of the original result;
`unavailable` is not full API capability success. Final analysis never calls a
model or tool:

```sh
python3 app/gatewayapp/testdata/code-mode/h1-value/analyze.py /ABSOLUTE/FROZEN_DIR
```
