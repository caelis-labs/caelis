#!/usr/bin/env bash
set -euo pipefail

run_dir="$1"
credential_path="$2"
credential_name="$3"
tmux_name="$4"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_dir="$(cd "$script_dir/../.." && pwd)"
binary_path="$run_dir/bundle/caelis-linux-amd64"
config_path="$run_dir/bundle/config.json"
tasks_path="$run_dir/tasks.txt"
job_name="$(jq -r '.run_id' "$run_dir/manifest.json")"
profile_id="$(jq -r '.model_profile_id' "$run_dir/manifest.json")"
dataset_ref="$(jq -r '.dataset + "@" + .dataset_digest' "$run_dir/manifest.json")"
concurrency="${CAELIS_EVAL_CONCURRENCY:-2}"

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
  --max-retries 1
  --retry-include ApiRateLimitError
  --agent-kwarg "binary_path=$binary_path"
  --agent-kwarg "config_path=$config_path"
  --agent-kwarg "credential_path=$credential_path"
  --agent-kwarg "credential_name=$credential_name"
  --agent-kwarg reasoning_effort=high
)
while IFS= read -r task_name; do
  [[ -n "$task_name" ]] && harbor_args+=(--include-task-name "$task_name")
done <"$tasks_path"

set +e
harbor "${harbor_args[@]}"
harbor_status=$?
set -e

report_status=0
python3 "$script_dir/report.py" \
  --job-dir "$run_dir/jobs/$job_name" \
  --manifest "$run_dir/manifest.json" \
  --tasks "$tasks_path" \
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

printf 'Terminal-Bench worker finished: harbor=%s report=%s\n' "$harbor_status" "$report_status"
if [[ "$harbor_status" -ne 0 ]]; then
  exit "$harbor_status"
fi
exit "$report_status"
