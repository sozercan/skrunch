package githubscan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/google/go-github/v69/github"
	"github.com/sozercan/skrunch/internal/workflows"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

const actionsPrefix = "actions://"

type FindingStatus string

const (
	StatusOK        FindingStatus = "ok"
	StatusUnpinned  FindingStatus = "unpinned"
	StatusInvalid   FindingStatus = "invalid"
	StatusRetry     FindingStatus = "retry"
	StatusError     FindingStatus = "error"
	defaultParallel               = 8
)

type Finding struct {
	Workflow string
	Ref      string
	Lines    []int
	Status   FindingStatus
	Message  string
}

type RepositoryResult struct {
	Name         string
	Source       string
	FilesScanned int
	Findings     []Finding
	ScanErrors   []string
}

type Report struct {
	Mode         string
	Target       string
	Repositories []RepositoryResult
}

type Summary struct {
	Repositories int
	Files        int
	Refs         int
	OK           int
	Unpinned     int
	Invalid      int
	Retry        int
	Errors       int
}

type Options struct {
	Concurrency     int
	IncludeForks    bool
	IncludeArchived bool
	RepoMatch       string
	RepoLimit       int
	Progress        io.Writer
	DebugWriter     io.Writer
}

type Scanner struct {
	client *Client
	opts   Options

	progressMu sync.Mutex
	cacheMu    sync.Mutex

	validationCache map[string]validationResult
	repoRefsCache   map[string][]reachabilityRef

	validationGroup singleflight.Group
	repoRefsGroup   singleflight.Group
}

type validationResult struct {
	Status  FindingStatus
	Message string
}

type fileScanResult struct {
	findings []Finding
	scanErr  string
}

type actionRef struct {
	Owner string
	Repo  string
	Path  string
	Ref   string
}

func NewScanner(client *Client, opts Options) *Scanner {
	if opts.Concurrency <= 0 {
		opts.Concurrency = defaultParallel
	}

	scanner := &Scanner{
		client:          client,
		opts:            opts,
		validationCache: make(map[string]validationResult),
		repoRefsCache:   make(map[string][]reachabilityRef),
	}
	if scanner.client != nil {
		scanner.client.debugLogf = scanner.debugf
	}
	return scanner
}

func ParseRepository(input string) (string, string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", fmt.Errorf("repository cannot be empty")
	}

	if strings.Contains(input, "://") {
		parsed, err := url.Parse(input)
		if err != nil {
			return "", "", fmt.Errorf("parse repository URL %q: %w", input, err)
		}
		input = strings.Trim(parsed.Path, "/")
	}

	input = strings.TrimSuffix(input, ".git")
	parts := strings.Split(input, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("repository must be OWNER/REPO or a repository URL")
	}

	return parts[0], parts[1], nil
}

func (r *Report) Summary() Summary {
	if r == nil {
		return Summary{}
	}

	var summary Summary
	summary.Repositories = len(r.Repositories)

	for _, repo := range r.Repositories {
		summary.Files += repo.FilesScanned
		summary.Errors += len(repo.ScanErrors)
		summary.Refs += len(repo.Findings)

		for _, finding := range repo.Findings {
			switch finding.Status {
			case StatusOK:
				summary.OK++
			case StatusUnpinned:
				summary.Unpinned++
			case StatusInvalid:
				summary.Invalid++
			case StatusRetry:
				summary.Retry++
			case StatusError:
				summary.Errors++
			}
		}
	}

	return summary
}

func (r *Report) HasFailures() bool {
	summary := r.Summary()
	return summary.Unpinned > 0 || summary.Invalid > 0 || summary.Retry > 0 || summary.Errors > 0
}

func (s *Scanner) ScanRepo(ctx context.Context, input string) (*Report, error) {
	if s.client == nil {
		return nil, fmt.Errorf("GitHub client is required")
	}

	owner, repo, err := ParseRepository(input)
	if err != nil {
		return nil, err
	}

	progress := newProgressIndicator(s.opts.Progress, progressModeRepo, owner+"/"+repo, 1, s.opts.DebugWriter == nil)
	defer progress.Stop()

	s.debugf("resolving repository %s/%s", owner, repo)
	if _, err := s.client.GetRepository(ctx, owner, repo); err != nil {
		return nil, err
	}

	result := s.scanRemoteRepository(ctx, owner, repo, progress)
	progress.Finish()
	return &Report{
		Mode:         "repo",
		Target:       owner + "/" + repo,
		Repositories: []RepositoryResult{result},
	}, nil
}

func (s *Scanner) ScanOrg(ctx context.Context, org string) (*Report, error) {
	if s.client == nil {
		return nil, fmt.Errorf("GitHub client is required")
	}

	s.debugf("listing repositories for org %s", org)
	progress := newProgressIndicator(s.opts.Progress, progressModeOrg, org, 0, s.opts.DebugWriter == nil)
	defer progress.Stop()

	if !progress.interactive {
		s.progressf("scanning repositories in %s\n", org)
	}

	results := make([]RepositoryResult, 0)
	var mu sync.Mutex

	group, ctx := errgroup.WithContext(ctx)
	repoCh := make(chan *github.Repository)
	workerCount := s.opts.Concurrency
	if workerCount <= 0 {
		workerCount = defaultParallel
	}

	for i := 0; i < workerCount; i++ {
		group.Go(func() error {
			for repository := range repoCh {
				fullName := repository.GetFullName()
				owner := repository.GetOwner().GetLogin()
				name := repository.GetName()

				progress.repoStarted()
				if !progress.interactive {
					s.progressf("scanning %s\n", fullName)
				}

				result := s.scanRemoteRepository(ctx, owner, name, progress)
				repoSummary := summarizeRepository(result)
				progress.repoFinished()
				if !progress.interactive {
					s.progressf(
						"finished %s: files=%d refs=%d invalid=%d retry=%d errors=%d\n",
						fullName,
						result.FilesScanned,
						len(result.Findings),
						repoSummary.Invalid,
						repoSummary.Retry,
						repoSummary.Errors,
					)
				}

				mu.Lock()
				results = append(results, result)
				mu.Unlock()
			}
			return nil
		})
	}

	group.Go(func() error {
		defer close(repoCh)

		page := 1
		listed := 0
		queued := 0
		for {
			repositories, nextPage, err := s.client.ListOrgRepositoriesPage(ctx, org, page)
			if err != nil {
				return err
			}
			listed += len(repositories)

			filtered, stop := filterOrgRepositories(repositories, s.opts, queued)
			if len(filtered) > 0 {
				queued += len(filtered)
				progress.addRepos(len(filtered))
				s.debugf(
					"org %s: page %d listed %d repositories, queued %d after filters",
					org,
					page,
					len(repositories),
					queued,
				)
				for _, repository := range filtered {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case repoCh <- repository:
					}
				}
			} else {
				s.debugf(
					"org %s: page %d listed %d repositories, queued %d after filters",
					org,
					page,
					len(repositories),
					queued,
				)
			}

			if stop || nextPage == 0 {
				s.debugf(
					"org %s: queued %d repositories after inspecting %d listed repositories",
					org,
					queued,
					listed,
				)
				return nil
			}
			page = nextPage
		}
	})

	if err := group.Wait(); err != nil {
		return nil, err
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Source < results[j].Source
	})

	progress.Finish()
	return &Report{
		Mode:         "org",
		Target:       org,
		Repositories: results,
	}, nil
}

func (s *Scanner) ScanPath(ctx context.Context, root string) (*Report, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve path %s: %w", root, err)
	}

	progress := newProgressIndicator(s.opts.Progress, progressModePath, absRoot, 1, s.opts.DebugWriter == nil)
	defer progress.Stop()

	s.debugf("discovering local workflow files under %s", absRoot)
	files, err := workflows.DiscoverLocalFiles(absRoot)
	if err != nil {
		return nil, err
	}
	s.debugf("discovered %d local workflow/composite files under %s", len(files), absRoot)

	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", absRoot, err)
	}

	result := RepositoryResult{
		Name:         filepath.Base(absRoot),
		Source:       absRoot,
		FilesScanned: len(files),
	}

	progress.addFiles(len(files))
	partials := make([]fileScanResult, len(files))
	var group errgroup.Group
	group.SetLimit(s.opts.Concurrency)
	for i, file := range files {
		i, file := i, file
		group.Go(func() error {
			defer progress.fileFinished()

			doc, err := workflows.ParseFile(file)
			displayPath := file
			if info.IsDir() {
				if rel, relErr := filepath.Rel(absRoot, file); relErr == nil {
					displayPath = filepath.ToSlash(rel)
				}
			}

			if err != nil {
				partials[i].scanErr = fmt.Sprintf("%s: %v", displayPath, err)
				s.debugf("%s: failed to parse workflow file: %v", displayPath, err)
				return nil
			}

			doc.Path = displayPath
			partials[i].findings = s.scanDocument(ctx, "", doc)
			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return nil, err
	}

	for _, partial := range partials {
		if partial.scanErr != "" {
			result.ScanErrors = append(result.ScanErrors, partial.scanErr)
		}
		result.Findings = append(result.Findings, partial.findings...)
	}

	sortRepositoryResult(&result)
	progress.Finish()
	return &Report{
		Mode:         "path",
		Target:       absRoot,
		Repositories: []RepositoryResult{result},
	}, nil
}

func (s *Scanner) scanRemoteRepository(ctx context.Context, owner, repo string, progress *progressIndicator) RepositoryResult {
	result := RepositoryResult{
		Name:   repo,
		Source: owner + "/" + repo,
	}

	s.debugf("%s: listing workflow files", result.Source)
	files, err := s.client.WorkflowFiles(ctx, owner, repo)
	if err != nil {
		s.debugf("%s: failed to list workflow files: %v", result.Source, err)
		result.ScanErrors = append(result.ScanErrors, err.Error())
		return result
	}
	s.debugf("%s: discovered %d workflow files", result.Source, len(files))

	result.FilesScanned = len(files)
	progress.addFiles(len(files))
	partials := make([]fileScanResult, len(files))
	var group errgroup.Group
	group.SetLimit(s.opts.Concurrency)
	for i, file := range files {
		i, file := i, file
		group.Go(func() error {
			defer progress.fileFinished()

			doc, err := workflows.ParseBytes(file.Path, file.Content)
			if err != nil {
				partials[i].scanErr = fmt.Sprintf("%s: %v", file.Path, err)
				s.debugf("%s: failed to parse workflow file: %v", documentLabel(result.Source, file.Path), err)
				return nil
			}

			partials[i].findings = s.scanDocument(ctx, result.Source, doc)
			return nil
		})
	}

	if err := group.Wait(); err != nil {
		result.ScanErrors = append(result.ScanErrors, err.Error())
	}

	for _, partial := range partials {
		if partial.scanErr != "" {
			result.ScanErrors = append(result.ScanErrors, partial.scanErr)
		}
		result.Findings = append(result.Findings, partial.findings...)
	}

	sortRepositoryResult(&result)
	return result
}

func (s *Scanner) scanDocument(ctx context.Context, source string, doc workflows.Document) []Finding {
	actionRefs := 0
	for _, ref := range doc.Refs {
		if strings.HasPrefix(ref.Normalized, actionsPrefix) {
			actionRefs++
		}
	}

	s.debugf("%s: validating %d action refs", documentLabel(source, doc.Path), actionRefs)

	findings := make([]Finding, 0, actionRefs)
	for _, ref := range doc.Refs {
		if !strings.HasPrefix(ref.Normalized, actionsPrefix) {
			continue
		}

		outcome := s.validateActionRef(ctx, ref.Normalized)
		findings = append(findings, Finding{
			Workflow: doc.Path,
			Ref:      displayActionRef(ref.Normalized),
			Lines:    slices.Clone(ref.Lines),
			Status:   outcome.Status,
			Message:  outcome.Message,
		})
	}
	return findings
}

func (s *Scanner) validateActionRef(ctx context.Context, normalized string) validationResult {
	ref, err := parseActionRef(normalized)
	if err != nil {
		s.debugf("failed to parse action ref %q: %v", displayActionRef(normalized), err)
		return validationResult{
			Status:  StatusError,
			Message: err.Error(),
		}
	}
	debugName := debugActionRef(ref)

	key := strings.ToLower(ref.Owner + "/" + ref.Repo + "@" + ref.Ref)

	s.cacheMu.Lock()
	if cached, ok := s.validationCache[key]; ok {
		s.cacheMu.Unlock()
		s.debugf("validation cache hit for %s", debugName)
		return cached
	}
	s.cacheMu.Unlock()

	value, _, _ := s.validationGroup.Do(key, func() (any, error) {
		s.debugf("validating %s", debugName)
		result := s.validateActionRefTarget(ctx, ref)

		s.cacheMu.Lock()
		s.validationCache[key] = result
		s.cacheMu.Unlock()

		s.debugf("validation result for %s: %s", debugName, result.Status)
		return result, nil
	})

	outcome, ok := value.(validationResult)
	if !ok {
		return validationResult{
			Status:  StatusError,
			Message: "unexpected validation result type",
		}
	}
	return outcome
}

func (s *Scanner) validateActionRefTarget(ctx context.Context, ref actionRef) validationResult {
	if !isFullSHA(ref.Ref) {
		return s.validateResolvableRef(ctx, ref)
	}
	return s.validateReachableCommit(ctx, ref)
}

func (s *Scanner) validateResolvableRef(ctx context.Context, ref actionRef) validationResult {
	err := s.client.ResolveCommit(ctx, ref.Owner, ref.Repo, ref.Ref)
	if err == nil {
		return validationResult{
			Status:  StatusOK,
			Message: resolvedMessage(ref.Ref),
		}
	}

	s.debugf("failed to resolve %s: %v", debugActionRef(ref), err)
	if result, ok := s.retryValidationResult(ref, err); ok {
		return result
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusNotFound:
			return validationResult{
				Status:  StatusInvalid,
				Message: unresolvedMessage(ref.Ref),
			}
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
			return validationResult{
				Status:  StatusError,
				Message: fmt.Sprintf("could not inspect %s/%s on %s: %v", ref.Owner, ref.Repo, s.client.Host(), err),
			}
		}
	}

	return validationResult{
		Status:  StatusError,
		Message: fmt.Sprintf("could not inspect %s/%s: %v", ref.Owner, ref.Repo, err),
	}
}

func (s *Scanner) validateReachableCommit(ctx context.Context, ref actionRef) validationResult {
	resolved := s.validateResolvableRef(ctx, ref)
	if resolved.Status != StatusOK {
		return resolved
	}

	refs, err := s.repoRefs(ctx, ref.Owner, ref.Repo)
	if err != nil {
		s.debugf("failed to load refs for %s: %v", debugActionRef(ref), err)
		if result, ok := s.retryValidationResult(ref, err); ok {
			return result
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case http.StatusNotFound:
				return validationResult{
					Status:  StatusInvalid,
					Message: fmt.Sprintf("could not find %s/%s on %s", ref.Owner, ref.Repo, s.client.Host()),
				}
			case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
				return validationResult{
					Status:  StatusError,
					Message: fmt.Sprintf("could not inspect %s/%s on %s: %v", ref.Owner, ref.Repo, s.client.Host(), err),
				}
			}
		}

		return validationResult{
			Status:  StatusError,
			Message: fmt.Sprintf("could not inspect %s/%s: %v", ref.Owner, ref.Repo, err),
		}
	}

	debugName := debugActionRef(ref)
	if headRef, ok := matchingHeadRef(refs, ref.Ref); ok {
		s.debugf("%s matches head of %s", debugName, headRef.QualifiedName)
		return validationResult{
			Status:  StatusOK,
			Message: "commit is reachable from a branch or tag",
		}
	}

	s.debugf("checking %s across %d refs", debugName, len(refs))
	for i, base := range refs {
		s.debugf("comparing %s against %s (%d/%d)", debugName, base.QualifiedName, i+1, len(refs))
		contains, err := s.client.RefContains(ctx, ref.Owner, ref.Repo, base.QualifiedName, ref.Ref)
		if err != nil {
			s.debugf("compare failed for %s at %s: %v", debugName, base.QualifiedName, err)
			if result, ok := s.retryValidationResult(ref, err); ok {
				return result
			}
			return validationResult{
				Status:  StatusError,
				Message: fmt.Sprintf("compare failed for %s/%s: %v", ref.Owner, ref.Repo, err),
			}
		}
		if contains {
			s.debugf("%s is reachable from %s", debugName, base.QualifiedName)
			return validationResult{
				Status:  StatusOK,
				Message: "commit is reachable from a branch or tag",
			}
		}
	}

	s.debugf("%s is not reachable from any branch or tag", debugName)
	return validationResult{
		Status:  StatusInvalid,
		Message: "commit SHA is not reachable from any branch or tag in the action repository",
	}
}

func (s *Scanner) retryValidationResult(ref actionRef, err error) (validationResult, bool) {
	if !isRetryableValidationError(err) {
		return validationResult{}, false
	}

	return validationResult{
		Status:  StatusRetry,
		Message: fmt.Sprintf("retry later: could not inspect %s/%s on %s: %v", ref.Owner, ref.Repo, s.client.Host(), err),
	}, true
}

func isRetryableValidationError(err error) bool {
	if err == nil {
		return false
	}

	var rateLimitErr *github.RateLimitError
	if errors.As(err, &rateLimitErr) {
		return true
	}

	var abuseRateLimitErr *github.AbuseRateLimitError
	if errors.As(err, &abuseRateLimitErr) {
		return true
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) && isRetryableAPIStatus(apiErr.StatusCode) && hasRateLimitMessage(err.Error()) {
		return true
	}

	return false
}

func isRetryableAPIStatus(statusCode int) bool {
	return statusCode == http.StatusForbidden || statusCode == http.StatusTooManyRequests
}

func hasRateLimitMessage(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(message, "rate limit exceeded") ||
		strings.Contains(message, "secondary rate limit") ||
		strings.Contains(message, "abuse detection") ||
		strings.Contains(message, "rate reset in") ||
		strings.Contains(message, "retry-after")
}

func (s *Scanner) repoRefs(ctx context.Context, owner, repo string) ([]reachabilityRef, error) {
	key := strings.ToLower(owner + "/" + repo)

	s.cacheMu.Lock()
	if cached, ok := s.repoRefsCache[key]; ok {
		s.cacheMu.Unlock()
		s.debugf("repo refs cache hit for %s/%s", owner, repo)
		return append([]reachabilityRef(nil), cached...), nil
	}
	s.cacheMu.Unlock()

	value, err, _ := s.repoRefsGroup.Do(key, func() (any, error) {
		s.debugf("fetching branches and tags for %s/%s", owner, repo)
		branches, err := s.client.ListBranchHeads(ctx, owner, repo)
		if err != nil {
			s.debugf("failed to list branches for %s/%s: %v", owner, repo, err)
			return nil, err
		}

		tags, err := s.client.ListTagHeads(ctx, owner, repo)
		if err != nil {
			s.debugf("failed to list tags for %s/%s: %v", owner, repo, err)
			return nil, err
		}

		refs := orderReachabilityRefs(branches, tags)

		s.cacheMu.Lock()
		s.repoRefsCache[key] = append([]reachabilityRef(nil), refs...)
		s.cacheMu.Unlock()

		s.debugf("loaded %d refs for %s/%s", len(refs), owner, repo)
		return refs, nil
	})
	if err != nil {
		return nil, err
	}

	refs, ok := value.([]reachabilityRef)
	if !ok {
		return nil, fmt.Errorf("unexpected repo refs result type")
	}

	return append([]reachabilityRef(nil), refs...), nil
}

func matchingHeadRef(refs []reachabilityRef, sha string) (reachabilityRef, bool) {
	for _, ref := range refs {
		if ref.HeadSHA != "" && strings.EqualFold(ref.HeadSHA, sha) {
			return ref, true
		}
	}
	return reachabilityRef{}, false
}

func orderReachabilityRefs(branches, tags []reachabilityRef) []reachabilityRef {
	orderedBranches := append([]reachabilityRef(nil), branches...)
	orderedTags := append([]reachabilityRef(nil), tags...)

	sort.Slice(orderedBranches, func(i, j int) bool {
		leftPriority := branchPriority(orderedBranches[i].Name)
		rightPriority := branchPriority(orderedBranches[j].Name)
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		if orderedBranches[i].Name != orderedBranches[j].Name {
			return orderedBranches[i].Name < orderedBranches[j].Name
		}
		return orderedBranches[i].QualifiedName < orderedBranches[j].QualifiedName
	})
	sortReachabilityRefsByName(orderedTags)

	out := make([]reachabilityRef, 0, len(orderedBranches)+len(orderedTags))
	seenHeads := make(map[string]struct{})
	for _, ref := range append(orderedBranches, orderedTags...) {
		headKey := strings.ToLower(strings.TrimSpace(ref.HeadSHA))
		if headKey != "" {
			if _, ok := seenHeads[headKey]; ok {
				continue
			}
			seenHeads[headKey] = struct{}{}
		}
		out = append(out, ref)
	}

	return out
}

func branchPriority(name string) int {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "main":
		return 0
	case "master":
		return 1
	default:
		return 2
	}
}

func (s *Scanner) progressf(format string, args ...any) {
	s.logf(s.opts.Progress, "", format, args...)
}

func (s *Scanner) debugf(format string, args ...any) {
	s.logf(s.opts.DebugWriter, "debug: ", format, args...)
}

func (s *Scanner) logf(w io.Writer, prefix, format string, args ...any) {
	if w == nil {
		return
	}

	s.progressMu.Lock()
	defer s.progressMu.Unlock()

	if !strings.HasSuffix(format, "\n") {
		format += "\n"
	}
	fmt.Fprintf(w, prefix+format, args...)
}

func parseActionRef(normalized string) (actionRef, error) {
	if !strings.HasPrefix(normalized, actionsPrefix) {
		return actionRef{}, fmt.Errorf("unsupported ref scheme in %q", normalized)
	}

	raw := strings.TrimPrefix(normalized, actionsPrefix)
	action, sha, ok := strings.Cut(raw, "@")
	if !ok || sha == "" {
		return actionRef{}, fmt.Errorf("missing @ref in %q", normalized)
	}

	parts := strings.SplitN(action, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return actionRef{}, fmt.Errorf("missing owner/repo in %q", normalized)
	}

	ref := actionRef{
		Owner: parts[0],
		Repo:  parts[1],
		Ref:   sha,
	}
	if len(parts) == 3 {
		ref.Path = parts[2]
	}

	return ref, nil
}

func displayActionRef(normalized string) string {
	return strings.TrimPrefix(normalized, actionsPrefix)
}

func documentLabel(source, path string) string {
	path = filepath.ToSlash(path)
	if source == "" {
		return path
	}
	return strings.TrimRight(source, "/") + "/" + strings.TrimLeft(path, "/")
}

func debugActionRef(ref actionRef) string {
	action := ref.Owner + "/" + ref.Repo
	if ref.Path != "" {
		action += "/" + ref.Path
	}
	return action + "@" + shortSHA(ref.Ref)
}

func shortSHA(ref string) string {
	if len(ref) <= 12 {
		return ref
	}
	return ref[:12]
}

func unresolvedMessage(ref string) string {
	if isFullSHA(ref) {
		return "commit SHA could not be resolved in the action repository"
	}
	return fmt.Sprintf("ref %q could not be resolved in the action repository", ref)
}

func resolvedMessage(ref string) string {
	if isFullSHA(ref) {
		return "commit SHA resolves in the action repository"
	}
	return "ref resolves in the action repository"
}

func isFullSHA(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	for _, ch := range ref {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') && (ch < 'A' || ch > 'F') {
			return false
		}
	}
	return true
}

func sortRepositoryResult(result *RepositoryResult) {
	sort.Slice(result.Findings, func(i, j int) bool {
		if result.Findings[i].Workflow != result.Findings[j].Workflow {
			return result.Findings[i].Workflow < result.Findings[j].Workflow
		}
		if firstLine(result.Findings[i].Lines) != firstLine(result.Findings[j].Lines) {
			return firstLine(result.Findings[i].Lines) < firstLine(result.Findings[j].Lines)
		}
		if result.Findings[i].Status != result.Findings[j].Status {
			return result.Findings[i].Status < result.Findings[j].Status
		}
		return result.Findings[i].Ref < result.Findings[j].Ref
	})

	sort.Strings(result.ScanErrors)
}

func firstLine(lines []int) int {
	if len(lines) == 0 {
		return 0
	}
	return lines[0]
}

func summarizeRepository(result RepositoryResult) Summary {
	summary := Summary{
		Repositories: 1,
		Files:        result.FilesScanned,
		Refs:         len(result.Findings),
		Errors:       len(result.ScanErrors),
	}

	for _, finding := range result.Findings {
		switch finding.Status {
		case StatusOK:
			summary.OK++
		case StatusUnpinned:
			summary.Unpinned++
		case StatusInvalid:
			summary.Invalid++
		case StatusRetry:
			summary.Retry++
		case StatusError:
			summary.Errors++
		}
	}

	return summary
}

func filterOrgRepositories(repositories []*github.Repository, opts Options, queued int) ([]*github.Repository, bool) {
	if opts.RepoLimit > 0 && queued >= opts.RepoLimit {
		return nil, true
	}

	remaining := opts.RepoLimit - queued
	if opts.RepoLimit <= 0 {
		remaining = 0
	}

	filtered := make([]*github.Repository, 0, len(repositories))
	for _, repository := range repositories {
		if !includeOrgRepository(repository, opts) {
			continue
		}

		filtered = append(filtered, repository)
		if remaining > 0 && len(filtered) >= remaining {
			return filtered, true
		}
	}

	return filtered, false
}

func includeOrgRepository(repository *github.Repository, opts Options) bool {
	if repository == nil {
		return false
	}
	if !opts.IncludeArchived && repository.GetArchived() {
		return false
	}
	if !opts.IncludeForks && repository.GetFork() {
		return false
	}
	if !matchesOrgRepository(repository, opts.RepoMatch) {
		return false
	}
	return true
}

func matchesOrgRepository(repository *github.Repository, match string) bool {
	match = strings.TrimSpace(strings.ToLower(match))
	if match == "" {
		return true
	}
	name := strings.ToLower(repository.GetName())
	fullName := strings.ToLower(repository.GetFullName())
	return strings.Contains(name, match) || strings.Contains(fullName, match)
}
