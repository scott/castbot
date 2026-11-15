#!/usr/bin/env bash
# Fail when deploy.env or a private key is tracked.
# gitleaks still runs unchanged in CI. This check adds the file-name cases
# gitleaks does not treat as secrets by themselves.
set -euo pipefail

root=$(git rev-parse --show-toplevel)
cd "$root"

fail=0
while IFS= read -r path; do
  [[ -n "$path" ]] || continue
  base=${path##*/}
  case "$base" in
    deploy.env | id_rsa | id_dsa | id_ecdsa | id_ed25519 | *.pem | *.key)
      printf 'refusing tracked file: %s\n' "$path" >&2
      fail=1
      ;;
  esac
done < <(git ls-files)

if git grep -I -n -E '^-----BEGIN (OPENSSH |RSA |EC |DSA |ENCRYPTED )?PRIVATE KEY-----$' -- .; then
  printf 'private key material is tracked\n' >&2
  fail=1
fi

exit "$fail"
