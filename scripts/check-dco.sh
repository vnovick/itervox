#!/usr/bin/env bash
# check-dco.sh — verify every commit in a range carries a Developer Certificate
# of Origin sign-off that matches the commit author.
#
# Usage: scripts/check-dco.sh <base> [<head>]
#
#   <base>  the commit the range starts from (exclusive), e.g. the PR base SHA
#   <head>  the last commit of the range (inclusive); defaults to HEAD
#
# Every non-merge commit in <base>..<head> must contain at least one line
#
#   Signed-off-by: Full Name <author-email>
#
# whose email equals the commit's author email (compared case-insensitively),
# which is what `git commit -s` writes. Merge commits are skipped, as the DCO
# GitHub App does. See DCO at the repo root and CONTRIBUTING.md.
#
# Used by .github/workflows/dco.yml for pull requests from forks; tested by
# scripts/check-dco_test.sh (make dco-test). Exits 0 when every commit is
# signed off, 1 when at least one is not, 2 on usage errors.

set -euo pipefail

if [[ $# -lt 1 || $# -gt 2 ]]; then
  echo "usage: $0 <base> [<head>]" >&2
  exit 2
fi

base=$1
head=${2:-HEAD}

for ref in "$base" "$head"; do
  if ! git rev-parse --verify --quiet "${ref}^{commit}" >/dev/null; then
    echo "check-dco: unknown commit '$ref' (fetch with fetch-depth: 0?)" >&2
    exit 2
  fi
done

lower() { tr '[:upper:]' '[:lower:]'; }

checked=0
missing=()

while IFS= read -r sha; do
  [[ -n "$sha" ]] || continue
  checked=$((checked + 1))
  author_email=$(git log -1 --format='%ae' "$sha" | lower)
  signed=0
  # Every Signed-off-by line in the message, in any paragraph (same rule as
  # the DCO GitHub App). Keep only the address between the last <...>.
  while IFS= read -r line; do
    email=${line##*<}
    email=${email%%>*}
    if [[ "$line" == *"<"*">"* && "$(printf '%s' "$email" | lower)" == "$author_email" ]]; then
      signed=1
      break
    fi
  done < <(git log -1 --format='%B' "$sha" |
    grep -iE '^[[:space:]]*Signed-off-by:' |
    sed -E 's/^[[:space:]]*[^:]*:[[:space:]]*//')
  if [[ $signed -eq 0 ]]; then
    missing+=("$(git log -1 --format='%h %s (author: %an <%ae>)' "$sha")")
  fi
done < <(git rev-list --no-merges "$base..$head")

if [[ ${#missing[@]} -eq 0 ]]; then
  echo "check-dco: all $checked commit(s) in $base..$head are signed off"
  exit 0
fi

echo "check-dco: ${#missing[@]} of $checked commit(s) lack a Signed-off-by matching the author email:"
printf '  %s\n' "${missing[@]}"
cat <<'EOF'

Every commit must be signed off under the Developer Certificate of Origin
(see the DCO file). Sign off new commits with `git commit -s`. To add the
sign-off to the commits already on this branch:

  git rebase --signoff <base-branch>     # e.g. git rebase --signoff origin/main
  git push --force-with-lease

The sign-off email must equal the commit author email (git config user.email).
EOF
exit 1
