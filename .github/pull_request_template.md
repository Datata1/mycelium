## What

<!-- One or two sentences: what does this PR change and why? -->

## Checklist

- [ ] `task check` passes (vet + `go test -tags sqlite_fts5 -race ./...`)
- [ ] `golangci-lint run --build-tags sqlite_fts5 ./...` is clean
- [ ] New `internal/query` methods have an integration test case
- [ ] User-visible changes have a CHANGELOG.md entry
- [ ] Golden-file diffs (if any) were regenerated deliberately and reviewed
- [ ] Architectural invariants hold (see CONTRIBUTING.md — sole writer/reader,
      additive migrations, no LLM calls at index time)
