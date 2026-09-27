#!/usr/bin/env bash
# Verifies that every `uses: owner/repo@ref` in the workflows resolves to a real
# tag. GitHub only reports an unresolvable ref when the workflow actually runs,
# so a typo or an assumed floating major tag surfaces as a failed release rather
# than a failed check. Not every action publishes a floating major tag:
# sigstore/cosign-installer, for instance, has v2 and v3 but no v4.
set -euo pipefail

workflows=("${@:-.github/workflows}")
auth=()
if [ -n "${GITHUB_TOKEN:-}" ]; then
  auth=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
fi

status=0
checked=0
skipped=0
refs=$(grep -rhoE 'uses:[[:space:]]+[A-Za-z0-9._-]+/[A-Za-z0-9._-]+@[A-Za-z0-9._-]+' "${workflows[@]}" \
       | sed -E 's/uses:[[:space:]]+//' | sort -u)

for ref in $refs; do
  repo="${ref%@*}"
  tag="${ref#*@}"
  code=$(curl -sS -o /dev/null -w '%{http_code}' "${auth[@]}" \
    "https://api.github.com/repos/${repo}/git/ref/tags/${tag}")
  case "$code" in
    200) printf '  ok       %s\n' "$ref"; checked=$((checked + 1)) ;;
    403|429) printf '  skipped  %s (GitHub API rate limited)\n' "$ref"; skipped=$((skipped + 1)) ;;
    *)   printf '  MISSING  %s — no such tag (HTTP %s)\n' "$ref" "$code"; status=1 ;;
  esac
done

printf '\n%d verified, %d skipped\n' "$checked" "$skipped"
if [ "$skipped" -gt 0 ]; then
  echo "note: skipped refs were NOT verified. Set GITHUB_TOKEN to raise the API rate limit."
fi

if [ "$status" -ne 0 ]; then
  echo
  echo "FAIL: one or more actions reference a tag that does not exist."
  echo "Pin the full version (e.g. @v4.1.2) when an action publishes no floating major tag."
fi
exit "$status"
