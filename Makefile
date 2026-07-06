.PHONY: all build verify release-check release-hooks-clean govulncheck-check goreleaser-check dev test lint lint-go fmt vet web-deps web-typecheck web-lint web-format web-build web-test web-coverage web-spelling coverage clean benchmark tui-golden size-budget no-os-exit e2e

# Pin to the toolchain declared in go.mod so `go tool cover` and other tools
# always use go1.25.11, even on machines where /usr/local/go is an older version.
# Must stay in sync with the `go` directive in go.mod.
export GOTOOLCHAIN := go1.25.11
GO_PACKAGES := ./cmd/... ./internal/...

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
verify: web-deps fmt vet lint-go test evals-fast web-typecheck web-lint web-format web-coverage web-build web-spelling size-budget no-os-exit verify-track-b-docs

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

test:
	go test -race $(GO_PACKAGES) -count=1

# Run tests with coverage and generate an HTML report (coverage.html).
coverage:
	go test -coverprofile=coverage.out $(GO_PACKAGES)
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
