#!/bin/sh
# fixture.sh: builds the referee test fixture repository in DIR. Shared by
# the Action's end-to-end job (.github/workflows/action.yml) and the CI
# template test (referee/ci/templates_test.sh), so both judge the same
# branches:
#
#   main            .referee.yml (human: .github/**, second_opinion:
#                   src/auth/**), a CI workflow, a Go package and its test
#   human           edits the CI workflow                       lanes 20
#   second-opinion  edits src/auth/auth.go                      lanes 10
#   code-only       edits src/app.go (the fix)                  lanes 0, audit 0
#   audit-skip      adds t.Skip to the test                     audit 20
#   agent           adds continue-on-error to the CI workflow   lanes 20, audit 20
#   pin-edited      edits .referee.yml                          pin 20
#
# usage: sh referee/testdata/fixture.sh DIR [EXCLUDE...]
# Each EXCLUDE is added to .git/info/exclude before the first commit. Files
# are written with printf (no sed -i), so it runs on Linux and macOS.
set -eu

dir=${1:?usage: fixture.sh DIR [EXCLUDE...]}
shift
cd "$dir"
g() { git -c user.email=ci@example.invalid -c user.name=ci -c commit.gpgsign=false "$@"; }
git init -q .
git symbolic-ref HEAD refs/heads/main
for pattern in "$@"; do echo "$pattern" >>.git/info/exclude; done

ci_yml() { # ci_yml TEST-COMMAND [JOB-LINE]
	printf 'name: ci\non: push\njobs:\n  t:\n    runs-on: ubuntu-latest\n'
	[ -z "${2:-}" ] || printf '%s\n' "$2"
	printf '    steps:\n      - run: %s\n' "$1"
}
policy() { printf 'human:\n  - ".github/**"\nsecond_opinion:\n  - "%s"\n' "$1"; }
app() { printf 'package app\n\nfunc Add(a, b int) int { return %s }\n' "$1"; }
app_test() { # app_test [FIRST-LINE]
	printf 'package app\n\nimport "testing"\n\nfunc TestAdd(t *testing.T) {\n'
	[ -z "${1:-}" ] || printf '\t%s\n' "$1"
	printf '\tif Add(1, 2) != 3 {\n\t\tt.Fatal("add")\n\t}\n}\n'
}
auth() { printf 'package auth\n\nfunc Check(token string) bool { return %s }\n' "$1"; }

mkdir -p .github/workflows src/auth
policy 'src/auth/**' >.referee.yml
ci_yml 'go test ./...' >.github/workflows/ci.yml
app 'a + b' >src/app.go
app_test >src/app_test.go
auth 'token != ""' >src/auth/auth.go
g add -A
g commit -qm base

branch() { # branch NAME FILE, with the file's new content on stdin
	g checkout -qb "$1" main
	cat >"$2"
	g commit -qam "$1"
}
ci_yml 'go test -race ./...' | branch human .github/workflows/ci.yml
auth 'token != "" && len(token) > 8' | branch second-opinion src/auth/auth.go
app 'b + a' | branch code-only src/app.go
app_test 't.Skip("flaky")' | branch audit-skip src/app_test.go
ci_yml 'go test ./...' '    continue-on-error: true' | branch agent .github/workflows/ci.yml
policy 'src/**' | branch pin-edited .referee.yml
g checkout -q main
