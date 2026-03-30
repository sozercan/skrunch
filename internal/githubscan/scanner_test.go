package githubscan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-github/v69/github"
)

func TestScannerDebugfWritesPrefixedLine(t *testing.T) {
	var buf bytes.Buffer
	scanner := &Scanner{
		opts: Options{
			DebugWriter: &buf,
		},
	}

	scanner.debugf("validating %s", "actions/checkout@deadbeef")

	if got, want := buf.String(), "debug: validating actions/checkout@deadbeef\n"; got != want {
		t.Fatalf("debugf() = %q, want %q", got, want)
	}
}

func TestNewScannerWiresClientDebugLogger(t *testing.T) {
	var buf bytes.Buffer
	client := &Client{}

	NewScanner(client, Options{DebugWriter: &buf})
	client.debugf("retrying request")

	if got, want := buf.String(), "debug: retrying request\n"; got != want {
		t.Fatalf("client.debugf() = %q, want %q", got, want)
	}
}

func TestDocumentLabel(t *testing.T) {
	if got, want := documentLabel("octo/repo", ".github/workflows/ci.yml"), "octo/repo/.github/workflows/ci.yml"; got != want {
		t.Fatalf("documentLabel() = %q, want %q", got, want)
	}
}

func TestValidateActionRefResolvableBranchIsOK(t *testing.T) {
	client := &Client{
		host: "github.com",
		getCommitFn: func(_ context.Context, owner, repo, ref string) error {
			if got, want := owner+"/"+repo, "actions/checkout"; got != want {
				t.Fatalf("ResolveCommit() repo = %q, want %q", got, want)
			}
			if got, want := ref, "main"; got != want {
				t.Fatalf("ResolveCommit() ref = %q, want %q", got, want)
			}
			return nil
		},
	}

	scanner := NewScanner(client, Options{})
	got := scanner.validateActionRef(context.Background(), "actions://actions/checkout@main")

	if got.Status != StatusOK {
		t.Fatalf("validateActionRef() status = %q, want %q", got.Status, StatusOK)
	}
	if got.Message != "ref resolves in the action repository" {
		t.Fatalf("validateActionRef() message = %q", got.Message)
	}
}

func TestValidateActionRefMissingBranchIsInvalid(t *testing.T) {
	client := &Client{
		host: "github.com",
		getCommitFn: func(_ context.Context, _, _, _ string) error {
			return &APIError{StatusCode: http.StatusNotFound, Err: errors.New("not found")}
		},
	}

	scanner := NewScanner(client, Options{})
	got := scanner.validateActionRef(context.Background(), "actions://actions/checkout@does-not-exist")

	if got.Status != StatusInvalid {
		t.Fatalf("validateActionRef() status = %q, want %q", got.Status, StatusInvalid)
	}
	if got.Message != `ref "does-not-exist" could not be resolved in the action repository` {
		t.Fatalf("validateActionRef() message = %q", got.Message)
	}
}

func TestValidateActionRefReachableFullSHAIsOK(t *testing.T) {
	sha := strings.Repeat("a", 40)
	client := &Client{
		host: "github.com",
		getCommitFn: func(_ context.Context, _, _, ref string) error {
			if got, want := ref, sha; got != want {
				t.Fatalf("ResolveCommit() ref = %q, want %q", got, want)
			}
			return nil
		},
		listBranchesFn: func(_ context.Context, _, _ string) ([]string, error) {
			return []string{"main"}, nil
		},
		listTagsFn: func(_ context.Context, _, _ string) ([]string, error) {
			return nil, nil
		},
		refContainsFn: func(_ context.Context, _, _, base, target string) (bool, error) {
			return base == "refs/heads/main" && target == sha, nil
		},
	}

	scanner := NewScanner(client, Options{})
	got := scanner.validateActionRef(context.Background(), "actions://actions/checkout@"+sha)

	if got.Status != StatusOK {
		t.Fatalf("validateActionRef() status = %q, want %q", got.Status, StatusOK)
	}
	if got.Message != "commit is reachable from a branch or tag" {
		t.Fatalf("validateActionRef() message = %q", got.Message)
	}
}

func TestValidateActionRefReachableFullSHAMatchingHeadSkipsCompare(t *testing.T) {
	sha := strings.Repeat("b", 40)
	client := &Client{
		host: "github.com",
		getCommitFn: func(_ context.Context, _, _, ref string) error {
			if got, want := ref, sha; got != want {
				t.Fatalf("ResolveCommit() ref = %q, want %q", got, want)
			}
			return nil
		},
		listBranchHeadsFn: func(_ context.Context, _, _ string) ([]reachabilityRef, error) {
			return []reachabilityRef{
				newReachabilityRef(reachabilityRefBranch, "main", sha),
				newReachabilityRef(reachabilityRefBranch, "release", strings.Repeat("c", 40)),
			}, nil
		},
		listTagHeadsFn: func(_ context.Context, _, _ string) ([]reachabilityRef, error) {
			return []reachabilityRef{
				newReachabilityRef(reachabilityRefTag, "v1", sha),
			}, nil
		},
		refContainsFn: func(_ context.Context, _, _, _, _ string) (bool, error) {
			t.Fatal("RefContains() should not be called when the target matches a cached ref head")
			return false, nil
		},
	}

	scanner := NewScanner(client, Options{})
	got := scanner.validateActionRef(context.Background(), "actions://actions/checkout@"+sha)

	if got.Status != StatusOK {
		t.Fatalf("validateActionRef() status = %q, want %q", got.Status, StatusOK)
	}
	if got.Message != "commit is reachable from a branch or tag" {
		t.Fatalf("validateActionRef() message = %q", got.Message)
	}
}

func TestValidateActionRefSAMLFallbackRateLimitIsRetry(t *testing.T) {
	sha := strings.Repeat("a", 40)
	client := &Client{
		host: "github.com",
		getCommitFn: func(_ context.Context, _, _, ref string) error {
			if got, want := ref, sha; got != want {
				t.Fatalf("ResolveCommit() ref = %q, want %q", got, want)
			}
			return &APIError{
				Operation:  "resolve ref github/codeql-action@" + sha,
				StatusCode: http.StatusForbidden,
				Err: fmt.Errorf(
					"authenticated request hit SAML enforcement; anonymous fallback failed: GET https://api.github.com/repos/github/codeql-action/commits/%s: 403 API rate limit exceeded for 20.191.123.238. [rate reset in 32m44s]",
					sha,
				),
			}
		},
	}

	scanner := NewScanner(client, Options{})
	got := scanner.validateActionRef(context.Background(), "actions://github/codeql-action@"+sha)

	if got.Status != StatusRetry {
		t.Fatalf("validateActionRef() status = %q, want %q", got.Status, StatusRetry)
	}
	if !strings.Contains(got.Message, "retry later: could not inspect github/codeql-action on github.com") {
		t.Fatalf("validateActionRef() message = %q, want retry-later message", got.Message)
	}
}

func TestValidateActionRefForbiddenWithoutRateLimitIsError(t *testing.T) {
	client := &Client{
		host: "github.com",
		getCommitFn: func(_ context.Context, _, _, _ string) error {
			return &APIError{
				Operation:  "resolve ref actions/checkout@main",
				StatusCode: http.StatusForbidden,
				Err:        errors.New("resource not accessible by integration"),
			}
		},
	}

	scanner := NewScanner(client, Options{})
	got := scanner.validateActionRef(context.Background(), "actions://actions/checkout@main")

	if got.Status != StatusError {
		t.Fatalf("validateActionRef() status = %q, want %q", got.Status, StatusError)
	}
	if !strings.Contains(got.Message, "could not inspect actions/checkout on github.com") {
		t.Fatalf("validateActionRef() message = %q, want error message", got.Message)
	}
}

func TestValidateActionRefForkOnlyFullSHAIsInvalid(t *testing.T) {
	sha := strings.Repeat("a", 40)
	client := &Client{
		host: "github.com",
		getCommitFn: func(_ context.Context, _, _, ref string) error {
			if got, want := ref, sha; got != want {
				t.Fatalf("ResolveCommit() ref = %q, want %q", got, want)
			}
			return nil
		},
		listBranchesFn: func(_ context.Context, _, _ string) ([]string, error) {
			return []string{"main"}, nil
		},
		listTagsFn: func(_ context.Context, _, _ string) ([]string, error) {
			return nil, nil
		},
		refContainsFn: func(_ context.Context, _, _, base, target string) (bool, error) {
			if got, want := base, "refs/heads/main"; got != want {
				t.Fatalf("RefContains() base = %q, want %q", got, want)
			}
			if got, want := target, sha; got != want {
				t.Fatalf("RefContains() target = %q, want %q", got, want)
			}
			return false, nil
		},
	}

	scanner := NewScanner(client, Options{})
	got := scanner.validateActionRef(context.Background(), "actions://actions/checkout@"+sha)

	if got.Status != StatusInvalid {
		t.Fatalf("validateActionRef() status = %q, want %q", got.Status, StatusInvalid)
	}
	if got.Message != "commit SHA is not reachable from any branch or tag in the action repository" {
		t.Fatalf("validateActionRef() message = %q", got.Message)
	}
}

func TestOrderReachabilityRefsPrioritizesBranchesAndDedupesHeads(t *testing.T) {
	branches := []reachabilityRef{
		newReachabilityRef(reachabilityRefBranch, "feature", strings.Repeat("3", 40)),
		newReachabilityRef(reachabilityRefBranch, "main", strings.Repeat("1", 40)),
		newReachabilityRef(reachabilityRefBranch, "master", strings.Repeat("2", 40)),
	}
	tags := []reachabilityRef{
		newReachabilityRef(reachabilityRefTag, "v1", strings.Repeat("1", 40)),
		newReachabilityRef(reachabilityRefTag, "v2", strings.Repeat("4", 40)),
	}

	got := orderReachabilityRefs(branches, tags)

	if gotLen, wantLen := len(got), 4; gotLen != wantLen {
		t.Fatalf("orderReachabilityRefs() len = %d, want %d", gotLen, wantLen)
	}
	if got[0].QualifiedName != "refs/heads/main" {
		t.Fatalf("orderReachabilityRefs()[0] = %q, want refs/heads/main", got[0].QualifiedName)
	}
	if got[1].QualifiedName != "refs/heads/master" {
		t.Fatalf("orderReachabilityRefs()[1] = %q, want refs/heads/master", got[1].QualifiedName)
	}
	if got[2].QualifiedName != "refs/heads/feature" {
		t.Fatalf("orderReachabilityRefs()[2] = %q, want refs/heads/feature", got[2].QualifiedName)
	}
	if got[3].QualifiedName != "refs/tags/v2" {
		t.Fatalf("orderReachabilityRefs()[3] = %q, want refs/tags/v2", got[3].QualifiedName)
	}
}

func TestFilterOrgRepositoriesAppliesFiltersAndLimit(t *testing.T) {
	repositories := []*github.Repository{
		{Name: github.Ptr("alpha-sdk"), FullName: github.Ptr("Azure/alpha-sdk")},
		{Name: github.Ptr("beta"), FullName: github.Ptr("Azure/beta")},
		{Name: github.Ptr("gamma-sdk"), FullName: github.Ptr("Azure/gamma-sdk"), Fork: github.Ptr(true)},
		{Name: github.Ptr("delta-sdk"), FullName: github.Ptr("Azure/delta-sdk")},
	}

	filtered, stop := filterOrgRepositories(repositories, Options{
		RepoMatch:    "sdk",
		RepoLimit:    2,
		IncludeForks: false,
	}, 0)

	if !stop {
		t.Fatal("filterOrgRepositories() stop = false, want true")
	}
	if got, want := len(filtered), 2; got != want {
		t.Fatalf("filterOrgRepositories() len = %d, want %d", got, want)
	}
	if got, want := filtered[0].GetFullName(), "Azure/alpha-sdk"; got != want {
		t.Fatalf("filterOrgRepositories()[0] = %q, want %q", got, want)
	}
	if got, want := filtered[1].GetFullName(), "Azure/delta-sdk"; got != want {
		t.Fatalf("filterOrgRepositories()[1] = %q, want %q", got, want)
	}
}

func TestMatchesOrgRepositoryUsesCaseInsensitiveNameOrFullName(t *testing.T) {
	repository := &github.Repository{
		Name:     github.Ptr("awesome-sdk"),
		FullName: github.Ptr("Azure/awesome-sdk"),
	}

	if !matchesOrgRepository(repository, "AWESOME") {
		t.Fatal("matchesOrgRepository() = false, want true for name match")
	}
	if !matchesOrgRepository(repository, "azure/awesome") {
		t.Fatal("matchesOrgRepository() = false, want true for full name match")
	}
	if matchesOrgRepository(repository, "containers") {
		t.Fatal("matchesOrgRepository() = true, want false for non-match")
	}
}
