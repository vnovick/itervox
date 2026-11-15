#!/bin/sh
# gh-stub.sh: a local stand-in for the GitHub CLI in the scripted pipeline-guard demo.
# pipeline-guard-demo.sh installs it as `gh` first on the daemon's PATH, so neither the
# agent nor the daemon reaches GitHub. It knows two calls:
#
#   gh pr create ...                 records a pull request for the current
#                                    branch and prints its URL
#   gh pr view <branch> --json ...   prints that URL while the PR is open
#
# Pull requests live in $DEMO_GH_STATE (one file per branch); every call is
# logged to $DEMO_GH_STATE/calls.log. Anything else exits 1, as gh does for
# a repository it cannot reach. The URLs are placeholders on example.com.

state=${DEMO_GH_STATE:?DEMO_GH_STATE is not set}
mkdir -p "$state"
printf '%s\n' "gh $*" >>"$state/calls.log"

base="https://example.com/demo/cart/pull"
key() { printf '%s' "$1" | tr '/' '_'; }

case "$1 $2" in
"pr create")
	branch=$(git symbolic-ref --short -q HEAD) || exit 1
	file="$state/pr-$(key "$branch")"
	if [ ! -f "$file" ]; then
		n=1
		for f in "$state"/pr-*; do
			[ -e "$f" ] && n=$((n + 1))
		done
		printf '%s/%s\n' "$base" "$n" >"$file"
	fi
	cat "$file"
	;;
"pr view")
	file="$state/pr-$(key "${3:-}")"
	[ -n "${3:-}" ] && [ -f "$file" ] || exit 1
	cat "$file"
	;;
*)
	exit 1
	;;
esac
