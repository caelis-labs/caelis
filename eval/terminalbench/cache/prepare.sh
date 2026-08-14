#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
metadata_path="${1:?cache metadata output path is required}"
overlay_path="${2:?cache compose overlay output path is required}"

apt_image_context="$script_dir/apt-cacher-ng"
uv_image_context="$script_dir/uv-mirror"
network_name="caelis-terminalbench-cache"
volume_name="caelis-terminalbench-apt-cache-v1"
apt_container_name="caelis-terminalbench-apt-cache"
apt_container_alias="apt-cache"
apt_component="terminalbench-apt-cache"
apt_component_label="com.caelis.eval.component=$apt_component"
uv_container_name="caelis-terminalbench-uv-cache"
uv_container_alias="uv-cache"
uv_component="terminalbench-uv-cache"
uv_component_label="com.caelis.eval.component=$uv_component"

config_digest="$(
  shasum -a 256 \
    "$apt_image_context/Dockerfile" \
    "$uv_image_context/Dockerfile" \
    "$script_dir/compose.yaml" \
    | shasum -a 256 \
    | awk '{print $1}'
)"
apt_config_digest="$(shasum -a 256 "$apt_image_context/Dockerfile" | awk '{print $1}')"
uv_config_digest="$(shasum -a 256 "$uv_image_context/Dockerfile" | awk '{print $1}')"
apt_image_name="caelis/terminalbench-apt-cache:${apt_config_digest:0:16}"
uv_image_name="caelis/terminalbench-uv-cache:${uv_config_digest:0:16}"

apt_image_reused=false
if docker image inspect "$apt_image_name" >/dev/null 2>&1; then
  apt_image_reused=true
else
  printf 'Building Terminal-Bench APT cache image %s\n' "$apt_image_name" >&2
  docker build --tag "$apt_image_name" "$apt_image_context" >&2
fi
apt_image_owner="$(docker image inspect --format '{{index .Config.Labels "com.caelis.eval.component"}}' "$apt_image_name")"
if [[ "$apt_image_owner" != "$apt_component" ]]; then
  printf 'image %s exists but is not owned by the Caelis Terminal-Bench cache\n' "$apt_image_name" >&2
  exit 1
fi
apt_image_id="$(docker image inspect --format '{{.Id}}' "$apt_image_name")"

uv_image_reused=false
if docker image inspect "$uv_image_name" >/dev/null 2>&1; then
  uv_image_reused=true
else
  printf 'Building Terminal-Bench uv cache image %s\n' "$uv_image_name" >&2
  docker build --tag "$uv_image_name" "$uv_image_context" >&2
fi
uv_image_owner="$(docker image inspect --format '{{index .Config.Labels "com.caelis.eval.component"}}' "$uv_image_name")"
if [[ "$uv_image_owner" != "$uv_component" ]]; then
  printf 'image %s exists but is not owned by the Caelis Terminal-Bench cache\n' "$uv_image_name" >&2
  exit 1
fi
uv_image_id="$(docker image inspect --format '{{.Id}}' "$uv_image_name")"

if ! docker network inspect "$network_name" >/dev/null 2>&1; then
  docker network create --label "$apt_component_label" "$network_name" >/dev/null || \
    docker network inspect "$network_name" >/dev/null
fi
network_owner="$(docker network inspect --format '{{index .Labels "com.caelis.eval.component"}}' "$network_name")"
if [[ "$network_owner" != "$apt_component" ]]; then
  printf 'network %s exists but is not owned by the Caelis Terminal-Bench cache\n' "$network_name" >&2
  exit 1
fi

volume_reused=true
if ! docker volume inspect "$volume_name" >/dev/null 2>&1; then
  volume_reused=false
  docker volume create --label "$apt_component_label" "$volume_name" >/dev/null || \
    docker volume inspect "$volume_name" >/dev/null
fi
volume_owner="$(docker volume inspect --format '{{index .Labels "com.caelis.eval.component"}}' "$volume_name")"
if [[ "$volume_owner" != "$apt_component" ]]; then
  printf 'volume %s exists but is not owned by the Caelis Terminal-Bench cache\n' "$volume_name" >&2
  exit 1
fi

apt_container_reused=false
if docker container inspect "$apt_container_name" >/dev/null 2>&1; then
  owner="$(docker container inspect --format '{{index .Config.Labels "com.caelis.eval.component"}}' "$apt_container_name")"
  if [[ "$owner" != "$apt_component" ]]; then
    printf 'container %s exists but is not owned by the Caelis Terminal-Bench cache\n' "$apt_container_name" >&2
    exit 1
  fi
  current_image="$(docker container inspect --format '{{.Config.Image}}' "$apt_container_name")"
  current_volume="$(docker container inspect --format '{{range .Mounts}}{{if eq .Destination "/var/cache/apt-cacher-ng"}}{{.Name}}{{end}}{{end}}' "$apt_container_name")"
  if [[ "$current_image" == "$apt_image_name" && "$current_volume" == "$volume_name" ]]; then
    apt_container_reused=true
  else
    docker container rm --force "$apt_container_name" >/dev/null
  fi
fi
if ! docker container inspect "$apt_container_name" >/dev/null 2>&1; then
  docker run --detach \
    --name "$apt_container_name" \
    --label "$apt_component_label" \
    --network "$network_name" \
    --network-alias "$apt_container_alias" \
    --restart unless-stopped \
    --volume "$volume_name:/var/cache/apt-cacher-ng" \
    "$apt_image_name" >/dev/null
elif [[ "$(docker container inspect --format '{{.State.Running}}' "$apt_container_name")" != "true" ]]; then
  docker container start "$apt_container_name" >/dev/null
fi

uv_container_reused=false
if docker container inspect "$uv_container_name" >/dev/null 2>&1; then
  owner="$(docker container inspect --format '{{index .Config.Labels "com.caelis.eval.component"}}' "$uv_container_name")"
  if [[ "$owner" != "$uv_component" ]]; then
    printf 'container %s exists but is not owned by the Caelis Terminal-Bench cache\n' "$uv_container_name" >&2
    exit 1
  fi
  current_image="$(docker container inspect --format '{{.Config.Image}}' "$uv_container_name")"
  if [[ "$current_image" == "$uv_image_name" ]]; then
    uv_container_reused=true
  else
    docker container rm --force "$uv_container_name" >/dev/null
  fi
fi
if ! docker container inspect "$uv_container_name" >/dev/null 2>&1; then
  docker run --detach \
    --name "$uv_container_name" \
    --label "$uv_component_label" \
    --network "$network_name" \
    --network-alias "$uv_container_alias" \
    --restart unless-stopped \
    "$uv_image_name" >/dev/null
elif [[ "$(docker container inspect --format '{{.State.Running}}' "$uv_container_name")" != "true" ]]; then
  docker container start "$uv_container_name" >/dev/null
fi

wait_for_health() {
  local name="$1"
  local description="$2"
  local health
  for _ in $(seq 1 30); do
    health="$(docker container inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}missing{{end}}' "$name")"
    case "$health" in
      healthy) return ;;
      unhealthy)
        docker container logs "$name" >&2 || true
        printf 'Terminal-Bench %s failed its health check\n' "$description" >&2
        exit 1
        ;;
    esac
    sleep 1
  done
  docker container logs "$name" >&2 || true
  printf 'Terminal-Bench %s did not become healthy within 30 seconds\n' "$description" >&2
  exit 1
}

wait_for_health "$apt_container_name" "APT cache"
wait_for_health "$uv_container_name" "uv cache"

cp "$script_dir/compose.yaml" "$overlay_path"
jq -n \
  --arg schema_version "caelis.eval.terminalbench.cache/v1" \
  --arg mode "apt-cacher-ng+uv-mirror" \
  --arg network "$network_name" \
  --arg config_digest "$config_digest" \
  --arg apt_proxy_url "http://$apt_container_alias:3142" \
  --arg apt_image "$apt_image_name" \
  --arg apt_image_id "$apt_image_id" \
  --arg apt_volume "$volume_name" \
  --argjson apt_image_reused "$apt_image_reused" \
  --argjson apt_volume_reused "$volume_reused" \
  --argjson apt_container_reused "$apt_container_reused" \
  --arg uv_version "0.9.5" \
  --arg uv_download_url "http://$uv_container_alias:8080" \
  --arg uv_image "$uv_image_name" \
  --arg uv_image_id "$uv_image_id" \
  --argjson uv_image_reused "$uv_image_reused" \
  --argjson uv_container_reused "$uv_container_reused" \
  '{schema_version:$schema_version, mode:$mode, network:$network,
    config_digest:$config_digest,
    apt:{proxy_url:$apt_proxy_url, image:$apt_image, image_id:$apt_image_id,
      volume:$apt_volume, image_reused:$apt_image_reused,
      volume_reused:$apt_volume_reused, container_reused:$apt_container_reused},
    uv:{version:$uv_version, download_url:$uv_download_url, image:$uv_image,
      image_id:$uv_image_id, image_reused:$uv_image_reused,
      container_reused:$uv_container_reused}}' \
  >"$metadata_path"
