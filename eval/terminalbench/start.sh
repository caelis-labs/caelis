#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_dir="$(cd "$script_dir/../.." && pwd)"
taskset_name="${1:-acceptance}"

case "$taskset_name" in
  smoke|prior-failures|acceptance|full) ;;
  *)
    printf 'usage: %s [smoke|prior-failures|acceptance|full]\n' "$0" >&2
    exit 2
    ;;
esac

for command_name in docker go harbor jq shasum tmux; do
  command -v "$command_name" >/dev/null 2>&1 || {
    printf 'required command not found: %s\n' "$command_name" >&2
    exit 1
  }
done

worktree_status="$(git -C "$repo_dir" -c core.fsmonitor=false status --porcelain --untracked-files=all)"
if [[ -n "$worktree_status" ]]; then
  printf 'Terminal-Bench requires a clean worktree so the recorded commit matches the executed binary.\n' >&2
  printf '%s\n' "$worktree_status" >&2
  exit 1
fi

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
ca_bundle_source="$("$harbor_python" -c 'import certifi; print(certifi.where())')"
[[ -f "$ca_bundle_source" ]] || {
  printf 'Harbor certifi CA bundle is unavailable: %s\n' "$ca_bundle_source" >&2
  exit 1
}

source_store="${CAELIS_EVAL_SOURCE_STORE:-$HOME/.caelis}"
source_config="$source_store/config.json"
profile_id="${CAELIS_EVAL_MODEL_PROFILE:-provider:xiaomi@token-plan-cn/xiaomi/mimo-v2.5-pro}"
endpoint_id="${CAELIS_EVAL_PROVIDER_ENDPOINT:-xiaomi@token-plan-cn}"
reasoning_effort="${CAELIS_EVAL_REASONING_EFFORT:-high}"
concurrency="${CAELIS_EVAL_CONCURRENCY:-2}"
timeout_multiplier="${CAELIS_EVAL_TIMEOUT_MULTIPLIER:-1.0}"
[[ -f "$source_config" ]] || {
  printf 'Caelis source config not found: %s\n' "$source_config" >&2
  exit 1
}

[[ "$concurrency" =~ ^[1-9][0-9]*$ ]] || {
  printf 'CAELIS_EVAL_CONCURRENCY must be a positive integer, got %s\n' "$concurrency" >&2
  exit 2
}
jq -en --arg value "$timeout_multiplier" '$value | tonumber > 0' >/dev/null || {
  printf 'CAELIS_EVAL_TIMEOUT_MULTIPLIER must be a positive number, got %s\n' "$timeout_multiplier" >&2
  exit 2
}

profile_json="$(jq -ec --arg profile "$profile_id" '.model_profiles.profiles[] | select(.id == $profile)' "$source_config")"
model_config_id="$(jq -er '.backend.provider.model_config_id' <<<"$profile_json")"
model_display_name="$(jq -r '.display_name // .id' <<<"$profile_json")"
jq -e --arg effort "$reasoning_effort" \
  'any(.effort.choices[]?; .canonical == $effort)' <<<"$profile_json" >/dev/null || {
  printf 'reasoning effort %s is not supported by model profile %s\n' "$reasoning_effort" "$profile_id" >&2
  exit 2
}
jq -e --arg model "$model_config_id" --arg endpoint "$endpoint_id" \
  'any(.models.configs[]?; .id == $model and .provider_endpoint_id == $endpoint)' \
  "$source_config" >/dev/null || {
  printf 'model config %s does not belong to provider endpoint %s\n' "$model_config_id" "$endpoint_id" >&2
  exit 2
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
started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
commit="$(git -C "$repo_dir" rev-parse HEAD)"
short_commit="${commit:0:12}"
branch="$(git -C "$repo_dir" symbolic-ref --quiet --short HEAD || printf 'detached')"
run_id="${CAELIS_EVAL_RUN_ID:-${timestamp}-${short_commit}-${taskset_name}}"
run_dir="$state_root/$run_id"
[[ ! -e "$run_dir" ]] || {
  printf 'evaluation run already exists: %s\n' "$run_dir" >&2
  exit 1
}
mkdir -p "$run_dir/bundle" "$run_dir/jobs"
exec > >(tee -a "$run_dir/start.log") 2>&1
ca_bundle_path="$run_dir/bundle/cacert.pem"
cp "$ca_bundle_source" "$ca_bundle_path"
chmod 644 "$ca_bundle_path"

cache_mode="${CAELIS_EVAL_VERIFIER_CACHE:-${CAELIS_EVAL_APT_CACHE:-enabled}}"
cache_metadata_path="$run_dir/bundle/verifier-cache.json"
cache_overlay_path="$run_dir/bundle/verifier-cache.compose.yaml"
case "$cache_mode" in
  enabled)
    "$script_dir/cache/prepare.sh" "$cache_metadata_path" "$cache_overlay_path"
    ;;
  disabled)
    jq -n \
      --arg schema_version "caelis.eval.terminalbench.cache/v1" \
      --arg mode "disabled" \
      '{schema_version:$schema_version, mode:$mode}' >"$cache_metadata_path"
    ;;
  *)
    printf 'CAELIS_EVAL_VERIFIER_CACHE must be enabled or disabled, got %s\n' "$cache_mode" >&2
    exit 2
    ;;
esac

config_path="$run_dir/bundle/config.json"
jq -e \
  --arg endpoint "$endpoint_id" \
  --arg profile "$profile_id" \
  --arg effort "$reasoning_effort" \
  '{
    schema_version,
    models: {
      provider_endpoints: [.models.provider_endpoints[] | select(.id == $endpoint)],
      configs: [.models.configs[] | select(.provider_endpoint_id == $endpoint)]
    },
    model_profiles: {
      default_profile_id: $profile,
      default_effort: $effort,
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
  go build -o "$run_dir/bundle/traceaudit" ./eval/terminalbench/cmd/traceaudit
)
chmod 700 "$binary_path" "$run_dir/bundle/traceaudit"
binary_digest="$(shasum -a 256 "$binary_path" | awk '{print $1}')"
traceaudit_digest="$(shasum -a 256 "$run_dir/bundle/traceaudit" | awk '{print $1}')"
config_digest="$(shasum -a 256 "$config_path" | awk '{print $1}')"

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

dataset_tasks_digest="$(shasum -a 256 "$dataset_tasks_path" | awk '{print $1}')"
tasks_digest="$(shasum -a 256 "$tasks_path" | awk '{print $1}')"

harness_digest="$(
  git -C "$repo_dir" ls-files eval/terminalbench \
    | LC_ALL=C sort \
    | while IFS= read -r relative_path; do
        shasum -a 256 "$repo_dir/$relative_path"
      done \
    | shasum -a 256 \
    | awk '{print $1}'
)"

go_version="$(go version)"
docker_version="$(docker version --format '{{.Client.Version}}/{{.Server.Version}}')"
python_version="$($harbor_python --version 2>&1)"
host_platform="$(uname -sm)"

dataset_digest="sha256:7d7bdc1cbedad549fc1140404bd4dc45e5fd0ea7c4186773687d177ad3a0699a"
jq -n \
  --arg schema_version "caelis.eval.terminalbench/v2" \
  --arg run_id "$run_id" \
  --arg commit "$commit" \
  --arg branch "$branch" \
  --arg started_at "$started_at" \
  --arg taskset "$taskset_name" \
  --arg dataset "terminal-bench/terminal-bench-2-1" \
  --arg dataset_digest "$dataset_digest" \
  --arg harbor_version "$harbor_version" \
  --arg profile_id "$profile_id" \
  --arg endpoint_id "$endpoint_id" \
  --arg model_config_id "$model_config_id" \
  --arg model_display_name "$model_display_name" \
  --arg reasoning_effort "$reasoning_effort" \
  --arg execution_mode "dangerously-skip-permissions" \
  --arg harness_digest "$harness_digest" \
  --arg binary_digest "$binary_digest" \
  --arg traceaudit_digest "$traceaudit_digest" \
  --arg config_digest "$config_digest" \
  --arg dataset_tasks_digest "$dataset_tasks_digest" \
  --arg tasks_digest "$tasks_digest" \
  --arg go_version "$go_version" \
  --arg docker_version "$docker_version" \
  --arg python_version "$python_version" \
  --arg host_platform "$host_platform" \
  --arg timeout_multiplier "$timeout_multiplier" \
  --argjson verifier_cache "$(cat "$cache_metadata_path")" \
  --argjson concurrency "$concurrency" \
  --argjson task_count "$task_count" \
  '{schema_version:$schema_version, run_id:$run_id, commit:$commit, branch:$branch,
    source_clean:true, started_at:$started_at, taskset:$taskset, dataset:$dataset,
    dataset_digest:$dataset_digest, harbor_version:$harbor_version,
    model_profile_id:$profile_id, provider_endpoint_id:$endpoint_id,
    model_config_id:$model_config_id, model:$model_display_name,
    reasoning_effort:$reasoning_effort,
    execution_mode:$execution_mode, harness_digest:$harness_digest,
    binary_digest:$binary_digest, traceaudit_digest:$traceaudit_digest,
    config_digest:$config_digest, dataset_tasks_digest:$dataset_tasks_digest,
    tasks_digest:$tasks_digest,
    runtime:{go:$go_version, docker:$docker_version, python:$python_version,
      host_platform:$host_platform},
    execution:{concurrency:$concurrency, max_retries:0,
      timeout_multiplier:$timeout_multiplier},
    verifier_cache:$verifier_cache,
    task_count:$task_count, status:"prepared"}' >"$run_dir/manifest.json"

tmux_name="caelis-tbench-${run_id//[^[:alnum:]-]/-}"
printf -v worker_command '%q %q %q %q %q %q' \
  "$script_dir/worker.sh" "$run_dir" "$credential_path" "$credential_name" "$ca_bundle_path" "$tmux_name"
tmux new-session -d -s "$tmux_name" "$worker_command"
tmux set-option -t "$tmux_name" remain-on-exit on

printf 'Terminal-Bench 2.1 run started\n'
printf '  tmux: %s\n' "$tmux_name"
printf '  tasks: %s (%s)\n' "$taskset_name" "$task_count"
printf '  artifacts: %s\n' "$run_dir"
printf 'attach with: tmux attach -t %s\n' "$tmux_name"
