# Fair comparison A: no GUI

`manifest.json` is the frozen input for `TestCodeModeFairLive`. It fixes four
independent Sessions in native/JS, then JS/native order. Each Session runs two
consecutive Turns, with 17/25 then 18/25 inputs. Only entry guidance differs;
both routes have the same shell ban, short Context7 question, output assertions
and no saved-script obligation. The second Turn never depends on GUI readiness.
Historical file capability B remains in the older harness and evidence.

The manifest pins the authorized model parameters, existing Context7 package
entry/version/hash and harness source hash. No credentials are included.
`TestCodeModeFairPrepare` produces such a manifest without starting MCP or making
a model call. The runner requires its explicit hash, validates it before sending
and reserves the output directory once. Never restart a directory with
RUN-STARTED.json to replace a failed or incomplete sample.

For this study, run from the repository root with the existing authorized local
profile and module entry (no installer):

```sh
export CAELIS_CODE_MODE_LIVE_CONFIG=/Users/xueyongzhi/.caelis/config.json
export CAELIS_CODE_MODE_FAIR_RUN="$PWD/.tmp/code-mode-fair-a-20261107"
export CAELIS_CODE_MODE_FAIR_MANIFEST_SHA256="$(cat app/gatewayapp/testdata/code-mode/fair-a/manifest.sha256)"
go test ./app/gatewayapp -run '^TestCodeModeFairLive$' -v -count=1 -timeout=0
```

Use the original prepared artifact directory, not an old live Session. The
streaming model gets an independent 600-second context per main or selector
generation, no Client.Timeout/header deadline, and no redispatch. A model/runtime
error or unknown outcome stops the study; failed business assertions are kept
and the fixed schedule continues. No model fallback or selective rerun exists.

Each request preserves SDK and actual HTTP instructions/tools values separately
from the full HTTP body, raw stream, first event, Response and original provider
receipt. Comparison must split first/continuation, pairs, and main/selector usage;
missing usage or cached counts remain unknown. Byte equality is not evidence of
provider KV caching. Actual Context7 query arguments, library/source and returned
results must be retained when interpreting variable documentation responses.

Offline accounting (never invokes a model or tool):

```sh
python3 app/gatewayapp/testdata/code-mode/fair-a/analyze.py "$CAELIS_CODE_MODE_FAIR_RUN"
```

The analyzer crosschecks final normalized usage against terminal raw SSE usage.
It preserves an explicitly reported cached_tokens=0 even when Go JSON omits
that zero, and leaves absent/incomplete usage unknown. Canonical tool-output
bytes count each tool-result Message once, not every repeated history copy.
