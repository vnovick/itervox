#!/bin/sh
# Trust check of the itervox GitHub Action (#252). Decides whether the event
# that started the workflow may run an agent with the repository's secrets:
#
#   issues / pull_request(_target) "labeled"  the user who added the label has
#                                             write access (admin, maintain or
#                                             write) to the repository
#   issue_comment                             the comment's author_association
#                                             is OWNER, MEMBER or COLLABORATOR
#   workflow_dispatch, schedule               trusted (starting them already
#                                             needs write access)
#   anything else                             refused
#
# A pull request from a fork is always refused, whatever the event: its head
# is code the repository's maintainers have not accepted. Every lookup that
# fails refuses (fail closed).
#
# Inputs (the runner sets the first three): GITHUB_EVENT_NAME,
# GITHUB_EVENT_PATH, GITHUB_REPOSITORY; GH_TOKEN for the permission lookups
# (`gh api`). Prints "trusted: <reason>" and exits 0, or
# "untrusted: <reason>" and exits 1; writes trusted=true|false to
# $GITHUB_OUTPUT when it is set. Needs sh, jq and gh.
set -eu

verdict() { # verdict <trusted:true|false> <reason>
	if [ "$1" = true ]; then
		printf 'trusted: %s\n' "$2"
	else
		printf 'untrusted: %s\n' "$2"
	fi
	if [ -n "${GITHUB_OUTPUT:-}" ]; then
		printf 'trusted=%s\n' "$1" >>"$GITHUB_OUTPUT"
	fi
	if [ "$1" = true ]; then
		exit 0
	fi
	exit 1
}

event=${GITHUB_EVENT_NAME:-}
payload=${GITHUB_EVENT_PATH:-}
repo=${GITHUB_REPOSITORY:-}
[ -n "$event" ] || verdict false "GITHUB_EVENT_NAME is not set"
[ -n "$repo" ] || verdict false "GITHUB_REPOSITORY is not set"

field() { # field <jq filter>: a string field of the event payload ("" when absent)
	jq -r "$1 // empty" "$payload" 2>/dev/null || true
}

# has_write <login>: the user may push to the repository.
has_write() {
	perm=$(gh api "repos/$repo/collaborators/$1/permission" --jq .permission 2>/dev/null) || return 1
	case $perm in
	admin | maintain | write) return 0 ;;
	*) return 1 ;;
	esac
}

# refuse_fork_pr <pull request number>: refuses when its head is a fork.
refuse_fork_pr() {
	head=$(gh api "repos/$repo/pulls/$1" --jq .head.repo.full_name 2>/dev/null) ||
		verdict false "cannot read pull request #$1"
	[ "$head" = "$repo" ] || verdict false "pull request #$1 comes from a fork ($head)"
}

case $event in
workflow_dispatch | schedule)
	verdict true "$event"
	;;
esac

if [ -z "$payload" ] || [ ! -r "$payload" ]; then
	verdict false "no readable event payload"
fi

case $event in
pull_request | pull_request_target)
	head=$(field .pull_request.head.repo.full_name)
	[ "$head" = "$repo" ] || verdict false "pull request from a fork (${head:-unknown head})"
	;;
esac

case $event in
issues | pull_request | pull_request_target)
	action=$(field .action)
	[ "$action" = labeled ] || verdict false "$event.$action is not a label being added"
	sender=$(field .sender.login)
	[ -n "$sender" ] || verdict false "the event names no sender"
	has_write "$sender" || verdict false "$sender, who added the label, has no write access to $repo"
	verdict true "label added by $sender (write access)"
	;;
issue_comment)
	pr=$(field '.issue.pull_request.url')
	if [ -n "$pr" ]; then
		refuse_fork_pr "$(field .issue.number)"
	fi
	assoc=$(field .comment.author_association)
	login=$(field .comment.user.login)
	case $assoc in
	OWNER | MEMBER | COLLABORATOR)
		verdict true "comment by $login ($assoc)"
		;;
	esac
	verdict false "comment by ${login:-unknown} (${assoc:-no association}) is not from an owner, member or collaborator"
	;;
esac

verdict false "event $event does not start itervox"
