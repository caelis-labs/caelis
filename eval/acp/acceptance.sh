#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_dir="$(cd "$script_dir/../.." && pwd)"
for command_name in acpx go jq shasum; do
  command -v "$command_name" >/dev/null 2>&1 || {
    printf 'required command not found: %s\n' "$command_name" >&2
    exit 1
  }
done

source_store="${CAELIS_EVAL_SOURCE_STORE:-$HOME/.caelis}"
source_config="$source_store/config.json"
profile_id="${CAELIS_EVAL_MODEL_PROFILE:-provider:xiaomi@token-plan-cn/xiaomi/mimo-v2.5-pro}"
endpoint_id="${CAELIS_EVAL_PROVIDER_ENDPOINT:-xiaomi@token-plan-cn}"
state_root="${CAELIS_ACP_EVAL_ROOT:-${XDG_STATE_HOME:-$HOME/.local/state}/caelis/evals/acp}"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
commit="$(git -C "$repo_dir" rev-parse --short=12 HEAD)"
run_id="${CAELIS_EVAL_RUN_ID:-${timestamp}-${commit}}"
run_dir="$state_root/$run_id"
[[ -f "$source_config" ]] || {
  printf 'Caelis source config not found: %s\n' "$source_config" >&2
  exit 1
}
[[ ! -e "$run_dir" ]] || {
  printf 'ACP evaluation run already exists: %s\n' "$run_dir" >&2
  exit 1
}
mkdir -p "$run_dir/bin" "$run_dir/logs" "$run_dir/workspace" "$run_dir/acpx-home"

runtime_store="$(mktemp -d "${TMPDIR:-/tmp}/caelis-acp-eval.XXXXXX")"
cleanup() {
  [[ -n "$runtime_store" && -d "$runtime_store" ]] && rm -rf -- "$runtime_store"
}
trap cleanup EXIT
mkdir -p "$runtime_store/providers/credentials"
chmod 700 "$runtime_store" "$runtime_store/providers" "$runtime_store/providers/credentials"

credential_ref="$(jq -er --arg endpoint "$endpoint_id" '.models.provider_endpoints[] | select(.id == $endpoint) | .credential_ref' "$source_config")"
credential_hash="$(printf '%s' "$credential_ref" | shasum -a 256 | awk '{print $1}')"
credential_path="$source_store/providers/credentials/$credential_hash.json"
[[ -f "$credential_path" ]] || {
  printf 'credential file for endpoint %s is unavailable\n' "$endpoint_id" >&2
  exit 1
}
cp "$credential_path" "$runtime_store/providers/credentials/$credential_hash.json"
chmod 600 "$runtime_store/providers/credentials/$credential_hash.json"

jq -e \
  --arg endpoint "$endpoint_id" \
  --arg profile "$profile_id" \
  '{
    schema_version,
    models: {
      provider_endpoints: [.models.provider_endpoints[] | select(.id == $endpoint)],
      configs: [.models.configs[] | select(.provider_endpoint_id == $endpoint)]
    },
    model_profiles: {
      default_profile_id: $profile,
      default_effort: "high",
      profiles: [.model_profiles.profiles[] | select(.id == $profile)]
    },
    agent_bindings: {},
    plugins: []
  }
  | select((.models.provider_endpoints | length) == 1)
  | select((.model_profiles.profiles | length) == 1)' \
  "$source_config" >"$runtime_store/config.json"
chmod 600 "$runtime_store/config.json"

binary="$run_dir/bin/caelis"
(
  cd "$repo_dir"
  go build \
    -ldflags "-X github.com/caelis-labs/caelis/internal/version.Version=eval -X github.com/caelis-labs/caelis/internal/version.Commit=$commit -X github.com/caelis-labs/caelis/internal/version.Date=$timestamp" \
    -o "$binary" ./cmd/caelis
)

jq -n \
  --arg schema_version "caelis.eval.acp/v1" \
  --arg run_id "$run_id" \
  --arg commit "$commit" \
  --arg started_at "$timestamp" \
  --arg acpx_version "$(acpx --version)" \
  --arg profile_id "$profile_id" \
  --arg endpoint_id "$endpoint_id" \
  '{schema_version:$schema_version, run_id:$run_id, commit:$commit,
    started_at:$started_at, acpx_version:$acpx_version,
    model_profile_id:$profile_id, provider_endpoint_id:$endpoint_id,
    status:"running"}' >"$run_dir/manifest.json"

run_gate() {
  local name="$1"
  shift
  printf 'running ACP gate: %s\n' "$name"
  set +e
  "$@" >"$run_dir/logs/$name.log" 2>&1
  local gate_status=$?
  set -e
  printf '%s\t%s\n' "$name" "$gate_status" >>"$run_dir/gates.tsv"
  return "$gate_status"
}

gate_status=0
cd "$repo_dir"
run_gate protocol-conformance \
  go test -count=1 ./internal/acpagentbridge \
  -run '^(TestBuiltInProjectionAndExternalWireShareSDKSemantics|TestRuntimeAgentConformance.*|TestRuntimeAgentACPSessionLoadSpawnGolden|TestRuntimeAgentACPSpawnLifecycleGolden)$' || gate_status=1
run_gate product-typed-client \
  go test -count=1 ./app/gatewayapp/acpagent -run '^TestNewFromStack' || gate_status=1
run_gate tui-side-acp-overlay \
  go test -count=1 ./surfaces/tui/app \
  -run '^(TestDurableTaskWaitFinalCompletesOriginalSpawnPanel|TestCanonicalTaskSequenceRendersIdenticallyLiveAndReplay|TestSideACPProjected.*)$' || gate_status=1

printf 'ACP_ACCEPTANCE_WORKSPACE\n' >"$run_dir/workspace/README.txt"
printf -v quoted_binary '%q' "$binary"
printf -v quoted_store '%q' "$runtime_store"
printf -v quoted_workspace '%q' "$run_dir/workspace"
printf -v quoted_profile '%q' "$profile_id"
agent_command="$quoted_binary acp -store-dir $quoted_store -workspace-key acp-acceptance -workspace-cwd $quoted_workspace -model-profile $quoted_profile -reasoning-effort high -approval-mode manual -policy-profile workspace-write -sandbox-backend host"

acpx_base=(
  acpx
  --agent "$agent_command"
  --cwd "$run_dir/workspace"
  --approve-all
  --non-interactive-permissions fail
  --format json
  --json-strict
  --timeout 300
  --ttl 1
)

acpx_status=0
HOME="$run_dir/acpx-home" "${acpx_base[@]}" sessions new --name typed \
  >"$run_dir/logs/session-new.jsonl" 2>"$run_dir/logs/session-new.stderr" || acpx_status=1
HOME="$run_dir/acpx-home" "${acpx_base[@]}" prompt -s typed \
  'Reply exactly ACP_FIRST_OK.' \
  >"$run_dir/logs/prompt-first.jsonl" 2>"$run_dir/logs/prompt-first.stderr" || acpx_status=1
sleep 2
HOME="$run_dir/acpx-home" "${acpx_base[@]}" prompt -s typed \
  'This process must load the existing ACP Session. Reply exactly ACP_RESUME_OK.' \
  >"$run_dir/logs/prompt-resume.jsonl" 2>"$run_dir/logs/prompt-resume.stderr" || acpx_status=1
HOME="$run_dir/acpx-home" "${acpx_base[@]}" prompt -s typed \
  "Use RunCommand, not Write or Patch, to run: printf '%s' ACP_TASK_FILE_OK > $run_dir/workspace/acp-task.txt && cat $run_dir/workspace/acp-task.txt. If Host permission is required, retry the same command with require_escalated and a concrete justification. Then reply exactly ACP_TASK_OK." \
  >"$run_dir/logs/prompt-task.jsonl" 2>"$run_dir/logs/prompt-task.stderr" || acpx_status=1
HOME="$run_dir/acpx-home" "${acpx_base[@]}" sessions list --filter-cwd "$run_dir/workspace" \
  >"$run_dir/logs/session-list.jsonl" 2>"$run_dir/logs/session-list.stderr" || acpx_status=1
HOME="$run_dir/acpx-home" "${acpx_base[@]}" sessions close typed \
  >"$run_dir/logs/session-close.jsonl" 2>"$run_dir/logs/session-close.stderr" || acpx_status=1

structured_status=0
for output in "$run_dir"/logs/*.jsonl; do
  jq -e -s 'all(.[]; type == "object")' "$output" >/dev/null || structured_status=1
done
agent_message_text() {
  jq -rs '[.[]
    | select(.method == "session/update")
    | select(.params.update.sessionUpdate == "agent_message_chunk")
    | .params.update.content.text] | join("")' "$1"
}
[[ "$(agent_message_text "$run_dir/logs/prompt-first.jsonl")" == "ACP_FIRST_OK" ]] || structured_status=1
[[ "$(agent_message_text "$run_dir/logs/prompt-resume.jsonl")" == "ACP_RESUME_OK" ]] || structured_status=1
[[ "$(agent_message_text "$run_dir/logs/prompt-task.jsonl")" == "ACP_TASK_OK" ]] || structured_status=1
grep -Fq 'ACP_TASK_FILE_OK' "$run_dir/workspace/acp-task.txt" || structured_status=1
jq -e -s '
  any(.[];
    .method == "session/update"
    and .params.update.sessionUpdate == "tool_call_update"
    and .params.update.kind == "execute"
    and .params.update.status == "completed")
' "$run_dir/logs/prompt-task.jsonl" >/dev/null || structured_status=1
jq -e -s 'any(.[]; .method == "session/request_permission")' \
  "$run_dir/logs/prompt-task.jsonl" >/dev/null || structured_status=1

passed=false
if [[ "$gate_status" -eq 0 && "$acpx_status" -eq 0 && "$structured_status" -eq 0 ]]; then
  passed=true
fi
finished_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
jq -n \
  --arg schema_version "caelis.eval.acp.report/v1" \
  --arg run_id "$run_id" \
  --arg commit "$commit" \
  --arg finished_at "$finished_at" \
  --argjson deterministic_gates_exit "$gate_status" \
  --argjson acpx_exit "$acpx_status" \
  --argjson structured_output_exit "$structured_status" \
  --argjson passed "$passed" \
  '{schema_version:$schema_version, run_id:$run_id, commit:$commit,
    finished_at:$finished_at, deterministic_gates_exit:$deterministic_gates_exit,
    acpx_exit:$acpx_exit, structured_output_exit:$structured_output_exit,
    passed:$passed}' >"$run_dir/report.json"

{
  printf '# Caelis ACP acceptance report\n\n'
  printf -- '- Run: `%s`\n' "$run_id"
  printf -- '- Commit: `%s`\n' "$commit"
  printf -- '- Deterministic protocol/product/TUI gates: `%s`\n' "$gate_status"
  printf -- '- Real acpx lifecycle/prompt/resume/task/close: `%s`\n' "$acpx_status"
  printf -- '- Structured JSON and marker validation: `%s`\n' "$structured_status"
  printf -- '- Passed: `%s`\n' "$passed"
} >"$run_dir/report.md"

jq \
  --arg finished_at "$finished_at" \
  --argjson passed "$passed" \
  '.finished_at = $finished_at | .status = (if $passed then "completed" else "failed" end)' \
  "$run_dir/manifest.json" >"$run_dir/manifest.next.json"
mv "$run_dir/manifest.next.json" "$run_dir/manifest.json"

printf 'ACP acceptance artifacts: %s\n' "$run_dir"
cat "$run_dir/report.md"
[[ "$passed" == true ]]
