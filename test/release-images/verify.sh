#!/bin/sh
set -eu
CDPATH=
export CDPATH

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <registry/owner> <release-tag>" >&2
  exit 2
fi

command -v docker >/dev/null 2>&1 || {
  echo "docker is required" >&2
  exit 1
}
command -v jq >/dev/null 2>&1 || {
  echo "jq is required" >&2
  exit 1
}

registry_owner=${1%/}
release_tag=$2
case "$registry_owner" in
  http://*|https://*|*'?'*|*'#'*|'')
    echo "registry/owner must be a registry path without scheme, query, or fragment" >&2
    exit 2
    ;;
esac
case "$release_tag" in
  *[!A-Za-z0-9._-]*|'')
    echo "release tag contains unsupported characters" >&2
    exit 2
    ;;
esac

evidence_directory=${OPENLINKER_RELEASE_EVIDENCE_DIR:-}
if [ -n "$evidence_directory" ]; then
  mkdir -p "$evidence_directory"
fi

for image_name in \
  openlinker-agent-codex \
  openlinker-agent-claude \
  openlinker-egress-gateway \
  openlinker-browser-runtime; do
  image_reference="${registry_owner}/${image_name}:${release_tag}"
  index_json=$(docker buildx imagetools inspect --raw "$image_reference")
  echo "$index_json" | jq -e '
    .mediaType
    | contains("image.index") or contains("manifest.list")
  ' >/dev/null

  if [ -n "$evidence_directory" ]; then
    echo "$index_json" >"$evidence_directory/${image_name}-index.json"
  fi

  for architecture in amd64 arm64; do
    platform_digest=$(
      echo "$index_json" |
        jq -er --arg architecture "$architecture" '
          [
            .manifests[]
            | select(
                .platform.os == "linux"
                and .platform.architecture == $architecture
              )
            | .digest
          ]
          | if length == 1 then .[0] else error(
              "expected exactly one linux/" + $architecture + " manifest"
            ) end
        '
    )
    attestation_digest=$(
      echo "$index_json" |
        jq -er --arg platform_digest "$platform_digest" '
          [
            .manifests[]
            | select(
                .annotations["vnd.docker.reference.type"]
                  == "attestation-manifest"
                and .annotations["vnd.docker.reference.digest"]
                  == $platform_digest
              )
            | .digest
          ]
          | if length == 1 then .[0] else error(
              "expected exactly one attestation manifest for " + $platform_digest
            ) end
        '
    )
    attestation_json=$(
      docker buildx imagetools inspect \
        --raw \
        "${registry_owner}/${image_name}@${attestation_digest}"
    )
    echo "$attestation_json" | jq -e '
      [
        .layers[]?
        | .annotations["in-toto.io/predicate-type"]
      ]
      | any(. == "https://spdx.dev/Document")
    ' >/dev/null
    echo "$attestation_json" | jq -e '
      [
        .layers[]?
        | .annotations["in-toto.io/predicate-type"]
      ]
      | any(startswith("https://slsa.dev/provenance/"))
    ' >/dev/null

    if [ -n "$evidence_directory" ]; then
      echo "$attestation_json" \
        >"$evidence_directory/${image_name}-${architecture}-attestations.json"
    fi
  done
  echo "verified ${image_reference}: linux/amd64 + linux/arm64 + SPDX SBOM + SLSA provenance"
done
