---
name: go-toolchain-sync
description: Use when editing go.mod, Makefile, bumping the Go toolchain version, addressing govulncheck stdlib vulnerabilities, or when `make verify` fails with `go: go.mod requires go >= X.Y.Z (running go X.Y.Z; GOTOOLCHAIN=goX.Y.Z)`. Keeps the two Go version pins (`go.mod` and `Makefile`) in lockstep.
---

# go-toolchain-sync

itervox pins the Go toolchain in **two** places. They MUST match byte-for-byte.

| File | Line | Current |
|---|---|---|
| `go.mod` | 3 | `go 1.25.9` |
| `Makefile` | 6 | `export GOTOOLCHAIN := go1.25.9` |

CI is the single-source-of-truth pattern: `.github/workflows/ci-go.yml` and `release.yml` use `actions/setup-go@v6` with `go-version-file: go.mod`. **Never** hardcode `go-version:` in workflow YAML.

## Symptom: bump pin mismatch

```
gofmt -l -w .
go vet ./...
go: go.mod requires go >= 1.25.9 (running go 1.25.8; GOTOOLCHAIN=go1.25.8)
make: *** [vet] Error 1
```

The Makefile's `export GOTOOLCHAIN` overrides the contributor's shell `unset GOTOOLCHAIN` because `make` rebuilds the env on every invocation. The fix is always: align the two pins.

## Bump checklist (one commit)

- [ ] Edit `go.mod` line 3: `go 1.X.Y` -> new version
- [ ] Edit `Makefile` line 6: `export GOTOOLCHAIN := go1.X.Y` -> matching version (update the comment too if present)
- [ ] `go build ./...` (toolchain auto-downloads; watch for `go: downloading go1.X.Y`)
- [ ] `make verify` (confirms both pins align and full suite passes)
- [ ] `govulncheck -tags dev ./...` (confirms the bump closes the intended CVEs)
- [ ] Grep `.github/workflows/*.yml` for `go-version:` literals — must not exist; only `go-version-file: go.mod`

## Contributor reports `go: requires go >= X.Y.Z`

1. Ask them to run `go env GOTOOLCHAIN`.
2. If not `auto`, run `go env -w GOTOOLCHAIN=auto`.
3. Do **not** tell them to install a new Go binary. The auto mechanism downloads under `$GOPATH/sdk/`.

## govulncheck stdlib findings

Stdlib vulns (`crypto/x509`, `crypto/tls`, `archive/tar`, `net/http`, ...) are fixed by bumping to the patch release listed in `Fixed in: std@goA.B.C`. Bumping `go.mod` + `Makefile` in lockstep is the entire fix — no code changes.

## Do NOT

- Do not remove the `Makefile` `GOTOOLCHAIN` export. It is the defense against "works on my machine" drift from contributor `/usr/local/go` installs.
- Do not bump only one pin.
- Do not add a third pin in CI workflows.
