# AGENTS.md

This file is intentionally minimal and limited to repository-specific
requirements.

Do not expand this file into a broad codebase overview or duplicate the README
unless a concrete repository need appears. Keep only commands, constraints, and
repo-specific guidance that an agent cannot reliably infer from the repository.

## Required Commands

- Build: `make build`
- Test: `go test ./...`
- Full check: `make check`
- Format: `make fmt`

## Repo Map

- CLI entrypoint: `cmd/skrunch/main.go`
- CLI wiring and flags: `internal/cli/root.go`
- GitHub scan and validation logic: `internal/githubscan/`
- Text output rendering: `internal/output/text.go`
- Workflow and composite action parsing: `internal/workflows/parser.go`

## Change Expectations

- If behavior changes, update tests in the affected package.
- If CLI flags or report/output text change, update `README.md`.
- Prefer the existing Go and Makefile-based workflow over adding new tooling.
