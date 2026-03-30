//go:build e2e

package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	skrunchauth "github.com/sozercan/skrunch/internal/auth"
	"github.com/sozercan/skrunch/internal/githubscan"
)

func TestPathCommandE2ECleanDirectoryPrefersWorkflowFiles(t *testing.T) {
	root := t.TempDir()

	writeCLIFile(t, filepath.Join(root, ".github", "workflows", "ci.yml"), `name: CI
on:
  push:
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo "hello"
`)
	writeCLIFile(t, filepath.Join(root, "notes.yaml"), "name: [\n")

	stdout, stderr, err := executeRootCommand(t, "--host", "example.invalid", "path", root)
	if err != nil {
		t.Fatalf("executeRootCommand() error = %v", err)
	}

	if !strings.Contains(stdout, "PATH "+root+"\n") {
		t.Fatalf("stdout = %q, want path header", stdout)
	}
	if !strings.Contains(stdout, "repos=1 files=1 action_refs=0 ok=0 invalid=0 retry=0 errors=0") {
		t.Fatalf("stdout = %q, want clean summary", stdout)
	}
	if !strings.Contains(stdout, "\n"+root+"\nfiles=1 action_refs=0 ok=0 invalid=0 retry=0 errors=0\nclean\n") {
		t.Fatalf("stdout = %q, want clean repo section", stdout)
	}
	if strings.Contains(stdout, "scan errors") || strings.Contains(stdout, "notes.yaml") {
		t.Fatalf("stdout = %q, did not expect fallback YAML to be scanned", stdout)
	}

	if !strings.Contains(stderr, "using GitHub host example.invalid without authentication; API rate limits may apply\n") {
		t.Fatalf("stderr = %q, want auth banner", stderr)
	}
	if !strings.Contains(stderr, "scanned "+root+": files 1/1 complete\n") {
		t.Fatalf("stderr = %q, want final progress line", stderr)
	}
}

func TestPathCommandE2EReportsParseErrors(t *testing.T) {
	root := t.TempDir()

	writeCLIFile(t, filepath.Join(root, ".github", "workflows", "broken.yml"), "name: [\n")

	stdout, stderr, err := executeRootCommand(t, "--host", "example.invalid", "path", root)
	if err == nil {
		t.Fatal("executeRootCommand() error = nil, want exit error")
	}

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("executeRootCommand() error = %T, want *ExitError", err)
	}
	if got, want := exitErr.Code, 1; got != want {
		t.Fatalf("ExitError.Code = %d, want %d", got, want)
	}

	if !strings.Contains(stdout, "repos=1 files=1 action_refs=0 ok=0 invalid=0 retry=0 errors=1") {
		t.Fatalf("stdout = %q, want error summary", stdout)
	}
	if !strings.Contains(stdout, "scan errors") {
		t.Fatalf("stdout = %q, want scan errors section", stdout)
	}
	if !strings.Contains(stdout, ".github/workflows/broken.yml: decode YAML:") {
		t.Fatalf("stdout = %q, want parse error details", stdout)
	}

	if !strings.Contains(stderr, "using GitHub host example.invalid without authentication; API rate limits may apply\n") {
		t.Fatalf("stderr = %q, want auth banner", stderr)
	}
	if !strings.Contains(stderr, "scanned "+root+": files 1/1 complete\n") {
		t.Fatalf("stderr = %q, want final progress line", stderr)
	}
}

func TestPathCommandE2EDebugLogs(t *testing.T) {
	root := t.TempDir()

	writeCLIFile(t, filepath.Join(root, ".github", "workflows", "ci.yml"), `name: CI
on:
  pull_request:
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo "debug"
`)

	stdout, stderr, err := executeRootCommand(t, "--host", "example.invalid", "--debug", "path", root)
	if err != nil {
		t.Fatalf("executeRootCommand() error = %v", err)
	}

	if !strings.Contains(stdout, "repos=1 files=1 action_refs=0 ok=0 invalid=0 retry=0 errors=0") {
		t.Fatalf("stdout = %q, want clean summary", stdout)
	}
	if !strings.Contains(stderr, "debug: scan options: concurrency=8 include_forks=false include_archived=false match=\"\" limit=0 show_ok=false\n") {
		t.Fatalf("stderr = %q, want debug options", stderr)
	}
	if !strings.Contains(stderr, "debug: discovering local workflow files under "+root+"\n") {
		t.Fatalf("stderr = %q, want discovery debug log", stderr)
	}
	if !strings.Contains(stderr, "debug: discovered 1 local workflow/composite files under "+root+"\n") {
		t.Fatalf("stderr = %q, want discovery count log", stderr)
	}
	if !strings.Contains(stderr, "debug: .github/workflows/ci.yml: validating 0 action refs\n") {
		t.Fatalf("stderr = %q, want validation debug log", stderr)
	}
}

func TestPathCommandE2EReportsForkOnlyFullSHAAsInvalid(t *testing.T) {
	root := t.TempDir()
	sha := strings.Repeat("a", 40)

	writeCLIFile(t, filepath.Join(root, ".github", "workflows", "ci.yml"), `name: CI
on:
  push:
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@`+sha+`
`)

	restoreScanClientFactory(t, func(_ context.Context, resolved skrunchauth.Resolved) (*githubscan.Client, error) {
		if got, want := resolved.Host, "example.invalid"; got != want {
			t.Fatalf("resolved host = %q, want %q", got, want)
		}

		return githubscan.NewStubClient(resolved.Host, githubscan.ClientHooks{
			GetCommit: func(_ context.Context, owner, repo, ref string) error {
				if got, want := owner+"/"+repo, "actions/checkout"; got != want {
					t.Fatalf("ResolveCommit() repo = %q, want %q", got, want)
				}
				if got, want := ref, sha; got != want {
					t.Fatalf("ResolveCommit() ref = %q, want %q", got, want)
				}
				return nil
			},
			ListBranches: func(_ context.Context, owner, repo string) ([]string, error) {
				if got, want := owner+"/"+repo, "actions/checkout"; got != want {
					t.Fatalf("ListBranches() repo = %q, want %q", got, want)
				}
				return []string{"main"}, nil
			},
			ListTags: func(_ context.Context, owner, repo string) ([]string, error) {
				if got, want := owner+"/"+repo, "actions/checkout"; got != want {
					t.Fatalf("ListTags() repo = %q, want %q", got, want)
				}
				return nil, nil
			},
			RefContains: func(_ context.Context, owner, repo, base, target string) (bool, error) {
				if got, want := owner+"/"+repo, "actions/checkout"; got != want {
					t.Fatalf("RefContains() repo = %q, want %q", got, want)
				}
				if got, want := base, "refs/heads/main"; got != want {
					t.Fatalf("RefContains() base = %q, want %q", got, want)
				}
				if got, want := target, sha; got != want {
					t.Fatalf("RefContains() target = %q, want %q", got, want)
				}
				return false, nil
			},
		}), nil
	})

	stdout, stderr, err := executeRootCommand(t, "--host", "example.invalid", "path", root)
	if err == nil {
		t.Fatal("executeRootCommand() error = nil, want exit error")
	}

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("executeRootCommand() error = %T, want *ExitError", err)
	}
	if got, want := exitErr.Code, 1; got != want {
		t.Fatalf("ExitError.Code = %d, want %d", got, want)
	}

	if !strings.Contains(stdout, "repos=1 files=1 action_refs=1 ok=0 invalid=1 retry=0 errors=0") {
		t.Fatalf("stdout = %q, want invalid summary", stdout)
	}
	if !strings.Contains(stdout, ".github/workflows/ci.yml\n") {
		t.Fatalf("stdout = %q, want workflow section", stdout)
	}
	if !strings.Contains(stdout, "actions/checkout@"+sha) {
		t.Fatalf("stdout = %q, want full SHA ref", stdout)
	}
	if !strings.Contains(stdout, "commit SHA is not reachable from any branch or tag in the action repository") {
		t.Fatalf("stdout = %q, want unreachable SHA message", stdout)
	}

	if !strings.Contains(stderr, "using GitHub host example.invalid without authentication; API rate limits may apply\n") {
		t.Fatalf("stderr = %q, want auth banner", stderr)
	}
	if !strings.Contains(stderr, "scanned "+root+": files 1/1 complete\n") {
		t.Fatalf("stderr = %q, want final progress line", stderr)
	}
}

func executeRootCommand(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_ENTERPRISE_TOKEN", "")
	t.Setenv("GITHUB_ENTERPRISE_TOKEN", "")

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	cmd := newRootCmd()
	cmd.SetArgs(args)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func writeCLIFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func restoreScanClientFactory(t *testing.T, fn func(context.Context, skrunchauth.Resolved) (*githubscan.Client, error)) {
	t.Helper()

	previous := newScanClient
	newScanClient = fn
	t.Cleanup(func() {
		newScanClient = previous
	})
}
