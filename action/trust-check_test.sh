#!/usr/bin/env bash
#
# #252 test for action/trust-check.sh. Run: make deploy-test (or
# bash action/trust-check_test.sh). Needs bash and jq.
#
# `gh` is a stub on PATH that answers the two lookups the check makes — a
# user's permission on the repository and a pull request's head repository —
# from fixture files, and fails for anything else (so a lookup failure is
# exercised too). Each case builds an event payload and checks the verdict
# and exit status: write access vs none for a label, author association for
# a comment, fork pull requests refused under every event.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/trust-check.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fails=0
pass() { printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; fails=$((fails + 1)); }

mkdir -p "$WORK/bin" "$WORK/perm" "$WORK/pulls"
cat >"$WORK/bin/gh" <<STUB
#!/bin/sh
# gh api <path> --jq <filter>
[ "\$1" = api ] || exit 2
case \$2 in
repos/acme/widgets/collaborators/*/permission)
	user=\${2#repos/acme/widgets/collaborators/}; user=\${user%/permission}
	[ -f "$WORK/perm/\$user" ] || { echo "HTTP 404" >&2; exit 1; }
	cat "$WORK/perm/\$user" ;;
repos/acme/widgets/pulls/*)
	n=\${2#repos/acme/widgets/pulls/}
	[ -f "$WORK/pulls/\$n" ] || { echo "HTTP 404" >&2; exit 1; }
	cat "$WORK/pulls/\$n" ;;
*) exit 1 ;;
esac
STUB
chmod +x "$WORK/bin/gh"
printf 'write\n' >"$WORK/perm/maintainer"
printf 'admin\n' >"$WORK/perm/owner"
printf 'read\n' >"$WORK/perm/reader"
printf 'triage\n' >"$WORK/perm/triager"
printf 'acme/widgets\n' >"$WORK/pulls/5"
printf 'mallory/widgets\n' >"$WORK/pulls/6"

# check <name> <event> <payload json> <want exit> <want output substring>
check() {
	local name=$1 event=$2 json=$3 want=$4 needle=$5 out code=0
	printf '%s' "$json" >"$WORK/event.json"
	: >"$WORK/output"
	out=$(PATH="$WORK/bin:$PATH" GITHUB_EVENT_NAME="$event" GITHUB_EVENT_PATH="$WORK/event.json" \
		GITHUB_REPOSITORY=acme/widgets GITHUB_OUTPUT="$WORK/output" sh "$SCRIPT" 2>&1) || code=$?
	if [ "$code" != "$want" ]; then
		fail "$name: exit $code, want $want ($out)"
		return
	fi
	case $out in
	*"$needle"*) ;;
	*)
		fail "$name: output '$out' lacks '$needle'"
		return
		;;
	esac
	local wantOut=trusted=true
	[ "$want" = 0 ] || wantOut=trusted=false
	if ! grep -qx "$wantOut" "$WORK/output"; then
		fail "$name: \$GITHUB_OUTPUT lacks $wantOut"
		return
	fi
	pass "$name"
}

label() { # label <sender> [<event> [<extra json>]]
	printf '{"action":"labeled","label":{"name":"itervox"},"sender":{"login":"%s"},"issue":{"number":4}%s}' "$1" "${2:-}"
}
comment() { # comment <login> <association> [<issue extra json>]
	printf '{"action":"created","comment":{"body":"/itervox go","user":{"login":"%s"},"author_association":"%s"},"issue":{"number":4%s}}' "$1" "$2" "${3:-}"
}
pr() { # pr <sender> <head repo>
	printf '{"action":"labeled","sender":{"login":"%s"},"pull_request":{"number":5,"head":{"repo":{"full_name":"%s"}}}}' "$1" "$2"
}

# Labels: write access vs none.
check "label by a user with write access" issues "$(label maintainer)" 0 "trusted: label added by maintainer"
check "label by an admin" issues "$(label owner)" 0 "trusted: label added by owner"
check "label by a read-only user" issues "$(label reader)" 1 "reader, who added the label, has no write access"
check "label by a triager" issues "$(label triager)" 1 "has no write access"
check "label by a stranger (lookup fails)" issues "$(label stranger)" 1 "has no write access"
check "issue opened, not labeled" issues '{"action":"opened","sender":{"login":"maintainer"}}' 1 "not a label being added"

# Comments: author association.
for a in OWNER MEMBER COLLABORATOR; do
	check "comment by $a" issue_comment "$(comment alice "$a")" 0 "trusted: comment by alice ($a)"
done
for a in CONTRIBUTOR FIRST_TIME_CONTRIBUTOR NONE; do
	check "comment by $a" issue_comment "$(comment bob "$a")" 1 "is not from an owner, member or collaborator"
done
check "comment with no association" issue_comment "$(comment bob "")" 1 "no association"

# Fork pull requests are refused under every event.
check "labeled same-repo PR" pull_request "$(pr maintainer acme/widgets)" 0 "trusted: label added by maintainer"
check "labeled fork PR" pull_request "$(pr maintainer mallory/widgets)" 1 "pull request from a fork (mallory/widgets)"
check "labeled fork PR (pull_request_target)" pull_request_target "$(pr owner mallory/widgets)" 1 "from a fork"
check "member comment on a same-repo PR" issue_comment \
	"$(comment alice MEMBER ',"pull_request":{"url":"https://api.github.com/repos/acme/widgets/pulls/5"}' | sed 's/"number":4/"number":5/')" 0 "trusted"
check "member comment on a fork PR" issue_comment \
	"$(comment alice MEMBER ',"pull_request":{"url":"https://api.github.com/repos/acme/widgets/pulls/6"}' | sed 's/"number":4/"number":6/')" 1 "pull request #6 comes from a fork (mallory/widgets)"
check "comment on an unreadable PR" issue_comment \
	"$(comment alice OWNER ',"pull_request":{"url":"https://api.github.com/repos/acme/widgets/pulls/9"}' | sed 's/"number":4/"number":9/')" 1 "cannot read pull request #9"

# Other events.
check "workflow_dispatch" workflow_dispatch '{}' 0 "trusted: workflow_dispatch"
check "schedule" schedule '{}' 0 "trusted: schedule"
check "push is refused" push '{}' 1 "does not start itervox"

# Missing environment fails closed.
code=0
out=$(env -u GITHUB_EVENT_NAME GITHUB_REPOSITORY=acme/widgets sh "$SCRIPT" 2>&1) || code=$?
if [ "$code" = 1 ] && [[ $out == *"GITHUB_EVENT_NAME is not set"* ]]; then
	pass "no event name"
else
	fail "no event name: exit $code ($out)"
fi
code=0
out=$(GITHUB_EVENT_NAME=issues GITHUB_EVENT_PATH="$WORK/missing.json" GITHUB_REPOSITORY=acme/widgets sh "$SCRIPT" 2>&1) || code=$?
if [ "$code" = 1 ] && [[ $out == *"no readable event payload"* ]]; then
	pass "unreadable payload"
else
	fail "unreadable payload: exit $code ($out)"
fi

if [ "$fails" -ne 0 ]; then
	printf '%d trust-check case(s) failed\n' "$fails"
	exit 1
fi
printf 'trust-check: all cases passed\n'
