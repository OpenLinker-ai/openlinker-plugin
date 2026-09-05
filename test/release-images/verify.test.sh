#!/bin/sh
set -eu
CDPATH=
export CDPATH

test_root=$(cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(cd -- "$test_root/../.." && pwd)
workflow="$repository_root/.github/workflows/images.yml"

require_workflow_value() {
  value=$1
  expected_count=$2
  actual_count=$(grep -F -c -- "$value" "$workflow" || true)
  if [ "$actual_count" != "$expected_count" ]; then
    echo "Provider image workflow contains $actual_count copies of '$value', want $expected_count" >&2
    exit 1
  fi
}

require_workflow_line() {
  value=$1
  expected_count=$2
  actual_count=$(grep -F -x -c -- "$value" "$workflow" || true)
  if [ "$actual_count" != "$expected_count" ]; then
    echo "Provider image workflow contains $actual_count exact lines '$value', want $expected_count" >&2
    exit 1
  fi
}

reject_workflow_value() {
  value=$1
  if grep -F -q -- "$value" "$workflow"; then
    echo "Provider image workflow must not contain '$value'" >&2
    exit 1
  fi
}

require_workflow_value "platform: linux/amd64" 1
require_workflow_value "platform: linux/arm64" 1
require_workflow_value '    runs-on: ${{ matrix.runner }}' 1
require_workflow_line "            runner: ubuntu-24.04" 1
require_workflow_line "            runner: ubuntu-24.04-arm" 1
require_workflow_value '      - "Dockerfile.browser.chrome"' 1
require_workflow_value '      - "Dockerfile.browser.native-chrome"' 1
require_workflow_value '      - "packages/browser-runtime/native-chrome/**"' 1
require_workflow_value '      - "cmd/**"' 1
require_workflow_value '      - "packages/**"' 1
require_workflow_value '      - "scripts/download-pinned-cli.mjs"' 1
require_workflow_value '      - "shared/cli-lock.json"' 1
require_workflow_value '          DOCKER_DEFAULT_PLATFORM: ${{ matrix.platform }}' 1
require_workflow_value "docker/setup-qemu-action@v3" 1
require_workflow_value "docker.io/tonistiigi/binfmt:qemu-v10.2.3-68@sha256:400a4873b838d1b89194d982c45e5fb3cda4593fbfd7e08a02e76b03b21166f0" 1
require_workflow_value "platforms: arm64" 1
require_workflow_value "run_live_provider:" 1
require_workflow_value "live-provider-acceptance:" 1
require_workflow_value "if: github.event_name == 'workflow_dispatch' && inputs.run_live_provider" 1
require_workflow_value "needs: browser-acceptance" 1
require_workflow_value "needs: [browser-acceptance, live-provider-acceptance]" 1
require_workflow_value "publish_images:" 1
require_workflow_value "push: \${{ github.event_name == 'workflow_dispatch' && inputs.publish_images && github.ref_type == 'tag' }}" 1
require_workflow_value "needs.live-provider-acceptance.result == 'success'" 1
reject_workflow_value "matrix.live_provider"
reject_workflow_value "github.ref_type == 'tag' || github.event_name == 'workflow_dispatch'"
reject_workflow_value '      - "pkg/**"'

temporary_root=$(mktemp -d "${TMPDIR:-/tmp}/openlinker-release-images.XXXXXX")
cleanup() {
  rm -rf -- "$temporary_root"
}
trap cleanup EXIT INT TERM

PATH="$test_root/fixtures:$PATH" \
  OPENLINKER_RELEASE_EVIDENCE_DIR="$temporary_root/evidence" \
  "$test_root/verify.sh" registry.example/openlinker v1.2.3 \
  >/dev/null

evidence_count=$(
  find "$temporary_root/evidence" -type f | wc -l | tr -d ' '
)
if [ "$evidence_count" != "12" ]; then
  echo "release verifier wrote $evidence_count evidence files, want 12" >&2
  exit 1
fi

if PATH="$test_root/fixtures:$PATH" \
  FAKE_MISSING_SBOM=1 \
  "$test_root/verify.sh" registry.example/openlinker v1.2.3 \
  >/dev/null 2>&1; then
  echo "release verifier accepted an image without an SPDX SBOM" >&2
  exit 1
fi

echo "Release image evidence verifier self-test passed"
