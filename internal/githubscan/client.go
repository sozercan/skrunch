package githubscan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v69/github"
	skrunchauth "github.com/sozercan/skrunch/internal/auth"
)

type Client struct {
	api         *github.Client
	anonymous   *github.Client
	host        string
	tokenSource string
	debugLogf   func(string, ...any)

	listOrgRepositoriesPageFn func(context.Context, string, int) ([]*github.Repository, int, error)
	getCommitFn               func(context.Context, string, string, string) error
	listBranchHeadsFn         func(context.Context, string, string) ([]reachabilityRef, error)
	listTagHeadsFn            func(context.Context, string, string) ([]reachabilityRef, error)
	listBranchesFn            func(context.Context, string, string) ([]string, error)
	listTagsFn                func(context.Context, string, string) ([]string, error)
	refContainsFn             func(context.Context, string, string, string, string) (bool, error)
}

type WorkflowFile struct {
	Path    string
	Content []byte
}

type reachabilityRefKind string

const (
	reachabilityRefBranch reachabilityRefKind = "branch"
	reachabilityRefTag    reachabilityRefKind = "tag"
)

type reachabilityRef struct {
	Name          string
	QualifiedName string
	HeadSHA       string
	Kind          reachabilityRefKind
}

type APIError struct {
	Operation  string
	StatusCode int
	Err        error
}

const (
	apiRetryMaxAttempts       = 5
	apiRetryBaseDelay         = 500 * time.Millisecond
	apiRetryMaxBackoff        = 8 * time.Second
	apiRetryMaxDelay          = 5 * time.Second
	apiPrimaryRateLimitBuffer = time.Second
	apiClientTimeout          = 30 * time.Second
)

var sleepContextFn = sleepContext

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if e.StatusCode != 0 {
		return fmt.Sprintf("%s: status %d: %v", e.Operation, e.StatusCode, e.Err)
	}
	return fmt.Sprintf("%s: %v", e.Operation, e.Err)
}

func (e *APIError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func NewClient(_ context.Context, resolved skrunchauth.Resolved) (*Client, error) {
	ghClient, err := newGitHubClient(&http.Client{Timeout: apiClientTimeout}, resolved.Host)
	if err != nil {
		return nil, err
	}

	var anonymousClient *github.Client
	if resolved.Token != "" {
		anonymousClient = ghClient

		ghClient, err = newGitHubClient(newAuthenticatedHTTPClient(resolved.Token), resolved.Host)
		if err != nil {
			return nil, err
		}
	}

	return &Client{
		api:         ghClient,
		anonymous:   anonymousClient,
		host:        resolved.Host,
		tokenSource: resolved.Source,
	}, nil
}

func newAuthenticatedHTTPClient(token string) *http.Client {
	return &http.Client{
		Timeout: apiClientTimeout,
		Transport: &staticTokenTransport{
			token: token,
		},
	}
}

type staticTokenTransport struct {
	token string
	base  http.RoundTripper
}

func (t *staticTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req2 := req.Clone(req.Context())
	if req2.Header == nil {
		req2.Header = make(http.Header)
	}
	req2.Header.Set("Authorization", "Bearer "+t.token)
	return t.baseTransport().RoundTrip(req2)
}

func (t *staticTokenTransport) baseTransport() http.RoundTripper {
	if t != nil && t.base != nil {
		return t.base
	}
	return http.DefaultTransport
}

func newGitHubClient(base *http.Client, host string) (*github.Client, error) {
	ghClient := github.NewClient(base)
	if skrunchauth.IsEnterprise(host) {
		var err error
		ghClient, err = ghClient.WithEnterpriseURLs(
			skrunchauth.APIBaseURL(host),
			skrunchauth.UploadBaseURL(host),
		)
		if err != nil {
			return nil, fmt.Errorf("configure enterprise GitHub client for %s: %w", host, err)
		}
	}
	return ghClient, nil
}

func (c *Client) Host() string {
	if c == nil {
		return ""
	}
	return c.host
}

func (c *Client) TokenSource() string {
	if c == nil {
		return ""
	}
	return c.tokenSource
}

func (c *Client) debugf(format string, args ...any) {
	if c == nil || c.debugLogf == nil {
		return
	}
	c.debugLogf(format, args...)
}

func (c *Client) GetRepository(ctx context.Context, owner, repo string) (*github.Repository, error) {
	operation := fmt.Sprintf("get repository %s/%s", owner, repo)
	var repository *github.Repository
	resp, err := c.withAnonymousFallback(ctx, operation, func(ctx context.Context, client *github.Client) (*github.Response, error) {
		var innerResp *github.Response
		var innerErr error
		repository, innerResp, innerErr = client.Repositories.Get(ctx, owner, repo)
		return innerResp, innerErr
	})
	if err != nil {
		return nil, wrapAPIError(operation, resp, err)
	}
	return repository, nil
}

func (c *Client) ResolveCommit(ctx context.Context, owner, repo, ref string) error {
	if c != nil && c.getCommitFn != nil {
		return c.getCommitFn(ctx, owner, repo, ref)
	}

	operation := fmt.Sprintf("resolve ref %s/%s@%s", owner, repo, ref)
	resp, err := c.withAnonymousFallback(ctx, operation, func(ctx context.Context, client *github.Client) (*github.Response, error) {
		_, innerResp, innerErr := client.Repositories.GetCommit(ctx, owner, repo, ref, nil)
		return innerResp, innerErr
	})
	if err != nil {
		return wrapAPIError(operation, resp, err)
	}

	return nil
}

func (c *Client) ListOrgRepositories(ctx context.Context, org string) ([]*github.Repository, error) {
	var repositories []*github.Repository
	page := 1
	for {
		repos, nextPage, err := c.ListOrgRepositoriesPage(ctx, org, page)
		if err != nil {
			return nil, err
		}

		repositories = append(repositories, repos...)
		if nextPage == 0 {
			break
		}
		page = nextPage
	}

	return repositories, nil
}

func (c *Client) ListOrgRepositoriesPage(ctx context.Context, org string, page int) ([]*github.Repository, int, error) {
	if c != nil && c.listOrgRepositoriesPageFn != nil {
		return c.listOrgRepositoriesPageFn(ctx, org, page)
	}

	opts := &github.RepositoryListByOrgOptions{
		Type:      "all",
		Sort:      "full_name",
		Direction: "asc",
		ListOptions: github.ListOptions{
			PerPage: 100,
			Page:    page,
		},
	}

	operation := fmt.Sprintf("list repositories for org %s", org)
	var repos []*github.Repository
	resp, err := c.withAnonymousFallback(ctx, operation, func(ctx context.Context, client *github.Client) (*github.Response, error) {
		var innerResp *github.Response
		var innerErr error
		repos, innerResp, innerErr = client.Repositories.ListByOrg(ctx, org, opts)
		return innerResp, innerErr
	})
	if err != nil {
		return nil, 0, wrapAPIError(operation, resp, err)
	}

	if resp == nil {
		return repos, 0, nil
	}
	return repos, resp.NextPage, nil
}

func (c *Client) WorkflowFiles(ctx context.Context, owner, repo string) ([]WorkflowFile, error) {
	files, err := c.workflowFiles(ctx, owner, repo, ".github/workflows", true)
	if err != nil {
		return nil, err
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})

	return files, nil
}

func (c *Client) workflowFiles(ctx context.Context, owner, repo, path string, root bool) ([]WorkflowFile, error) {
	var (
		file    *github.RepositoryContent
		entries []*github.RepositoryContent
	)
	operation := fmt.Sprintf("get contents %s/%s:%s", owner, repo, path)
	resp, err := c.withAnonymousFallback(ctx, operation, func(ctx context.Context, client *github.Client) (*github.Response, error) {
		var innerResp *github.Response
		var innerErr error
		file, entries, innerResp, innerErr = client.Repositories.GetContents(
			ctx,
			owner,
			repo,
			path,
			&github.RepositoryContentGetOptions{},
		)
		return innerResp, innerErr
	})
	if err != nil {
		if root && resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		return nil, wrapAPIError(operation, resp, err)
	}

	if file != nil {
		if !hasYAMLExt(file.GetName()) {
			return nil, nil
		}

		content, err := file.GetContent()
		if err != nil {
			return nil, fmt.Errorf("decode content %s/%s:%s: %w", owner, repo, path, err)
		}

		return []WorkflowFile{{
			Path:    file.GetPath(),
			Content: []byte(content),
		}}, nil
	}

	var out []WorkflowFile
	for _, entry := range entries {
		if entry == nil {
			continue
		}

		switch entry.GetType() {
		case "dir":
			children, err := c.workflowFiles(ctx, owner, repo, entry.GetPath(), false)
			if err != nil {
				return nil, err
			}
			out = append(out, children...)
		case "file":
			if !hasYAMLExt(entry.GetName()) {
				continue
			}
			children, err := c.workflowFiles(ctx, owner, repo, entry.GetPath(), false)
			if err != nil {
				return nil, err
			}
			out = append(out, children...)
		}
	}

	return out, nil
}

func (c *Client) ListBranchHeads(ctx context.Context, owner, repo string) ([]reachabilityRef, error) {
	if c != nil && c.listBranchHeadsFn != nil {
		return slicesCloneReachabilityRefs(c.listBranchHeadsFn(ctx, owner, repo))
	}
	if c != nil && c.listBranchesFn != nil {
		branches, err := c.listBranchesFn(ctx, owner, repo)
		if err != nil {
			return nil, err
		}
		return namesToReachabilityRefs(branches, reachabilityRefBranch), nil
	}

	opts := &github.BranchListOptions{
		ListOptions: github.ListOptions{
			PerPage: 100,
		},
	}

	var branches []reachabilityRef
	for {
		operation := fmt.Sprintf("list branches for %s/%s", owner, repo)
		var page []*github.Branch
		resp, err := c.withAnonymousFallback(ctx, operation, func(ctx context.Context, client *github.Client) (*github.Response, error) {
			var innerResp *github.Response
			var innerErr error
			page, innerResp, innerErr = client.Repositories.ListBranches(ctx, owner, repo, opts)
			return innerResp, innerErr
		})
		if err != nil {
			return nil, wrapAPIError(operation, resp, err)
		}

		for _, branch := range page {
			if branch == nil {
				continue
			}
			name := branch.GetName()
			if name == "" {
				continue
			}
			branches = append(branches, newReachabilityRef(reachabilityRefBranch, name, branch.GetCommit().GetSHA()))
		}

		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	sortReachabilityRefsByName(branches)
	return branches, nil
}

func (c *Client) ListBranches(ctx context.Context, owner, repo string) ([]string, error) {
	branches, err := c.ListBranchHeads(ctx, owner, repo)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(branches))
	for _, branch := range branches {
		if branch.Name != "" {
			names = append(names, branch.Name)
		}
	}
	return names, nil
}

func (c *Client) ListTagHeads(ctx context.Context, owner, repo string) ([]reachabilityRef, error) {
	if c != nil && c.listTagHeadsFn != nil {
		return slicesCloneReachabilityRefs(c.listTagHeadsFn(ctx, owner, repo))
	}
	if c != nil && c.listTagsFn != nil {
		tags, err := c.listTagsFn(ctx, owner, repo)
		if err != nil {
			return nil, err
		}
		return namesToReachabilityRefs(tags, reachabilityRefTag), nil
	}

	opts := &github.ListOptions{PerPage: 100}

	var tags []reachabilityRef
	for {
		operation := fmt.Sprintf("list tags for %s/%s", owner, repo)
		var page []*github.RepositoryTag
		resp, err := c.withAnonymousFallback(ctx, operation, func(ctx context.Context, client *github.Client) (*github.Response, error) {
			var innerResp *github.Response
			var innerErr error
			page, innerResp, innerErr = client.Repositories.ListTags(ctx, owner, repo, opts)
			return innerResp, innerErr
		})
		if err != nil {
			return nil, wrapAPIError(operation, resp, err)
		}

		for _, tag := range page {
			if tag == nil {
				continue
			}
			name := tag.GetName()
			if name == "" {
				continue
			}
			tags = append(tags, newReachabilityRef(reachabilityRefTag, name, tag.GetCommit().GetSHA()))
		}

		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	sortReachabilityRefsByName(tags)
	return tags, nil
}

func (c *Client) ListTags(ctx context.Context, owner, repo string) ([]string, error) {
	tags, err := c.ListTagHeads(ctx, owner, repo)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag.Name != "" {
			names = append(names, tag.Name)
		}
	}
	return names, nil
}

func (c *Client) RefContains(ctx context.Context, owner, repo, base, target string) (bool, error) {
	if c != nil && c.refContainsFn != nil {
		return c.refContainsFn(ctx, owner, repo, base, target)
	}

	operation := fmt.Sprintf("compare commits for %s/%s (%s..%s)", owner, repo, base, target)
	var diff *github.CommitsComparison
	resp, err := c.withAnonymousFallback(ctx, operation, func(ctx context.Context, client *github.Client) (*github.Response, error) {
		var innerResp *github.Response
		var innerErr error
		diff, innerResp, innerErr = client.Repositories.CompareCommits(
			ctx,
			owner,
			repo,
			base,
			target,
			&github.ListOptions{PerPage: 1},
		)
		return innerResp, innerErr
	})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, wrapAPIError(operation, resp, err)
	}

	status := diff.GetStatus()
	return status == "behind" || status == "identical", nil
}

func (c *Client) withAnonymousFallback(
	ctx context.Context,
	operation string,
	fn func(context.Context, *github.Client) (*github.Response, error),
) (*github.Response, error) {
	resp, err := c.withRetryClient(ctx, c.api, operation, fn)
	if !c.shouldFallbackToAnonymous(resp, err) {
		return resp, err
	}

	c.debugf("%s: falling back to anonymous client after SAML enforcement", operation)
	fallbackResp, fallbackErr := c.withRetryClient(ctx, c.anonymous, operation+" (anonymous fallback)", fn)
	if fallbackErr == nil {
		c.debugf("%s: anonymous fallback succeeded", operation)
		return fallbackResp, nil
	}

	c.debugf("%s: anonymous fallback failed: %v", operation, fallbackErr)
	return fallbackResp, fmt.Errorf("authenticated request hit SAML enforcement; anonymous fallback failed: %w", fallbackErr)
}

func (c *Client) withRetryClient(
	ctx context.Context,
	client *github.Client,
	operation string,
	fn func(context.Context, *github.Client) (*github.Response, error),
) (*github.Response, error) {
	var (
		resp *github.Response
		err  error
	)

	for attempt := 0; attempt < apiRetryMaxAttempts; attempt++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}

		resp, err = fn(ctx, client)
		if err == nil {
			return resp, nil
		}

		if attempt == apiRetryMaxAttempts-1 {
			c.debugf(
				"%s: giving up after attempt %d/%d failed (status=%d: %v)",
				operation,
				attempt+1,
				apiRetryMaxAttempts,
				statusCode(resp),
				err,
			)
			break
		}

		delay, ok := retryDelay(resp, err, attempt)
		if !ok {
			break
		}

		c.debugf(
			"%s: retrying in %s after attempt %d/%d failed (status=%d: %v)",
			operation,
			delay,
			attempt+1,
			apiRetryMaxAttempts,
			statusCode(resp),
			err,
		)
		if err := sleepContextFn(ctx, delay); err != nil {
			return nil, err
		}
	}

	return resp, err
}

func (c *Client) shouldFallbackToAnonymous(resp *github.Response, err error) bool {
	if c == nil || c.anonymous == nil || err == nil {
		return false
	}
	return isSAMLForbidden(resp, err)
}

func retryDelay(resp *github.Response, err error, attempt int) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return 0, false
	}

	var rateLimitErr *github.RateLimitError
	if errors.As(err, &rateLimitErr) {
		if reset := rateLimitErr.Rate.Reset.Time; !reset.IsZero() {
			delay := time.Until(reset) + apiPrimaryRateLimitBuffer
			return boundedRetryDelay(delay)
		}
		return boundedRetryDelay(retryBackoff(attempt))
	}

	var abuseRateLimitErr *github.AbuseRateLimitError
	if errors.As(err, &abuseRateLimitErr) {
		if delay := abuseRateLimitErr.GetRetryAfter(); delay > 0 {
			return boundedRetryDelay(delay)
		}
		return boundedRetryDelay(retryBackoff(attempt))
	}

	if delay := retryAfterDelay(resp); delay > 0 {
		return boundedRetryDelay(delay)
	}

	switch statusCode(resp) {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return boundedRetryDelay(retryBackoff(attempt))
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return boundedRetryDelay(retryBackoff(attempt))
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return boundedRetryDelay(retryBackoff(attempt))
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return boundedRetryDelay(retryBackoff(attempt))
	}

	return 0, false
}

func boundedRetryDelay(delay time.Duration) (time.Duration, bool) {
	if delay <= 0 {
		return 0, false
	}
	if delay > apiRetryMaxDelay {
		return 0, false
	}
	return delay, true
}

func retryAfterDelay(resp *github.Response) time.Duration {
	if resp == nil || resp.Response == nil {
		return 0
	}

	retryAfter := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if retryAfter == "" {
		return 0
	}

	if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}

	if when, err := http.ParseTime(retryAfter); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}

	return 0
}

func retryBackoff(attempt int) time.Duration {
	delay := apiRetryBaseDelay
	for i := 0; i < attempt; i++ {
		if delay >= apiRetryMaxBackoff/2 {
			return apiRetryMaxBackoff
		}
		delay *= 2
	}
	return delay
}

func isSAMLForbidden(resp *github.Response, err error) bool {
	if resp != nil && forbiddenResponse(resp.Response) && hasGitHubSSOHeader(resp.Header) {
		return true
	}

	var githubErr *github.ErrorResponse
	if !errors.As(err, &githubErr) {
		return false
	}

	if forbiddenResponse(githubErr.Response) && hasGitHubSSOHeader(githubErr.Response.Header) {
		return true
	}

	message := strings.ToLower(strings.TrimSpace(githubErr.Message))
	return strings.Contains(message, "saml enforcement")
}

func forbiddenResponse(resp *http.Response) bool {
	return resp != nil && resp.StatusCode == http.StatusForbidden
}

func hasGitHubSSOHeader(header http.Header) bool {
	if header == nil {
		return false
	}
	return strings.TrimSpace(header.Get("X-GitHub-SSO")) != ""
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func statusCode(resp *github.Response) int {
	if resp == nil || resp.Response == nil {
		return 0
	}
	return resp.StatusCode
}

func wrapAPIError(operation string, resp *github.Response, err error) error {
	if err == nil {
		return nil
	}
	if resp == nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return &APIError{
		Operation:  operation,
		StatusCode: resp.StatusCode,
		Err:        err,
	}
}

func newReachabilityRef(kind reachabilityRefKind, name, headSHA string) reachabilityRef {
	qualifiedName := name
	switch kind {
	case reachabilityRefBranch:
		qualifiedName = "refs/heads/" + name
	case reachabilityRefTag:
		qualifiedName = "refs/tags/" + name
	}

	return reachabilityRef{
		Name:          name,
		QualifiedName: qualifiedName,
		HeadSHA:       strings.TrimSpace(headSHA),
		Kind:          kind,
	}
}

func namesToReachabilityRefs(names []string, kind reachabilityRefKind) []reachabilityRef {
	refs := make([]reachabilityRef, 0, len(names))
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			continue
		}
		refs = append(refs, newReachabilityRef(kind, name, ""))
	}
	sortReachabilityRefsByName(refs)
	return refs
}

func sortReachabilityRefsByName(refs []reachabilityRef) {
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Name != refs[j].Name {
			return refs[i].Name < refs[j].Name
		}
		return refs[i].QualifiedName < refs[j].QualifiedName
	})
}

func slicesCloneReachabilityRefs(refs []reachabilityRef, err error) ([]reachabilityRef, error) {
	if err != nil {
		return nil, err
	}
	return append([]reachabilityRef(nil), refs...), nil
}

func hasYAMLExt(path string) bool {
	path = strings.ToLower(path)
	return strings.HasSuffix(path, ".yml") || strings.HasSuffix(path, ".yaml")
}
