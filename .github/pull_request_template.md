## What does this PR do?

<!-- One paragraph explaining the change and the motivation. Focus on WHY, not what. -->

## Type of change

- [ ] Bug fix
- [ ] New feature
- [ ] Refactoring (no behaviour change)
- [ ] Documentation
- [ ] CI / tooling

## Testing

<!-- How did you test this? What edge cases did you consider? -->

## Checklist

- [ ] `make verify` passes locally
- [ ] New behaviour is covered by tests
- [ ] Frontend coverage gate remains green (`make web-coverage` or `make verify`)
- [ ] Go/package changes use the repo-owned package scope (`./cmd/... ./internal/...`)
- [ ] Go dependency or toolchain changes ran `govulncheck -tags dev ./cmd/... ./internal/...`
- [ ] Large-file changes stay within `make size-budget`
- [ ] No new direct `os.Exit()` outside `cmd/itervox/exit.go`
- [ ] No API tokens, secrets, or credentials in the diff
- [ ] Exported Go symbols have doc comments

## Related issues

<!-- Closes #NNN -->
