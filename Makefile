.PHONY: all build verify chaos release-check release-hooks-clean govulncheck-check goreleaser-check dev test lint lint-go fmt vet web-deps web-typecheck web-lint web-format web-build web-test web-coverage web-spelling coverage clean benchmark tui-golden size-budget no-os-exit no-bare-go deadcode e2e

# Pin to the toolchain declared in go.mod so `go tool cover` and other tools
# always use go1.26.9, even on machines where /usr/local/go is an older version.
# Must stay in sync with the `go` directive in go.mod.
export GOTOOLCHAIN := go1.26.9
GO_PACKAGES := ./cmd/... ./internal/...
# Explicit go test timeout: internal/agent alone takes ~8-9 min under -race
# (SSH process-group tests), too close to go test's 10-minute default.
GO_TEST_TIMEOUT ?= 20m

all: build verify

# IMPORTANT: Always use `make build` instead of bare `go build ./cmd/itervox`.
# The Go binary embeds web/dist via //go:embed. If web/dist is missing, the binary
# compiles but panics at runtime with "embed: failed to sub web/dist".
# `make build` runs web-build first to ensure the frontend assets exist.
# `go build -o itervox ./cmd/itervox` produces the repo-root binary that
# Lane-3 e2e specs (`web/e2e/helpers/daemon.ts`) spawn — without -o the
# binary is discarded. The package build afterwards compiles every repo-owned
# Go package as a fail-fast sanity check without traversing web/node_modules.
build: web-deps web-build
	go build -o itervox ./cmd/itervox
	go build $(GO_PACKAGES)

# verify mirrors the gates CI runs (Web CI + Go CI). web-deps installs once,
# then each web target consumes the installed node_modules. The leaf web
# targets (web-typecheck, web-lint, web-format, web-test, web-coverage, web-build) do NOT
# install on their own so lefthook can run them in parallel without racing
# pnpm installs.
# web-build MUST run before vet/lint-go/test: internal/server/embed.go embeds
# internal/server/web/dist (gitignored), so on a fresh checkout every Go
# compile step fails with "pattern web/dist: no matching files found" until
# the frontend is built. Order is load-bearing — verify runs serially.
verify: web-deps web-build fmt vet lint-go test evals-fast web-typecheck web-lint web-format web-coverage web-spelling size-budget no-os-exit no-bare-go deadcode verify-track-b-docs deploy-test

# evals-fast runs the deterministic recorded-mode evals suite. Sub-second
# wall-clock; no API spend. Wired into `verify` so a prompt-change that
# breaks recorded scenarios fails the build (P1.a).
evals-fast:
	go test -race -count=1 -run '^TestMergeBotEvalsRecordedMode_AllScenariosPass$$|^TestReviewerEvalsRecordedMode_AllScenariosPass$$|^TestJudge|^TestReport' ./internal/evals/...

# Release preflight mirrors tag-time checks. Keep `verify` as the normal PR/local
# edit gate; this target additionally requires release tooling and a clean tree.
release-check: verify govulncheck-check goreleaser-check release-hooks-clean

govulncheck-check:
	govulncheck -tags dev $(GO_PACKAGES)

goreleaser-check:
	goreleaser check

release-hooks-clean:
	go mod tidy
	cd web && pnpm install --frozen-lockfile
	cd web && pnpm build
	git diff --exit-code
	git diff --cached --exit-code

# Guard against new os.Exit() outside cmd/itervox/exit.go — see CLAUDE.md.
no-os-exit:
	@bash scripts/check-no-os-exit.sh

# no-bare-go (CORE-008) fails on any production `go` statement that neither
# defers orchestrator.RecoverGoroutine / failFastOnPanic nor appears on the
# reasoned allowlist inside the script (e.g. orch.Run, which is never wrapped).
no-bare-go:
	@bash scripts/check-no-bare-go.sh

# deadcode (CORE-110/112) fails on any production-unreachable Go function
# (golang.org/x/tools/cmd/deadcode, pinned in the script) that is not on the
# reasoned scripts/deadcode-allowlist.txt, and on a stale allowlist entry.
# DEADCODE=/path/to/deadcode uses a local binary instead of `go run`.
deadcode:
	@bash scripts/check-deadcode.sh

# size-budget enforces hard caps on a small set of files we don't want growing
# unchecked. Caps reflect the 2026-04-28 working-tree LOC + a small headroom;
# tighten after each successful extraction (see todo_list_270426 T-20). Adding
# a new file to the budget should be a deliberate decision — the list lives
# inline so a `git blame` makes the cap's history obvious.
size-budget:
	@for pair in \
	  "cmd/itervox/main.go 2000" \
	  "cmd/itervox/adapter_settings.go 400" \
	  "cmd/itervox/init.go 600" \
	  "internal/statusui/model.go 3010" \
	  "internal/statusui/keys.go 200" \
	  "internal/server/handlers.go 1500" \
	  "web/src/components/itervox/IssueDetailSlide.tsx 460" \
	  "web/src/components/itervox/BoardColumn.tsx 360" \
	  "web/src/components/itervox/RunningSessionsTable.tsx 410" \
	  "web/src/pages/Settings/automations/AutomationEditorFields.tsx 280" \
	  "web/src/pages/Settings/automations/AutomationFilterFields.tsx 200" \
	  "web/src/pages/Settings/automations/AutomationInstructionsPanel.tsx 100" \
	  "web/src/pages/Settings/automations/automationEditorConstants.ts 160" \
	  "web/src/pages/Dashboard/index.tsx 405"; do \
	    set -- $$pair; n=$$(wc -l < $$1); \
	    if [ $$n -gt $$2 ]; then \
	      echo "size-budget: $$1 has $$n lines, cap is $$2"; exit 1; \
	    fi; \
	done
	@echo "size-budget: all files within cap"

fmt:
	gofmt -l -w cmd internal

vet:
	go vet $(GO_PACKAGES)

lint-go:
	golangci-lint run ./cmd/... ./internal/...

lint: lint-go

# -tags sshmatrix runs the slow SSH login-shell matrix in internal/agent
# (CORE-172), which a plain `go test ./...` skips to stay under go test's
# default 10-minute package timeout. CI passes the same tag.
GO_TEST_TAGS ?= sshmatrix
test:
	go test -race -timeout $(GO_TEST_TIMEOUT) -tags $(GO_TEST_TAGS) $(GO_PACKAGES) -count=1

# chaos runs the orchestrator and tracker tests over and over while busy
# loops saturate every CPU (#126); the ordering races this catches only show
# up under load. Settings: CHAOS_PACKAGES, CHAOS_RUN, CHAOS_COUNT, CHAOS_CPU,
# CHAOS_HOGS, CHAOS_TIMEOUT, CHAOS_OUT (see scripts/chaos.sh). Not part of
# verify: it takes tens of minutes. CI runs it nightly (.github/workflows/chaos.yml).
chaos:
	@bash scripts/chaos.sh

# Run tests with coverage and generate an HTML report (coverage.html).
coverage:
	go test -timeout $(GO_TEST_TIMEOUT) -coverprofile=coverage.out $(GO_PACKAGES)
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"
	@go tool cover -func=coverage.out | tail -1

# Remove build artifacts and generated coverage files.
clean:
	rm -f itervox coverage.out coverage.html
	go clean $(GO_PACKAGES)

# Regenerate catwalk golden files after intentional TUI render changes.
tui-golden:
	go test ./internal/statusui/... -args -rewrite

# Run benchmarks with memory allocation stats.
benchmark:
	go test -bench=. -benchmem $(GO_PACKAGES)

# Web dependency install — idempotent and fast when the lockfile matches.
# Standalone leaf web targets (web-lint, web-typecheck, web-format) skip
# this on purpose so lefthook's parallel pre-commit doesn't race on pnpm
# installs; aggregate targets (verify, build) depend on web-deps so a
# clean checkout still works without a manual `pnpm install`.
web-deps:
	cd web && pnpm install --frozen-lockfile

web-typecheck:
	cd web && pnpm exec tsc --noEmit -p tsconfig.app.json

web-lint:
	cd web && pnpm lint

web-format:
	cd web && pnpm format:check

web-build:
	cd web && pnpm build

web-test:
	cd web && pnpm test

web-coverage:
	cd web && pnpm test:coverage

# Guard against old "Symphony" name in user-visible strings (skip internal identifiers).
web-spelling:
	@if grep -rni '".*Symphony' web/src/ --include="*.ts" --include="*.tsx" 2>/dev/null | grep -q .; then \
		echo "ERROR: 'Symphony' found in user-visible strings — should be 'Itervox'."; \
		grep -rni '".*Symphony' web/src/ --include="*.ts" --include="*.tsx"; \
		exit 1; \
	fi

# Guard against documentation drift on the Track B file-backed-profile
# surfaces (schema 2, SOUL.md / INSTRUCTIONS.md, HEARTBEAT.md, init --update).
# Each user-facing doc must reference every surface so an operator reading
# any single doc gets the full picture.
.PHONY: verify-track-b-docs
TRACK_B_DOCS := CHANGELOG.md README.md CLAUDE.md AGENTS.md docs/architecture.md
TRACK_B_TERMS := "itervox_schema_version" "SOUL.md" "INSTRUCTIONS.md" "HEARTBEAT.md" "init --update"
verify-track-b-docs:
	@fail=0; \
	for doc in $(TRACK_B_DOCS); do \
		for term in $(TRACK_B_TERMS); do \
			if ! grep -q -F "$$term" "$$doc"; then \
				echo "ERROR: $$doc is missing required Track B reference: $$term"; \
				fail=1; \
			fi; \
		done; \
	done; \
	if [ $$fail -ne 0 ]; then \
		echo "Fix by adding the missing term to the listed doc. Every Track B surface must appear in every TRACK_B_DOCS file."; \
		exit 1; \
	fi

dev:
	cd web && pnpm dev

# Shell tests for the deploy/ scripts (CORE-061 fetch-secrets, CORE-062
# bootstrap data disk + HOME relocation, CORE-107 upgrade rollback + cleanup
# retention, CORE-113 heartbeat exporter, watchdog and alert-rule family
# names). Cloud CLIs, curl, blkid/mount/mkfs and systemctl are stubbed on
# PATH; needs only bash, jq and python3 (the systemd EnvironmentFile parser port).
.PHONY: deploy-test
deploy-test:
	bash deploy/lib/fetch-secrets_test.sh
	bash deploy/bootstrap_test.sh
	bash deploy/aws/provision_test.sh
	bash deploy/docker/healthcheck_test.sh
	bash deploy/upgrade_test.sh
	bash deploy/cleanup_test.sh
	bash deploy/monitoring/heartbeat-metrics_test.sh
	bash deploy/monitoring/itervox-watchdog_test.sh
	bash deploy/monitoring/rules_names_test.sh

# Deploy lint (CORE-105). Same targets as .github/workflows/deploy-lint.yml;
# each tool runs from a pinned image, so only docker is needed.
SHELLCHECK_IMAGE ?= koalaman/shellcheck:v0.10.0
HADOLINT_IMAGE   ?= hadolint/hadolint:v2.12.0
ACTIONLINT_IMAGE ?= rhysd/actionlint:1.7.7
PROMETHEUS_IMAGE ?= prom/prometheus:v3.1.0
SMOKE_IMAGE      ?= itervox:deploy-smoke

.PHONY: deploy-lint deploy-shellcheck deploy-hadolint deploy-actionlint deploy-promtool deploy-tofu deploy-smoke
deploy-lint: deploy-shellcheck deploy-hadolint deploy-actionlint deploy-promtool deploy-tofu

deploy-shellcheck:
	@files=$$(find deploy -name '*.sh' -type f | sort); printf 'shellcheck %s\n' $$files; \
	docker run --rm -v "$(CURDIR):/mnt:ro" -w /mnt $(SHELLCHECK_IMAGE) -x $$files

deploy-hadolint:
	@echo "hadolint deploy/docker/Dockerfile"
	docker run --rm -i $(HADOLINT_IMAGE) < deploy/docker/Dockerfile

deploy-actionlint:
	docker run --rm -v "$(CURDIR):/repo:ro" -w /repo $(ACTIONLINT_IMAGE) -color=false .github/workflows/deploy-lint.yml

deploy-promtool:
	docker run --rm -v "$(CURDIR)/deploy/monitoring:/w:ro" -w /w --entrypoint promtool $(PROMETHEUS_IMAGE) check rules prometheus-rules.yml
	docker run --rm -v "$(CURDIR)/deploy/monitoring:/w:ro" -w /w --entrypoint promtool $(PROMETHEUS_IMAGE) test rules prometheus-rules.test.yml

deploy-tofu:
	bash deploy/terraform/validate.sh

# Builds the image, requires /api/v1/health and /api/v1/ready to answer 200,
# then removes the image again (the container is removed by the script).
deploy-smoke:
	docker build -f deploy/docker/Dockerfile -t $(SMOKE_IMAGE) .
	bash deploy/docker/smoke_test.sh $(SMOKE_IMAGE); rc=$$?; docker rmi $(SMOKE_IMAGE) >/dev/null; exit $$rc

# Run end-to-end Playwright flows. Builds the binary first since e2e specs
# spawn `./itervox` and rely on the embedded web/dist. Not part of `make
# verify` because Playwright pulls a chromium binary the contributor must
# install once with `pnpm exec playwright install chromium`. T-31 / F-NEW-E.
.PHONY: e2e
e2e: build
	cd web && pnpm test:e2e

# Run the route-mocked Lane-2 browser specs (T-61..T-71). These do NOT need a
# real itervox daemon — Vite's dev server is started by Playwright's webServer
# config and every /api/v1/* call is intercepted by `e2e/fixtures/mockApi.ts`.
# Like `make e2e`, not part of `make verify` because the chromium binary is a
# one-time install (`pnpm exec playwright install chromium`).
.PHONY: qa-current-ui
qa-current-ui:
	cd web && pnpm test:ui-current

# Alias for the real-daemon Lane-3 specs. Same prerequisites as `make e2e`.
.PHONY: qa-daemon
qa-daemon: e2e

# Full existing-functionality regression baseline (T-75). Combines:
#   1. `make verify`         — Go race tests + lint + size-budget + vitest
#   2. `make qa-current-ui`  — route-mocked browser smoke (Lane 2)
#   3. `make qa-daemon`      — real-daemon e2e (Lane 3)
#
# NOT part of `make verify` because Playwright requires a one-time
# `pnpm exec playwright install chromium` per contributor; `make verify` must
# stay zero-extra-deps for new contributors.
.PHONY: qa-current
qa-current: verify qa-current-ui qa-daemon
	@echo "qa-current: full existing-functionality baseline passed"
