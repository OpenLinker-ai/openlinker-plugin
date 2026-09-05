#!/usr/bin/env bash
set -euo pipefail

contract_roots=(
  etc/fonts
  usr/share/fontconfig
  usr/share/fonts
  usr/local/share/fonts
)

for contract_root in "${contract_roots[@]}"; do
  if [[ ! -d "/${contract_root}" ]]; then
    echo "missing Browser font-contract root: /${contract_root}" >&2
    exit 1
  fi
done

manifest_file="$(mktemp)"
trap 'rm -f "${manifest_file}"' EXIT

(
  cd /
  find "${contract_roots[@]}" \( -type f -o -type l \) -print0 |
    LC_ALL=C sort -z |
    while IFS= read -r -d '' item; do
      if [[ -L "${item}" ]]; then
        printf 'SYMLINK\t%s\t%s\n' "$(readlink "${item}")" "${item}"
      else
        printf 'FILE\t%s\t%s\n' \
          "$(sha256sum "${item}" | awk '{print $1}')" \
          "${item}"
      fi
    done
) >"${manifest_file}"

sha256sum "${manifest_file}" | awk '{print $1}'
