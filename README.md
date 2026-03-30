# skrunch

`skrunch` scans GitHub Actions workflow refs and reports refs that cannot be
resolved in the referenced action repository, are not reachable from any branch
or tag in that repository, need to be retried later, or failed validation.

It can scan:

- a single remote repository
- every repository in a GitHub organization
- a local path containing workflow files or composite action manifests

## What It Does

- Validates `uses:` refs against the GitHub API.
- Verifies full commit SHAs are reachable from a branch or tag in the action
  repository, so fork-only SHAs are reported as invalid.
- Scans remote repositories by reading `.github/workflows`.
- Scans local paths for workflow files and composite action files.
- Supports GitHub.com and GitHub Enterprise Server.
- Splits org scan output into `clean repositories` and `repositories with issues`.

## Install

Build from source:

```bash
make build
```

Install with Go:

```bash
go install github.com/sozercan/skrunch/cmd/skrunch@latest
```

## Authentication

`skrunch` resolves auth the same way `gh` does:

- it uses `--host` if provided
- otherwise it uses the default host from GitHub CLI config
- it reads tokens from the standard GitHub CLI env/config sources

Unauthenticated scans work, but GitHub API rate limits are much lower and some
repositories may be inaccessible.

Examples:

```bash
gh auth login
skrunch repo actions/checkout
```

For GitHub Enterprise Server:

```bash
gh auth login --hostname github.example.com
skrunch --host github.example.com org my-org
```

## Usage

```bash
skrunch [command]
```

Commands:

- `skrunch repo OWNER/REPO|URL`
- `skrunch org ORG`
- `skrunch path PATH`

Examples:

```bash
skrunch repo owner/repo
skrunch repo https://github.com/owner/repo
skrunch org my-org
skrunch org my-org --match sdk --limit 25
skrunch path .
skrunch path .github/workflows
skrunch repo owner/repo --show-ok
```

## Important Flags

- `--host`: GitHub host to scan against.
- `--concurrency`: max concurrent repository or file scans.
- `--match`: org mode substring filter on repository name or full name.
- `--limit`: max number of repositories to scan in org mode.
- `--include-forks`: include fork repositories in org mode.
- `--include-archived`: include archived repositories in org mode.
- `--show-ok`: include successfully resolved refs in the detailed output.
- `--debug`: print scan and validation debug logs to stderr.

Run `skrunch --help` or `skrunch <command> --help` for the full CLI help.

## Output

Every scan starts with a summary like this:

```text
ORG my-org
repos=3 files=7 action_refs=11 ok=9 invalid=1 retry=1 errors=0
```

Org scans then render two summary tables:

- `clean repositories`
- `repositories with issues`

Repositories with issues also get detailed per-repo findings. A typical org
report looks like this:

```text
ORG my-org
repos=2 files=3 action_refs=4 ok=3 invalid=1 retry=0 errors=0

clean repositories
+----------------+-------+-------------+----+
| REPOSITORY     | FILES | ACTION REFS | OK |
+----------------+-------+-------------+----+
| my-org/repo-ok | 1     | 1           | 1  |
+----------------+-------+-------------+----+

repositories with issues
+-------------------+-------+-------------+-----------+---------+-------+--------+
| REPOSITORY        | FILES | ACTION REFS | NEEDS PIN | INVALID | RETRY | ERRORS |
+-------------------+-------+-------------+-----------+---------+-------+--------+
| my-org/repo-bad   | 2     | 3           | 0         | 1       | 0     | 0      |
+-------------------+-------+-------------+-----------+---------+-------+--------+

column guide: action refs=checked uses: refs, needs pin=not pinned to a tag or full commit SHA, invalid=bad or unreachable ref, retry=temporary GitHub/API problem, errors=scan or non-retry validation problem

my-org/repo-bad
files=2 action_refs=3 ok=2 invalid=1 retry=0 errors=0
.github/workflows/ci.yml
+---------+------------------------+-------+----------------------------------------------------------+
| STATUS  | ACTION REF             | LINES | DETAILS                                                  |
+---------+------------------------+-------+----------------------------------------------------------+
| INVALID | actions/checkout@nope  | [18]  | ref "nope" could not be resolved in the action repository |
+---------+------------------------+-------+----------------------------------------------------------+
```

Current detailed statuses:

- `OK`: ref resolved successfully. Shown only with `--show-ok`.
- `INVALID`: ref could not be resolved, or a full SHA is not reachable from any
  branch or tag in the action repository.
- `RETRY`: temporary validation failure, usually rate limit or other retryable API failure.
- `ERROR`: non-retryable validation or scan failure.

Issue table columns:

- `action refs`: number of `uses:` refs checked in the repository.
- `needs pin`: refs that are not pinned to a tag or full commit SHA.
- `invalid`: refs that do not resolve, or full SHAs that are not reachable from a branch or tag.
- `retry`: temporary GitHub/API failures where rerunning may succeed.
- `errors`: scan failures or non-retryable validation failures.

## Exit Codes

- `0`: scan completed and no issues were reported
- `1`: issues were reported or the command failed

## Local Path Scans

`path` mode:

- scans `.github/workflows/**/*.yml|yaml` when present
- also picks up `action.yml` and `action.yaml`
- falls back to all YAML files under the provided path when no targeted files are found

This makes it useful for checking a repository checkout before pushing changes.

## Caching

`skrunch` uses in-memory caches during a single run:

- validation results are cached per `owner/repo@ref`
- branch/tag lists are cached per `owner/repo`
- concurrent requests for the same key are deduplicated

There is no persistent on-disk cache yet.

## Development

```bash
make build
make test
make check
```

## Acknowledgements

- `clank`, especially for the strict reachability model used by `skrunch`
