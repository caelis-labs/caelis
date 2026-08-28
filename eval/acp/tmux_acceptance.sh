#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_dir="$(cd "$script_dir/../.." && pwd)"
for command_name in go jq shasum tmux; do
  command -v "$command_name" >/dev/null 2>&1 || {
    printf 'required command not found: %s\n' "$command_name" >&2
    exit 1
  }
done

source_store="${CAELIS_EVAL_SOURCE_STORE:-$HOME/.caelis}"
source_config="$source_store/config.json"
profile_id="${CAELIS_EVAL_MODEL_PROFILE:-provider:xiaomi@token-plan-cn/xiaomi/mimo-v2.5-pro}"
state_root="${CAELIS_ACP_TMUX_EVAL_ROOT:-${XDG_STATE_HOME:-$HOME/.local/state}/caelis/evals/acp-tmux}"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
commit="$(git -C "$repo_dir" rev-parse --short=12 HEAD)"
run_id="${CAELIS_EVAL_RUN_ID:-${timestamp}-${commit}}"
run_dir="$state_root/$run_id"
[[ -f "$source_config" ]] || {
  printf 'Caelis source config not found: %s\n' "$source_config" >&2
  exit 1
}
[[ ! -e "$run_dir" ]] || {
  printf 'ACP tmux evaluation run already exists: %s\n' "$run_dir" >&2
  exit 1
}
mkdir -p "$run_dir/bin" "$run_dir/logs" "$run_dir/workspace"

runtime_store="$(mktemp -d "${TMPDIR:-/tmp}/caelis-acp-tmux-eval.XXXXXX")"
tmux_name="caelis-acp-${run_id//[^[:alnum:]-]/-}"
cleanup() {
  if tmux has-session -t "$tmux_name" 2>/dev/null; then
    tmux kill-session -t "$tmux_name"
  fi
  [[ -n "$runtime_store" && -d "$runtime_store" ]] && rm -rf -- "$runtime_store"
}
trap cleanup EXIT
mkdir -p "$runtime_store/providers/credentials" "$runtime_store/child-sessions"
chmod 700 "$runtime_store" "$runtime_store/providers" "$runtime_store/providers/credentials"

model_config_id="$(jq -er --arg profile "$profile_id" '
  .model_profiles.profiles[]
  | select(.id == $profile)
  | .backend.provider.model_config_id
' "$source_config")"
endpoint_id="$(jq -er --arg model "$model_config_id" '
  .models.configs[]
  | select(.id == $model)
  | .provider_endpoint_id
' "$source_config")"
credential_ref="$(jq -er --arg endpoint "$endpoint_id" '
  .models.provider_endpoints[]
  | select(.id == $endpoint)
  | .credential_ref
' "$source_config")"
credential_hash="$(printf '%s' "$credential_ref" | shasum -a 256 | awk '{print $1}')"
credential_path="$source_store/providers/credentials/$credential_hash.json"
[[ -f "$credential_path" ]] || {
  printf 'credential file for endpoint %s is unavailable\n' "$endpoint_id" >&2
  exit 1
}
cp "$credential_path" "$runtime_store/providers/credentials/$credential_hash.json"
chmod 600 "$runtime_store/providers/credentials/$credential_hash.json"

binary="$run_dir/bin/caelis"
external_agent="$run_dir/bin/acpe2eagent"
(
  cd "$repo_dir"
  go build \
    -ldflags "-X github.com/caelis-labs/caelis/internal/version.Version=eval -X github.com/caelis-labs/caelis/internal/version.Commit=$commit -X github.com/caelis-labs/caelis/internal/version.Date=$timestamp" \
    -o "$binary" ./cmd/caelis
  go build -o "$external_agent" ./internal/acpe2eagent
)

external_profile_id="acp:scripted-approval:default"
jq -e \
  --arg endpoint "$endpoint_id" \
  --arg model "$model_config_id" \
  --arg profile "$profile_id" \
  --arg external_profile "$external_profile_id" \
  --arg external_agent "$external_agent" \
  --arg external_workdir "$run_dir/workspace" \
  --arg child_sessions "$runtime_store/child-sessions" \
  '{
    schema_version,
    models: {
      provider_endpoints: [.models.provider_endpoints[] | select(.id == $endpoint)],
      configs: [.models.configs[] | select(.id == $model)]
    },
    external_agents: {
      connections: [{
        id: "scripted-approval",
        name: "Scripted Approval ACP",
        launcher: {
          kind: "executable",
          command: $external_agent,
          env: {
            SDK_ACP_SCRIPTED_MODE: "approval_command",
            SDK_ACP_SESSION_ROOT: $child_sessions
          },
          work_dir: $external_workdir
        }
      }],
      agents: [{
        id: "scripted-approval",
        name: "Scripted Approval ACP",
        connection_id: "scripted-approval"
      }]
    },
    model_profiles: {
      default_profile_id: $profile,
      default_effort: "high",
      profiles: ([.model_profiles.profiles[] | select(.id == $profile)] + [{
        id: $external_profile,
        display_name: "Scripted Approval ACP",
        backend: {acp: {
          agent_id: "scripted-approval",
          remote_model_id: "caelis:agent-default"
        }},
        effort: {
          default_effort: "none",
          choices: [{canonical: "none", wire_value: ""}]
        }
      }])
    },
    agent_bindings: {bindings: [
      {handle: "zenith", profile_id: $external_profile, effort: "none"},
      {handle: "guardian", profile_id: $profile, effort: "high"}
    ]},
    sandbox: {requested_type: "host"},
    runtime: {approval_mode: "auto-review", policy_profile: "workspace-write"},
    plugins: []
  }
  | select((.models.provider_endpoints | length) == 1)
  | select((.models.configs | length) == 1)
  | select((.model_profiles.profiles | length) == 2)' \
  "$source_config" >"$runtime_store/config.json"
chmod 600 "$runtime_store/config.json"

printf 'ACP_TMUX_ACCEPTANCE_WORKSPACE\n' >"$run_dir/workspace/README.txt"
jq -n \
  --arg schema_version "caelis.eval.acp-tmux/v1" \
  --arg run_id "$run_id" \
  --arg commit "$commit" \
  --arg started_at "$timestamp" \
  --arg profile_id "$profile_id" \
  --arg external_profile_id "$external_profile_id" \
  '{schema_version:$schema_version, run_id:$run_id, commit:$commit,
    started_at:$started_at, model_profile_id:$profile_id,
    external_profile_id:$external_profile_id, approval_mode:"auto-review",
    scenarios:["side_acp", "spawn_external_acp_subagent"], status:"running"}' \
  >"$run_dir/manifest.json"

printf -v quoted_workspace '%q' "$run_dir/workspace"
printf -v quoted_binary '%q' "$binary"
printf -v quoted_store '%q' "$runtime_store"
worker_command="cd $quoted_workspace && exec env TERM=xterm-256color CAELIS_TUI_NO_ANIMATION=1 CAELIS_TUI_RENDER_FPS=30 $quoted_binary --embedded --store-dir $quoted_store --no-animation"
tmux new-session -d -s "$tmux_name" -x 140 -y 48 "$worker_command"
tmux set-option -t "$tmux_name" remain-on-exit on
tmux_target="$tmux_name:0.0"

capture_plain() {
  tmux capture-pane -p -t "$tmux_target" -S -2000
}

save_capture() {
  capture_plain >"$1"
}

fail_run() {
  local reason="$1"
  save_capture "$run_dir/logs/failure-tui.txt" || true
  printf '%s\n' "$reason" >"$run_dir/logs/failure.txt"
  jq --arg reason "$reason" '.status = "failed" | .failure = $reason' \
    "$run_dir/manifest.json" >"$run_dir/manifest.next.json"
  mv "$run_dir/manifest.next.json" "$run_dir/manifest.json"
  printf 'ACP tmux acceptance failed: %s\nArtifacts: %s\n' "$reason" "$run_dir" >&2
  exit 1
}

wait_for_text() {
  local text="$1"
  local timeout_seconds="$2"
  local deadline=$((SECONDS + timeout_seconds))
  while ((SECONDS < deadline)); do
    if capture_plain | grep -Fq "$text"; then
      return 0
    fi
    if ! tmux has-session -t "$tmux_name" 2>/dev/null; then
      return 1
    fi
    sleep 1
  done
  return 1
}

wait_for_text "xiaomi/mimo-v2.5-pro" 60 || fail_run "TUI did not become ready"

side_prompt="/zenith Run the scripted approval command flow."
tmux send-keys -t "$tmux_target" -l "$side_prompt"
tmux send-keys -t "$tmux_target" Enter
wait_for_text "child approval ok" 240 || fail_run "Side ACP did not finish the approval command flow"
wait_for_text "Automatic approval review approved" 240 || fail_run "Side ACP did not display an approved automatic review"
# The participant's final message can render just before its terminal lifecycle.
# Let that lifecycle restore main-Turn input before submitting the next scenario.
sleep 3
save_capture "$run_dir/logs/side-acp-tui.txt"

spawn_prompt="Use Spawn exactly once with agent zenith and prompt 'Run the scripted approval command flow.' Then use Task wait until it completes. After the child returns, reply with exactly the token formed by joining SPAWN_ACP_ and OK, with no spaces."
tmux send-keys -t "$tmux_target" -l "$spawn_prompt"
tmux send-keys -t "$tmux_target" Enter
wait_for_text "SPAWN_ACP_OK" 360 || fail_run "Spawn external ACP subagent flow did not finish"
wait_for_text "[zenith]:" 60 || fail_run "Spawn external ACP subagent row was not rendered"
save_capture "$run_dir/logs/spawn-acp-tui.txt"

ready=false
side_final_visible=false
auto_review_visible=false
spawn_row_visible=false
spawn_final_visible=false
interactive_permission_prompt_visible=false
grep -Fq "xiaomi/mimo-v2.5-pro" "$run_dir/logs/side-acp-tui.txt" && ready=true
grep -Fq "child approval ok" "$run_dir/logs/side-acp-tui.txt" && side_final_visible=true
grep -Fq "Automatic approval review approved" "$run_dir/logs/side-acp-tui.txt" && auto_review_visible=true
grep -Fq "[zenith]:" "$run_dir/logs/spawn-acp-tui.txt" && spawn_row_visible=true
grep -Fq "SPAWN_ACP_OK" "$run_dir/logs/spawn-acp-tui.txt" && spawn_final_visible=true
if grep -Eq 'Allow once|Reject once|Always allow' "$run_dir/logs/side-acp-tui.txt" "$run_dir/logs/spawn-acp-tui.txt"; then
  interactive_permission_prompt_visible=true
fi

passed=false
if [[ "$ready" == true && "$side_final_visible" == true && "$auto_review_visible" == true &&
      "$spawn_row_visible" == true && "$spawn_final_visible" == true &&
      "$interactive_permission_prompt_visible" == false ]]; then
  passed=true
fi
finished_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
jq -n \
  --arg schema_version "caelis.eval.acp-tmux.report/v1" \
  --arg run_id "$run_id" \
  --arg commit "$commit" \
  --arg finished_at "$finished_at" \
  --argjson ready "$ready" \
  --argjson side_final_visible "$side_final_visible" \
  --argjson auto_review_visible "$auto_review_visible" \
  --argjson spawn_row_visible "$spawn_row_visible" \
  --argjson spawn_final_visible "$spawn_final_visible" \
  --argjson interactive_permission_prompt_visible "$interactive_permission_prompt_visible" \
  --argjson passed "$passed" \
  '{schema_version:$schema_version, run_id:$run_id, commit:$commit,
    finished_at:$finished_at, approval_mode:"auto-review",
    permission_decision_keystrokes_sent:false, ready:$ready,
    side_acp:{final_visible:$side_final_visible, auto_review_visible:$auto_review_visible},
    spawn_external_acp_subagent:{row_visible:$spawn_row_visible, final_visible:$spawn_final_visible},
    interactive_permission_prompt_visible:$interactive_permission_prompt_visible,
    passed:$passed}' >"$run_dir/report.json"

jq \
  --arg finished_at "$finished_at" \
  --argjson passed "$passed" \
  '.finished_at = $finished_at | .status = (if $passed then "completed" else "failed" end)' \
  "$run_dir/manifest.json" >"$run_dir/manifest.next.json"
mv "$run_dir/manifest.next.json" "$run_dir/manifest.json"

tmux send-keys -t "$tmux_target" -l "/quit"
tmux send-keys -t "$tmux_target" Enter

printf 'ACP tmux acceptance artifacts: %s\n' "$run_dir"
jq . "$run_dir/report.json"
[[ "$passed" == true ]]
