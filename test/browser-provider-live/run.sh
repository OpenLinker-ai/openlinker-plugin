#!/bin/sh
set -eu

command -v docker >/dev/null 2>&1 || {
  echo "docker is required" >&2
  exit 1
}
command -v node >/dev/null 2>&1 || {
  echo "node is required" >&2
  exit 1
}

validate_credential_file() {
  name=$1
  path=$(printenv "$name" 2>/dev/null || true)
  if [ -z "$path" ]; then
    echo "${name} is required; missing credentials block this release gate" >&2
    exit 1
  fi
  case "$path" in
    /*) ;;
    *)
      echo "${name} must be an absolute path" >&2
      exit 1
      ;;
  esac
  if [ -L "$path" ] || [ ! -f "$path" ] || [ ! -s "$path" ]; then
    echo "${name} must name a non-empty regular file and not a symlink" >&2
    exit 1
  fi
  size=$(wc -c <"$path" | tr -d ' ')
  if [ "$size" -gt 65536 ]; then
    echo "${name} exceeds the 64 KiB credential limit" >&2
    exit 1
  fi
  owner=$(stat -c '%u' "$path" 2>/dev/null || stat -f '%u' "$path")
  permissions=$(stat -c '%a' "$path" 2>/dev/null || stat -f '%Lp' "$path")
  if [ "$owner" != "$(id -u)" ] || [ $((0$permissions & 077)) -ne 0 ]; then
    echo "${name} must be owned by the current user and inaccessible to group/other" >&2
    exit 1
  fi
}

validate_credential_file OPENLINKER_BROWSER_LIVE_CODEX_API_KEY_FILE
validate_credential_file OPENLINKER_BROWSER_LIVE_ANTHROPIC_API_KEY_FILE

repository_root=$(cd -- "$(dirname -- "$0")/../.." && pwd)
OPENLINKER_BROWSER_ACCEPTANCE_LIVE_PROVIDER_MATRIX=1 \
  exec "$repository_root/test/browser-image/run.sh"
