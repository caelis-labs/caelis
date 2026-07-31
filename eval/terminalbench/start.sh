#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_dir="$(cd "$script_dir/../.." && pwd)"
taskset_name="${1:-acceptance}"

case "$taskset_name" in
  smoke|acceptance|full) ;;
  *)
    printf 'usage: %s [smoke|acceptance|full]\n' "$0" >&2
    exit 2
    ;;
esac

for command_name in docker go harbor jq shasum tmux; do
  command -v "$command_name" >/dev/null 2>&1 || {
    printf 'required command not found: %s\n' "$command_name" >&2
    exit 1
  }
done

harbor_version="$(harbor --version)"
if [[ "$harbor_version" != "0.16.1" ]]; then
  printf 'Harbor 0.16.1 is required, got %s\n' "$harbor_version" >&2
  exit 1
fi
docker info >/dev/null
harbor_python="${HARBOR_PYTHON:-}"
if [[ -z "$harbor_python" ]]; then
  harbor_python="$(sed -n '1s/^#!//p' "$(command -v harbor)")"
fi
if [[ ! -x "$harbor_python" ]]; then
  printf 'cannot resolve Harbor Python; set HARBOR_PYTHON explicitly\n' >&2
  exit 1
fi

source_store="${CAELIS_EVAL_SOURCE_STORE:-$HOME/.caelis}"
source_config="$source_store/config.json"
profile_id="${CAELIS_EVAL_MODEL_PROFILE:-provider:xiaomi@token-plan-cn/xiaomi/mimo-v2.5-pro}"
endpoint_id="${CAELIS_EVAL_PROVIDER_ENDPOINT:-xiaomi@token-plan-cn}"
[[ -f "$source_config" ]] || {
  printf 'Caelis source config not found: %s\n' "$source_config" >&2
  exit 1
}

credential_ref="$(jq -er --arg endpoint "$endpoint_id" '.models.provider_endpoints[] | select(.id == $endpoint) | .credential_ref' "$source_config")"
credential_hash="$(printf '%s' "$credential_ref" | shasum -a 256 | awk '{print $1}')"
credential_name="$credential_hash.json"
credential_path="$source_store/providers/credentials/$credential_name"
[[ -f "$credential_path" ]] || {
  printf 'credential file for endpoint %s is unavailable\n' "$endpoint_id" >&2
  exit 1
}

state_root="${CAELIS_EVAL_ROOT:-${XDG_STATE_HOME:-$HOME/.local/state}/caelis/evals/terminalbench}"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
commit="$(git -C "$repo_dir" rev-parse --short=12 HEAD)"
run_id="${CAELIS_EVAL_RUN_ID:-${timestamp}-${commit}-${taskset_name}}"
run_dir="$state_root/$run_id"
[[ ! -e "$run_dir" ]] || {
  printf 'evaluation run already exists: %s\n' "$run_dir" >&2
  exit 1
}
mkdir -p "$run_dir/bundle" "$run_dir/jobs"

config_path="$run_dir/bundle/config.json"
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
  "$source_config" >"$config_path"
chmod 600 "$config_path"

binary_path="$run_dir/bundle/caelis-linux-amd64"
(
  cd "$repo_dir"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags "-X github.com/caelis-labs/caelis/internal/version.Version=eval -X github.com/caelis-labs/caelis/internal/version.Commit=$commit -X github.com/caelis-labs/caelis/internal/version.Date=$timestamp" \
    -o "$binary_path" ./cmd/caelis
)
chmod 700 "$binary_path"

dataset_tasks_path="$run_dir/dataset-tasks.txt"
"$harbor_python" \
  "$script_dir/write_dataset_tasks.py" >"$dataset_tasks_path"
tasks_path="$run_dir/tasks.txt"
if [[ "$taskset_name" == "full" ]]; then
  cp "$dataset_tasks_path" "$tasks_path"
else
  awk 'NF && $1 !~ /^#/' "$script_dir/tasksets/$taskset_name.txt" >"$tasks_path"
fi
task_count="$(wc -l <"$tasks_path" | tr -d ' ')"
[[ "$task_count" -gt 0 ]] || {
  printf 'task set %s is empty\n' "$taskset_name" >&2
  exit 1
}
while IFS= read -r task_name; do
  grep -Fqx "$task_name" "$dataset_tasks_path" || {
    printf 'task %s is not in pinned Terminal-Bench 2.1\n' "$task_name" >&2
    exit 1
  }
done <"$tasks_path"

dataset_digest="sha256:7d7bdc1cbedad549fc1140404bd4dc45e5fd0ea7c4186773687d177ad3a0699a"
jq -n \
  --arg schema_version "caelis.eval.terminalbench/v1" \
  --arg run_id "$run_id" \
  --arg commit "$commit" \
  --arg started_at "$timestamp" \
  --arg taskset "$taskset_name" \
  --arg dataset "terminal-bench/terminal-bench-2-1" \
  --arg dataset_digest "$dataset_digest" \
  --arg harbor_version "$harbor_version" \
  --arg profile_id "$profile_id" \
  --arg endpoint_id "$endpoint_id" \
  --argjson task_count "$task_count" \
  '{schema_version:$schema_version, run_id:$run_id, commit:$commit,
    started_at:$started_at, taskset:$taskset, dataset:$dataset,
    dataset_digest:$dataset_digest, harbor_version:$harbor_version,
    model_profile_id:$profile_id, provider_endpoint_id:$endpoint_id,
    task_count:$task_count, status:"prepared"}' >"$run_dir/manifest.json"

tmux_name="caelis-tbench-${run_id//[^[:alnum:]-]/-}"
printf -v worker_command '%q %q %q %q %q' \
  "$script_dir/worker.sh" "$run_dir" "$credential_path" "$credential_name" "$tmux_name"
tmux new-session -d -s "$tmux_name" "$worker_command"
tmux set-option -t "$tmux_name" remain-on-exit on

printf 'Terminal-Bench 2.1 run started\n'
printf '  tmux: %s\n' "$tmux_name"
printf '  tasks: %s (%s)\n' "$taskset_name" "$task_count"
printf '  artifacts: %s\n' "$run_dir"
printf 'attach with: tmux attach -t %s\n' "$tmux_name"
