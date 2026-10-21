#!/usr/bin/env bash
#
# Test for scripts/check-dco.sh. Run: make dco-test. Needs only bash and git;
# every case builds commits in a throwaway repository under mktemp.
#
# Proves:
#   - a range of commits made with `git commit -s` passes;
#   - an empty range passes;
#   - a commit with no Signed-off-by fails and is named in the output;
#   - a Signed-off-by whose email differs from the author email fails;
#   - sign-off matching is case-insensitive on the email and the trailer key;
#   - one bad commit among good ones fails the whole range;
#   - merge commits are not checked;
#   - `git rebase --signoff` (the documented fix) turns a failing range green;
#   - an unknown base is a usage error (exit 2), not a pass.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK="$HERE/check-dco.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fails=0
pass() { printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; fails=$((fails + 1)); }
# contains <want 0|1> <needle> <description>: assert $out does (1) or does
# not (0) contain needle.
contains() {
  local has=0
  [[ "$out" == *"$2"* ]] && has=1
  if [[ $has -eq $1 ]]; then pass "$3"; else fail "$3: output was: $out"; fi
}

# Isolate from the caller's git config (signing, hooks, templates).
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME="Ada Contributor" GIT_AUTHOR_EMAIL="ada@example.com"
export GIT_COMMITTER_NAME="Ada Contributor" GIT_COMMITTER_EMAIL="ada@example.com"

n=0
# new_repo: fresh repo with one unsigned base commit; prints nothing, cd's in.
new_repo() {
  n=$((n + 1))
  mkdir -p "$WORK/r$n"
  cd "$WORK/r$n"
  git init -q -b main .
  git config commit.gpgsign false
  echo base > base.txt
  git add base.txt
  git commit -q -m "base"
}
# commit_file <name> [git commit args...]
commit_file() {
  local f=$1
  shift
  echo "$f $RANDOM" >> "$f"
  git add "$f"
  git commit -q "$@"
}

# expect <want-exit> <description> [check-dco args...]; output in $out.
expect() {
  local want=$1 desc=$2
  shift 2
  local got=0
  out=$(bash "$CHECK" "$@" 2>&1) || got=$?
  if [[ $got -eq $want ]]; then
    pass "$desc (exit $got)"
  else
    fail "$desc: want exit $want, got $got"
    printf '%s\n' "$out" | sed 's/^/     | /'
  fi
}

# 1. All commits signed off with git commit -s.
new_repo
commit_file a.txt -s -m "feat: one"
commit_file b.txt -s -m "fix: two"
expect 0 "signed-off commits pass" main~2 main
contains 1 "all 2 commit(s)" "reports 2 checked commits"

# 2. Empty range.
expect 0 "empty range passes" main main

# 3. No sign-off at all.
new_repo
commit_file a.txt -m "feat: unsigned"
expect 1 "unsigned commit fails" main~1 main
contains 1 "feat: unsigned" "failing commit is named"
contains 1 "git rebase --signoff" "fix hint printed"

# 4. Sign-off with a different email than the author.
new_repo
commit_file a.txt -m "feat: mismatched" -m "Signed-off-by: Ada Contributor <someone-else@example.com>"
expect 1 "sign-off email mismatch fails" main~1 main

# 4b. Sign-off by a different person while the author's own is absent.
new_repo
GIT_AUTHOR_EMAIL="bob@example.com" commit_file a.txt -s -m "feat: author bob, committer ada signs"
expect 1 "committer-only sign-off (author differs) fails" main~1 main

# 5. Case-insensitive email and trailer key.
new_repo
GIT_AUTHOR_EMAIL="Ada@Example.com" commit_file a.txt -m "feat: case" -m "signed-off-by: Ada Contributor <ADA@example.COM>"
expect 0 "case-insensitive match passes" main~1 main

# 6. One bad commit among good ones.
new_repo
commit_file a.txt -s -m "feat: good one"
commit_file b.txt -m "feat: bad one"
commit_file c.txt -s -m "feat: good two"
expect 1 "one unsigned among signed fails" main~3 main
contains 1 "1 of 3 commit(s)" "reports 1 of 3"
contains 0 "good one" "signed commits not listed"

# 7. Merge commits are skipped (the merge itself is unsigned).
new_repo
git checkout -q -b topic
commit_file t.txt -s -m "feat: topic"
git checkout -q main
commit_file m.txt -s -m "feat: main side"
git merge -q --no-ff --no-edit topic
expect 0 "unsigned merge commit is skipped" main~1~1 main

# 8. The documented fix: git rebase --signoff.
new_repo
base=$(git rev-parse HEAD)
commit_file a.txt -m "feat: unsigned 1"
commit_file b.txt -m "feat: unsigned 2"
expect 1 "before rebase --signoff fails" "$base" HEAD
git rebase -q --signoff "$base"
expect 0 "after rebase --signoff passes" "$base" HEAD

# 9. Usage errors.
expect 2 "unknown base is a usage error" deadbeefdeadbeef HEAD
expect 2 "no arguments is a usage error"

if [[ $fails -gt 0 ]]; then
  echo "check-dco_test: $fails failure(s)"
  exit 1
fi
echo "check-dco_test: all passed"
