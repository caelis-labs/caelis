#!/usr/bin/env bash
set -euo pipefail

run_dir="$1"
credential_path="$2"
credential_name="$3"
ca_bundle_path="$4"
tmux_name="$5"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_dir="$(cd "$script_dir/../.." && pwd)"
binary_path="$run_dir/bundle/caelis-linux-amd64"
config_path="$run_dir/bundle/config.json"
tasks_path="$run_dir/tasks.txt"
job_name="$(jq -r '.run_id' "$run_dir/manifest.json")"
profile_id="$(jq -r '.model_profile_id' "$run_dir/manifest.json")"
reasoning_effort="$(jq -r '.reasoning_effort' "$run_dir/manifest.json")"
dataset_ref="$(jq -r '.dataset + "@" + .dataset_digest' "$run_dir/manifest.json")"
concurrency="$(jq -r '.execution.concurrency' "$run_dir/manifest.json")"
timeout_multiplier="$(jq -r '.execution.timeout_multiplier' "$run_dir/manifest.json")"
wire_validator="$run_dir/bundle/traceaudit"

exec > >(tee -a "$run_dir/harbor.log") 2>&1
cd "$repo_dir"
export PYTHONPATH="$repo_dir${PYTHONPATH:+:$PYTHONPATH}"
jq '.status = "running" | .tmux_session = $tmux' --arg tmux "$tmux_name" \
  "$run_dir/manifest.json" >"$run_dir/manifest.next.json"
mv "$run_dir/manifest.next.json" "$run_dir/manifest.json"

harbor_args=(
  run --yes
  --job-name "$job_name"
  --jobs-dir "$run_dir/jobs"
  --dataset "$dataset_ref"
  --agent eval.terminalbench.caelis_agent:CaelisAgent
  --model "$profile_id"
  --n-concurrent "$concurrency"
  --n-concurrent-agents "$concurrency"
  --timeout-multiplier "$timeout_multiplier"
  --max-retries 0
  --agent-kwarg "binary_path=$binary_path"
  --agent-kwarg "config_path=$config_path"
  --agent-kwarg "credential_path=$credential_path"
  --agent-kwarg "credential_name=$credential_name"
  --agent-kwarg "ca_bundle_path=$ca_bundle_path"
  --agent-kwarg "reasoning_effort=$reasoning_effort"
)
cache_mode="$(jq -r '.verifier_cache.mode // "disabled"' "$run_dir/manifest.json")"
if [[ "$cache_mode" == "apt-cacher-ng+uv-mirror" ]]; then
  cache_proxy_url="$(jq -er '.verifier_cache.apt.proxy_url' "$run_dir/manifest.json")"
  uv_download_url="$(jq -er '.verifier_cache.uv.download_url' "$run_dir/manifest.json")"
  cache_overlay_path="$run_dir/bundle/verifier-cache.compose.yaml"
  [[ -f "$cache_overlay_path" ]] || {
    printf 'Terminal-Bench verifier cache overlay is unavailable: %s\n' "$cache_overlay_path" >&2
    exit 1
  }
  harbor_args+=(
    --extra-docker-compose "$cache_overlay_path"
    --verifier-env "http_proxy=$cache_proxy_url"
    --verifier-env "no_proxy=localhost,127.0.0.1,apt-cache,uv-cache"
    --verifier-env "UV_DOWNLOAD_URL=$uv_download_url"
  )
elif [[ "$cache_mode" == "apt-cacher-ng" ]]; then
  cache_proxy_url="$(jq -er '.verifier_cache.proxy_url' "$run_dir/manifest.json")"
  cache_overlay_path="$run_dir/bundle/verifier-cache.compose.yaml"
  harbor_args+=(
    --extra-docker-compose "$cache_overlay_path"
    --verifier-env "http_proxy=$cache_proxy_url"
    --verifier-env "no_proxy=localhost,127.0.0.1,apt-cache"
  )
elif [[ "$cache_mode" != "disabled" ]]; then
  printf 'unsupported Terminal-Bench verifier cache mode: %s\n' "$cache_mode" >&2
  exit 1
fi
while IFS= read -r task_name; do
  [[ -n "$task_name" ]] && harbor_args+=(--include-task-name "$task_name")
done <"$tasks_path"

set +e
harbor "${harbor_args[@]}"
harbor_status=$?
set -e

harbor_finished_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
jq \
  --arg harbor_finished_at "$harbor_finished_at" \
  --argjson harbor_exit_code "$harbor_status" \
  '.harbor_finished_at = $harbor_finished_at
   | .harbor_exit_code = $harbor_exit_code' \
  "$run_dir/manifest.json" >"$run_dir/manifest.next.json"
mv "$run_dir/manifest.next.json" "$run_dir/manifest.json"

report_status=0
python3 -m eval.terminalbench.report \
  --job-dir "$run_dir/jobs/$job_name" \
  --manifest "$run_dir/manifest.json" \
  --tasks "$tasks_path" \
  --wire-validator "$wire_validator" \
  --output-dir "$run_dir" || report_status=$?

finished_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
jq \
  --arg finished_at "$finished_at" \
  --argjson harbor_exit_code "$harbor_status" \
  --argjson report_exit_code "$report_status" \
  '.finished_at = $finished_at
   | .harbor_exit_code = $harbor_exit_code
   | .report_exit_code = $report_exit_code
   | .status = (if $harbor_exit_code == 0 and $report_exit_code == 0 then "completed" else "failed" end)' \
  "$run_dir/manifest.json" >"$run_dir/manifest.next.json"
mv "$run_dir/manifest.next.json" "$run_dir/manifest.json"

python3 -m eval.terminalbench.audit --run-dir "$run_dir"
evidence_complete="$(jq -r '.evidence_complete' "$run_dir/audit.json")"
audit_status=0
if [[ "$evidence_complete" != "true" ]]; then
  audit_status=1
fi
jq \
  --argjson audit_exit_code "$audit_status" \
  --argjson evidence_complete "$evidence_complete" \
  '.audit_exit_code = $audit_exit_code
   | .evidence_complete = $evidence_complete
   | .status = (if .harbor_exit_code == 0 and .report_exit_code == 0 and $audit_exit_code == 0 then "completed" else "failed" end)' \
  "$run_dir/manifest.json" >"$run_dir/manifest.next.json"
mv "$run_dir/manifest.next.json" "$run_dir/manifest.json"

printf 'Terminal-Bench worker finished: harbor=%s report=%s audit=%s\n' \
  "$harbor_status" "$report_status" "$audit_status"
# Refresh the index after the final manifest and log line so their hashes are
# the immutable terminal state of this run.
python3 -m eval.terminalbench.audit --run-dir "$run_dir"
if [[ "$harbor_status" -ne 0 ]]; then
  exit "$harbor_status"
fi
if [[ "$audit_status" -ne 0 ]]; then
  exit "$audit_status"
fi
exit "$report_status"
