#!/bin/sh
# Pipeline check of the itervox GitHub Action ("safer CI mode"). An agent
# may change what CI checks, never how CI checks, so before the agent runs
# this refuses a job where the pipeline is within its reach:
#
#   policy     WORKFLOW.md (and, next to it, .itervox/settings.jsonc and the
#              .itervox/agents/ tree) differ from the policy ref: the run's
#              policy must come from the base branch, not from the branch
#              being worked on
#   hash       policy-sha256 is set and WORKFLOW.md's sha256 is not it
#   token      the token is a classic token with the `workflow` scope, so
#              the agent's pushes could change .github/workflows
#   secrets    deploy credentials are in the job environment (names only
#              are read, never values)
#
# Every check runs, and every refusal is printed. A failed lookup refuses
# (fail closed).
#
# Inputs: ITERVOX_WORKFLOW (path to WORKFLOW.md, default WORKFLOW.md);
# POLICY_REF (the branch or ref to compare against; empty: origin's default
# branch); POLICY_SHA256 (optional); SAFE_MODE ("false" turns each refusal
# into a ::warning:: and exits 0); GH_TOKEN (the token whose scopes are
# read with `gh api -i /`); ALLOW_ENV (variable names, separated by spaces
# or commas, that the secrets check lets through, e.g. the AWS keys a
# Bedrock-backed agent needs). Prints "pipeline: ok" and exits 0, or one
# "refused: <reason>" line per refusal and exits 5. Needs sh, git, gh, awk
# and sha256sum or shasum.
set -eu

wf=${ITERVOX_WORKFLOW:-WORKFLOW.md}
ref=${POLICY_REF:-}
want_sha=$(printf '%s' "${POLICY_SHA256:-}" | tr 'A-F' 'a-f')
safe=${SAFE_MODE:-true}

refusals=""
refuse() { # refuse <reason>: records one refusal (one line)
	refusals="${refusals}$1
"
}

sha256() { # sha256 <file>
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

# --- policy: the files that decide what the agent may do come from the ref.
if [ -z "$ref" ]; then
	ref=$(git ls-remote --symref origin HEAD 2>/dev/null | awk '$1 == "ref:" { sub("^refs/heads/", "", $2); print $2; exit }') || ref=""
fi
fetched=false
if [ -z "$ref" ]; then
	refuse "cannot tell the repository's default branch; set policy-ref"
elif [ ! -f "$wf" ]; then
	refuse "$wf is not in the checkout"
else
	depth=""
	if [ "$(git rev-parse --is-shallow-repository 2>/dev/null)" = true ]; then
		depth="--depth=1"
	fi
	# shellcheck disable=SC2086 # $depth is one flag or nothing
	if git fetch --quiet --no-tags $depth origin "$ref" 2>/dev/null; then
		fetched=true
	else
		refuse "cannot fetch $ref from origin to compare $wf with it"
	fi
fi
if [ "$fetched" = true ]; then
	base=$(mktemp)
	if ! git show "FETCH_HEAD:./$wf" >"$base" 2>/dev/null; then
		refuse "$wf is not on $ref: the run's policy must come from the base branch, not from the branch being worked on"
	elif ! cmp -s "$base" "$wf"; then
		refuse "$wf differs from the one on $ref: the run's policy must come from the base branch, not from the branch being worked on"
	fi
	rm -f "$base"
	dir=$(dirname "$wf")
	set -- "$dir/.itervox/agents" "$dir/.itervox/settings.jsonc"
	changed=$(git diff --name-only FETCH_HEAD -- "$@" 2>/dev/null; git ls-files --others --exclude-standard -- "$@" 2>/dev/null)
	if [ -n "$changed" ]; then
		refuse "agent profiles or project settings differ from $ref ($(printf '%s\n' "$changed" | sort -u | paste -sd, - | sed 's/,/, /g')): the run's policy must come from the base branch, not from the branch being worked on"
	fi
fi

# --- hash: an exact pin on WORKFLOW.md.
if [ -n "$want_sha" ] && [ -f "$wf" ]; then
	got=$(sha256 "$wf")
	[ "$got" = "$want_sha" ] || refuse "sha256 of $wf is $got, policy-sha256 is $want_sha"
fi

# --- token: a classic token with the workflow scope can push CI changes.
if [ -n "${GH_TOKEN:-}" ]; then
	if headers=$(gh api -i / 2>/dev/null); then
		scopes=$(printf '%s\n' "$headers" | tr -d '\r' | awk 'tolower($0) ~ /^x-oauth-scopes:/ { sub(/^[^:]*:[ \t]*/, ""); print; exit }')
		if printf '%s\n' "$scopes" | tr ',' '\n' | tr -d ' \t' | grep -qx workflow; then
			refuse "the token has the workflow scope, so the agent's pushes could change .github/workflows; give the action a token without it (GITHUB_TOKEN can't push changes to .github/workflows)"
		elif printf '%s\n' "$headers" | tr -d '\r' | grep -qi '^x-oauth-scopes:'; then
			echo "pipeline: the token is a classic token without the workflow scope"
		else
			echo "pipeline: the token has no OAuth scopes (GITHUB_TOKEN or an app token; GITHUB_TOKEN can't push changes to .github/workflows; a fine-grained token's Workflows permission can't be read here, so leave it off)"
		fi
	else
		refuse "cannot read the token's scopes (gh api -i /)"
	fi
fi

# --- secrets: deploy credentials in the job environment, by name only.
allow=" $(printf '%s' "${ALLOW_ENV:-}" | tr ',' ' ') "
# Case patterns inside this $( ) open with '(': bash 3.2 (macOS sh) otherwise
# reads a pattern's ')' as the end of the substitution.
found=$(awk 'BEGIN { for (k in ENVIRON) print k }' | sort | while IFS= read -r name; do
	case $allow in (*" $name "*) continue ;; esac
	case $name in
	(ANTHROPIC_API_KEY | OPENAI_API_KEY | CLAUDE_CODE_OAUTH_TOKEN | GH_TOKEN | GITHUB_TOKEN) continue ;;
	(AWS_SECRET_ACCESS_KEY | AWS_SESSION_TOKEN | GOOGLE_APPLICATION_CREDENTIALS | GOOGLE_CREDENTIALS | \
		AZURE_CLIENT_SECRET | AZURE_CREDENTIALS | NPM_TOKEN | NODE_AUTH_TOKEN | PYPI_TOKEN | PYPI_API_TOKEN | \
		TWINE_PASSWORD | CARGO_REGISTRY_TOKEN | DOCKER_PASSWORD | DOCKERHUB_TOKEN | KUBECONFIG | KUBE_CONFIG | \
		CLOUDFLARE_API_TOKEN | VERCEL_TOKEN | NETLIFY_AUTH_TOKEN | FLY_API_TOKEN | HEROKU_API_KEY | \
		DEPLOY_KEY | DEPLOY_TOKEN | *_DEPLOY_KEY | *_DEPLOY_TOKEN)
		printf '%s\n' "$name"
		;;
	esac
done | paste -sd, - | sed 's/,/, /g')
if [ -n "$found" ]; then
	refuse "deploy credentials in the job environment: $found; the agent's job must not hold them, deploy from a separate job after the pull request is merged"
fi

if [ -z "$refusals" ]; then
	echo "pipeline: ok, out of the agent's reach"
	exit 0
fi
if [ "$safe" = false ]; then
	printf '%s' "$refusals" | while IFS= read -r r; do
		printf '::warning::pipeline check (safe-mode off): %s\n' "$r"
	done
	exit 0
fi
printf '%s' "$refusals" | while IFS= read -r r; do
	printf 'refused: %s\n' "$r"
done
exit 5
