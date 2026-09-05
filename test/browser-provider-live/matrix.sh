#!/bin/sh
set -eu
umask 077

require_value() {
  name=$1
  value=$(printenv "$name" 2>/dev/null || true)
  if [ -z "$value" ]; then
    echo "${name} is required by the credential-backed Browser Provider gate" >&2
    exit 1
  fi
}

for name in \
  OPENLINKER_BROWSER_LIVE_CODEX_API_KEY_FILE \
  OPENLINKER_BROWSER_LIVE_ANTHROPIC_API_KEY_FILE \
  OPENLINKER_BROWSER_LIVE_FIXTURE_URL \
  OPENLINKER_BROWSER_LIVE_FIXTURE_MARKER \
  OPENLINKER_BROWSER_LIVE_INTERNAL_NETWORK \
  OPENLINKER_BROWSER_LIVE_CONTROL_VOLUME \
  OPENLINKER_BROWSER_LIVE_EGRESS_IP \
  OPENLINKER_BROWSER_LIVE_PREFIX; do
  require_value "$name"
done

repository_root=$(cd -- "$(dirname -- "$0")/../.." && pwd)
codex_image="openlinker-agent-codex:${OPENLINKER_BROWSER_LIVE_PREFIX}"
claude_image="openlinker-agent-claude:${OPENLINKER_BROWSER_LIVE_PREFIX}"
result_file=$(mktemp "${TMPDIR:-/tmp}/openlinker-browser-provider-live.XXXXXX")
cleanup() {
  status=$?
  docker rm -f \
    "${OPENLINKER_BROWSER_LIVE_PREFIX}-codex-native" \
    "${OPENLINKER_BROWSER_LIVE_PREFIX}-codex-mcp" \
    "${OPENLINKER_BROWSER_LIVE_PREFIX}-claude-native" \
    "${OPENLINKER_BROWSER_LIVE_PREFIX}-claude-mcp" \
    >/dev/null 2>&1 || true
  rm -f "$result_file"
  return "$status"
}
trap cleanup EXIT INT TERM

docker build \
  --target codex-live \
  -f "$repository_root/Dockerfile.providers" \
  -t "$codex_image" \
  "$repository_root"
docker build \
  --target claude-live \
  -f "$repository_root/Dockerfile.providers" \
  -t "$claude_image" \
  "$repository_root"

run_quadrant() {
  provider=$1
  mode=$2
  image=$3
  credential_file=$4

  output=$(
    docker run --rm -i \
      --name "${OPENLINKER_BROWSER_LIVE_PREFIX}-${provider}-${mode}" \
      --network "$OPENLINKER_BROWSER_LIVE_INTERNAL_NETWORK" \
      --read-only \
      --cap-drop ALL \
      --cap-add SETUID \
      --cap-add SETGID \
      --security-opt no-new-privileges:true \
      --pids-limit 256 \
      --memory 4g \
      --cpus 2 \
      --tmpfs /runtime:rw,noexec,nosuid,nodev,size=32m,uid=10001,gid=10001,mode=0700 \
      --tmpfs /provider:rw,noexec,nosuid,nodev,size=256m,uid=10002,gid=10002,mode=0700 \
      --tmpfs /browser-tool:rw,noexec,nosuid,nodev,size=4m,uid=10001,gid=10002,mode=2710 \
      --tmpfs /tmp:rw,noexec,nosuid,nodev,size=128m,mode=1777 \
      --mount "type=volume,src=${OPENLINKER_BROWSER_LIVE_CONTROL_VOLUME},dst=/browser-control" \
      -e HOME=/provider \
      -e CODEX_HOME=/provider \
      -e CLAUDE_CONFIG_DIR=/provider \
      -e "HTTP_PROXY=http://${OPENLINKER_BROWSER_LIVE_EGRESS_IP}:3128" \
      -e "HTTPS_PROXY=http://${OPENLINKER_BROWSER_LIVE_EGRESS_IP}:3128" \
      -e "http_proxy=http://${OPENLINKER_BROWSER_LIVE_EGRESS_IP}:3128" \
      -e "https_proxy=http://${OPENLINKER_BROWSER_LIVE_EGRESS_IP}:3128" \
      -e NO_PROXY= \
      -e no_proxy= \
      -e "OPENLINKER_CODEX_BASE_URL=${OPENLINKER_CODEX_BASE_URL:-}" \
      -e "OPENLINKER_CODEX_MODEL=${OPENLINKER_CODEX_MODEL:-}" \
      -e "OPENLINKER_CLAUDE_MODEL=${OPENLINKER_CLAUDE_MODEL:-}" \
      "$image" \
      --mode "$mode" \
      --fixture-url "${OPENLINKER_BROWSER_LIVE_FIXTURE_URL%/}/provider-live" \
      <"$credential_file"
  )
  printf '%s\n' "$output" >>"$result_file"
  printf '%s\n' "${provider}/${mode}: ${output}"
}

run_quadrant \
  codex native "$codex_image" \
  "$OPENLINKER_BROWSER_LIVE_CODEX_API_KEY_FILE"
run_quadrant \
  codex mcp "$codex_image" \
  "$OPENLINKER_BROWSER_LIVE_CODEX_API_KEY_FILE"
run_quadrant \
  claude native "$claude_image" \
  "$OPENLINKER_BROWSER_LIVE_ANTHROPIC_API_KEY_FILE"
run_quadrant \
  claude mcp "$claude_image" \
  "$OPENLINKER_BROWSER_LIVE_ANTHROPIC_API_KEY_FILE"

node "$repository_root/test/browser-provider-live/verify-results.mjs" \
  "$result_file" \
  "$OPENLINKER_BROWSER_LIVE_FIXTURE_MARKER"

metrics=$(curl -fsS --max-time 10 "${OPENLINKER_BROWSER_LIVE_FIXTURE_URL%/}/metrics")
METRICS="$metrics" node -e '
  const metrics = JSON.parse(process.env.METRICS);
  if (!Number.isInteger(metrics.provider_live_browsers) || metrics.provider_live_browsers < 4) {
    throw new Error(`expected at least four real Chromium fixture documents, got ${metrics.provider_live_browsers}`);
  }
'

if [ -n "${OPENLINKER_BROWSER_LIVE_EVIDENCE_FILE:-}" ]; then
  evidence_parent=$(dirname -- "$OPENLINKER_BROWSER_LIVE_EVIDENCE_FILE")
  if [ ! -d "$evidence_parent" ]; then
    echo "Browser Provider evidence parent directory does not exist" >&2
    exit 1
  fi
  cp "$result_file" "$OPENLINKER_BROWSER_LIVE_EVIDENCE_FILE"
  chmod 0600 "$OPENLINKER_BROWSER_LIVE_EVIDENCE_FILE"
fi

echo "Credential-backed Codex/Claude native-Plugin/direct-MCP Browser matrix passed"
