# 01 — OSS Hygiene

**Size:** S/M · **Depends on:** nothing · **Blocks:** nothing (fully parallel)

## Goal

Make the repo legally usable, contributor-friendly, and style-enforced: license,
correct module path, contribution docs, lint gate, single source of truth for
the Go version.

## Pain

- **No LICENSE.** `README.md` § License says "TBD" — legally nobody may use,
  fork, or contribute, despite public `v5.0.0`/`v5.0.1` tags.
- **Module path is wrong.** `go.mod` declares `github.com/jdwiederstein/mycelium`
  but the actual remote is `github.com/Datata1/mycelium`. 59 Go files import the
  wrong path; `go install`-style references and godoc links would 404.
- No CONTRIBUTING.md, CODE_OF_CONDUCT.md, SECURITY.md, issue/PR templates.
- No lint config, no lint CI job — only `go vet` runs. Formatting unenforced.
- `go.mod` says `go 1.25.0` while `.github/workflows/{ci,release}.yml` pin
  Go 1.22 — local builds and CI can diverge.

## Design

### License
- `LICENSE` = Apache License 2.0 verbatim (owner decision, confirmed).
- Optional `NOTICE` file: `mycelium\nCopyright <year> <owner>`.
- Replace the README "TBD" section with the Apache-2.0 statement.

### Module path fix (one mechanical PR)
1. `go.mod`: `module github.com/datata1/mycelium`.
2. Rewrite all imports:
   `grep -rl "jdwiederstein/mycelium" --include="*.go" . | xargs sed -i '' 's|jdwiederstein/mycelium|datata1/mycelium|g'`
3. Sweep non-Go references: docs/, README, CHANGELOG, Taskfile, workflows.
4. `go build -tags sqlite_fts5 ./... && go test -tags sqlite_fts5 -race ./...`.

### Community files
- **CONTRIBUTING.md** — the load-bearing doc. Contents:
  - Build prerequisites: cgo toolchain, **always `-tags sqlite_fts5`** (why:
    FTS5 in the embedded driver; migrations fail without it), Taskfile targets
    (`task build/check/smoke/daemon`).
  - Architecture one-pager linking `docs/` and the invariants from CLAUDE.md
    restated for humans: daemon = sole SQLite writer; `internal/query` sole
    reader / `internal/pipeline` sole writer; parsers are storage-agnostic
    plain structs; migrations additive only; no pre-commit hooks.
  - How to add a language → link `docs/adding-a-language.md` (fixed in WS06).
  - Test expectations: integration case per new query method; golden-diff
    review discipline (WS07).
  - Commit style: imperative present; CHANGELOG entry per milestone.
- **CODE_OF_CONDUCT.md** — Contributor Covenant 2.1.
- **SECURITY.md** — private report contact (owner email), supported-versions
  note (latest release only).
- `.github/ISSUE_TEMPLATE/bug_report.yml` (asks for `myco doctor` output, OS,
  version), `.github/ISSUE_TEMPLATE/feature_request.yml` (link
  `docs/limitations.md` first), `.github/pull_request_template.md` (checklist:
  `task check`, CHANGELOG, integration test for query changes).

### Lint
`.golangci.yml` — deliberately modest set so it doesn't fight the codebase:

```yaml
run:
  build-tags: [sqlite_fts5]     # without this, typecheck fails on FTS5 paths
linters:
  enable:
    # correctness
    - govet
    - staticcheck
    - errcheck
    - errorlint        # catches == comparisons on errors (see WS02)
    - sqlclosecheck
    - noctx
    - copyloopvar
    # hygiene
    - unused
    - ineffassign
    - unconvert
    - unparam
    - misspell
    - nolintlint
    - thelper
    # style
    - gofmt
    - goimports
    - revive           # minimal rule set; doc.go coverage already exists
```

No `gofumpt` initially — avoids a giant reformat PR that buries real changes.

### CI / Go version
- New `lint` job in `ci.yml` via `golangci/golangci-lint-action` (runner needs
  the cgo toolchain because of the build tag).
- All workflows switch `go-version: "1.22"` → `go-version-file: go.mod` so the
  version is defined exactly once.
- Then set the `go` directive to the actual floor: grep for 1.23+/1.24/1.25
  features (`slog.DiscardHandler` from WS02 needs 1.24+); if nothing needs
  1.25, lower to broaden the contributor toolchain range.
- `.editorconfig`: tabs for `*.go`, LF, final newline, 2-space YAML/JSON.

## Migration path (green at every step)

1. LICENSE + NOTICE + community files + templates — zero code impact.
2. Module path PR (mechanical, isolated — merge before other workstreams
   branch off, otherwise every later PR conflicts on import lines).
3. `.editorconfig`.
4. Run golangci-lint locally, fix findings in one mechanical PR. Expected:
   errcheck hits like the swallowed response-write encode in
   `internal/daemon/daemon.go:309,314` — mark `_ =` with a comment until WS02
   logs them properly.
5. Add the CI lint job only once the repo is clean.
6. Go-version alignment last, followed by a release-workflow dry run — the
   5-target native-runner cgo matrix is sensitive to toolchain bumps.

## Risks

- Lint + cgo + build tags is the classic footgun: a missing
  `build-tags: [sqlite_fts5]` produces hundreds of false typecheck errors.
- Module rename conflicts with every open branch — coordinate, merge first.
- Release workflow must be dry-run (tag on a fork or `workflow_dispatch`)
  after the Go bump; all 5 targets build natively with cgo.

## Verification

- `golangci-lint run --build-tags sqlite_fts5 ./...` clean locally.
- CI green on ubuntu + macos including the new lint job.
- `go build -tags sqlite_fts5 ./...` with the go.mod-pinned toolchain.
- `grep -r jdwiederstein .` returns nothing (except CHANGELOG history, which
  stays untouched).
