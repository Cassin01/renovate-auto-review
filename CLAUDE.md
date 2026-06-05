# CLAUDE.md

## Project Overview

A Go 1.26 CLI that enumerates open Renovate PRs in a target GitHub repository
and invokes `claude --permission-mode auto` per PR so Claude decides whether
the PR is safe to merge and posts a comment. Optionally auto-fixes failing CI.

See `README.md` for usage, flags, and config schema.

## Build Commands

This project has no `Makefile`; use the Go toolchain directly. These commands
mirror `.github/workflows/ci.yml` — running them locally reproduces CI.

* `go build -o renovate-auto-review .`     : Compile the binary
* `go test -race -count=1 ./...`           : Run the test suite (race detector on, no cache)
* `gofmt -l .`                             : Verify formatting (CI fails on any output)
* `go vet ./...`                           : Static analysis
* `golangci-lint run`                      : Linter (CI pins `v2.11`)
* `go run . -dry-run <owner/name>`         : End-to-end smoke without invoking `claude`

## Project Structure & Rules

Single `main` package. Each file has a single responsibility — do not let
cross-cutting code accumulate in `main.go`.

* `main.go`     : CLI entry, flag parsing, orchestration loop over PRs
* `config.go`   : TOML config loading, XDG paths, validation
* `github.go`   : `gh` CLI wrappers (list/view PRs, status checks, logs, comments)
* `claude.go`   : `claude` CLI invocation (review mode and verify mode), stream wiring
* `stream.go`   : `claude` stream-json parsing → progress trace (redacted)
* `cifix.go`    : token-safe CI auto-fix — verify/publish phase split, scoped/stripped env, secret scan
* `redact.go`   : credential redaction for stdout + the pre-publish secret scan
* `lockfile.go` : deterministic lockfile-only auto-fix path (no `claude`)
* `*_test.go`   : Tests, colocated with the file under test
* `testdata/`   : Fixtures (golden outputs, stubbed CLI responses)
* `config.example.toml` : Template for `~/.config/renovate-auto-review/config.toml`

The CI auto-fix flow is **phase-split for token safety**: a verify phase
(`claude`, scoped env, cannot commit/push/comment) and a publish phase
(tool-driven, registry-token env stripped, tracked-files-only staging,
secret-scan abort). A private registry token is supplied via the environment
only — never a config field. See `README.md` for the security model.

## External CLI Dependencies

This tool is a thin orchestrator; the heavy lifting is delegated to two
external CLIs. Both must be on `$PATH` for the binary to function.

* `gh` v2.x — `gh auth status` must show the `repo` scope. All GitHub I/O
  goes through `github.go`.
* `claude` — invoked with `--permission-mode auto`. All invocations go
  through `claude.go`.

**Tests must never invoke the real CLIs.** Use the existing seams: stub
`exec.Command` / inject fake outputs from `testdata/`. New tests that
shell out to the network or to a real `claude` are not acceptable.

## Test-Driven Development (TDD)

**Core Philosophy**: Tests are the specification. Correctness is ensured by
writing tests that describe expected behavior *before* implementation.

### Red-Green-Refactor Cycle

1. **RED** (Failing Test): Write a test describing the expected behavior. Run it to confirm it fails.
2. **GREEN** (Minimal Code): Write just enough code to make the test pass.
3. **REFACTOR** (Improve): Clean up and optimize the code while keeping tests green.

**Critical Rule**: Never skip steps. Never write implementation code without a failing test first.

### Workflow

* **Adding a New Feature**:
  1. Create or open the corresponding `*_test.go` file.
  2. Write a test for the new behavior (must fail).
  3. Run `go test -race -count=1 ./...` to confirm the expected error.
  4. Implement minimal code in the source file.
  5. Run `go test -race -count=1 ./...` to confirm passage.
  6. Refactor if needed.

* **Fixing a Bug**:
  1. Write a test that reproduces the bug (must fail).
  2. Fix the bug in the source.
  3. Run `go test -race -count=1 ./...` to confirm the fix and ensure no regressions.

* **Refactoring**:
  1. Ensure all existing tests pass.
  2. Refactor code.
  3. Run `go test -race -count=1 ./...` to ensure all tests still pass.

## Testing Guidelines

* **Framework**: Standard `testing` package. Prefer **table-driven tests** with named subtests via `t.Run`.
* **Assertion Pattern**: `t.Errorf("FuncName: want %v, got %v", want, got)` — function name + concise expectation.
* **Scope**: Test **observable behavior** (return values, errors, files written, stdout/stderr) rather than internal state or unexported helpers.
* **Mocking Strategy**:
  * The external seams are `exec.Command` (for `gh` / `claude`) and the filesystem.
  * Set up fakes *before* exercising the unit under test; do not let real subprocesses or network calls leak into tests.
  * Use `testdata/` for fixture inputs and golden outputs.
* **Race Detector**: Mandatory — `-race` is enforced by CI.
* **Coverage Checklist**:
  * All exported functions have tests.
  * Edge cases tested (nil, empty input, invalid input, missing config).
  * Error paths exercised (not just the happy path).

## Anti-Patterns

* **DO NOT** write code before tests.
* **DO NOT** mock dependencies *after* the unit under test has already wired its `exec` / filesystem calls — set fakes up first.
* **DO NOT** test unexported implementation details; assert on the public API instead.
* **DO NOT** skip, `t.Skip`, or comment out failing tests to make CI pass — fix the root cause.
* **DO NOT** call `panic` or `log.Fatal` outside `main()`. Return errors so tests can assert on them.
* **DO NOT** invoke real `gh` or `claude` from tests.

## Git Conventions

* **Language**: Commit messages and pull request titles, descriptions, and bodies MUST be written in English.
* **Commit Messages**: Conventional-Commits-style prefixes are used in this repo (`feat:`, `feat!:`, `fix:`, `docs:`, `refactor:` …).
