package output

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sozercan/skrunch/internal/githubscan"
)

func TestWriteTextOmitsUnpinnedSummaryColumn(t *testing.T) {
	report := &githubscan.Report{
		Mode:   "path",
		Target: "/tmp/workflows",
		Repositories: []githubscan.RepositoryResult{
			{
				Source:       "/tmp/workflows",
				FilesScanned: 1,
				Findings: []githubscan.Finding{
					{
						Workflow: ".github/workflows/ci.yml",
						Ref:      "actions/checkout@main",
						Lines:    []int{10},
						Status:   githubscan.StatusOK,
						Message:  "ref resolves in the action repository",
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := WriteText(&buf, report, TextOptions{ShowOK: true}); err != nil {
		t.Fatalf("WriteText() error = %v", err)
	}

	got := buf.String()
	if strings.Contains(got, "unpinned=") {
		t.Fatalf("WriteText() output unexpectedly contains unpinned summary: %q", got)
	}
	if !strings.Contains(got, "action_refs=1 ok=1 invalid=0 retry=0 errors=0") {
		t.Fatalf("WriteText() output = %q, want updated summary", got)
	}
	if !strings.Contains(got, ".github/workflows/ci.yml") {
		t.Fatalf("WriteText() output = %q, want workflow heading", got)
	}
	if !strings.Contains(got, "| STATUS |") || !strings.Contains(got, "ACTION REF") {
		t.Fatalf("WriteText() output = %q, want findings table header", got)
	}
	if !strings.Contains(got, "actions/checkout@main") || !strings.Contains(got, "[10]") {
		t.Fatalf("WriteText() output = %q, want findings table row", got)
	}
}

func TestWriteTextRendersScanErrorsTable(t *testing.T) {
	report := &githubscan.Report{
		Mode:   "repo",
		Target: "octo/repo",
		Repositories: []githubscan.RepositoryResult{
			{
				Source:       "octo/repo",
				FilesScanned: 0,
				ScanErrors:   []string{"failed to parse workflow"},
			},
		},
	}

	var buf bytes.Buffer
	if err := WriteText(&buf, report, TextOptions{}); err != nil {
		t.Fatalf("WriteText() error = %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "scan errors") {
		t.Fatalf("WriteText() output = %q, want scan errors section", got)
	}
	if !strings.Contains(got, "failed to parse workflow") {
		t.Fatalf("WriteText() output = %q, want scan error row", got)
	}
}

func TestWriteTextRendersOrgCleanRepositoryTableWhenAllReposAreClean(t *testing.T) {
	report := &githubscan.Report{
		Mode:   "org",
		Target: "project-copacetic",
		Repositories: []githubscan.RepositoryResult{
			{
				Source:       "project-copacetic/repo-one",
				FilesScanned: 2,
				Findings: []githubscan.Finding{
					{Workflow: ".github/workflows/ci.yml", Ref: "actions/checkout@main", Status: githubscan.StatusOK},
				},
			},
			{
				Source:       "project-copacetic/repo-two",
				FilesScanned: 1,
				Findings: []githubscan.Finding{
					{Workflow: ".github/workflows/release.yml", Ref: "actions/setup-go@v5", Status: githubscan.StatusOK},
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := WriteText(&buf, report, TextOptions{}); err != nil {
		t.Fatalf("WriteText() error = %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "clean repositories") {
		t.Fatalf("WriteText() output = %q, want clean repositories section", got)
	}
	if !strings.Contains(got, "| REPOSITORY") || !strings.Contains(got, "| ACTION REFS |") {
		t.Fatalf("WriteText() output = %q, want clean repository table", got)
	}
	if strings.Contains(got, "repositories with issues") {
		t.Fatalf("WriteText() output = %q, did not expect issue repositories section", got)
	}
	if !strings.Contains(got, "project-copacetic/repo-one") || !strings.Contains(got, "project-copacetic/repo-two") {
		t.Fatalf("WriteText() output = %q, want repository rows", got)
	}
	if strings.Contains(got, "\nproject-copacetic/repo-one\nfiles=") || strings.Contains(got, "\nproject-copacetic/repo-two\nfiles=") {
		t.Fatalf("WriteText() output = %q, did not expect clean repo detail sections", got)
	}
	if strings.Contains(got, "no issues found") {
		t.Fatalf("WriteText() output = %q, did not expect no issues found fallback", got)
	}
}

func TestWriteTextRendersOrgIssueTableAndDetails(t *testing.T) {
	report := &githubscan.Report{
		Mode:   "org",
		Target: "project-copacetic",
		Repositories: []githubscan.RepositoryResult{
			{
				Source:       "project-copacetic/repo-clean",
				FilesScanned: 1,
				Findings: []githubscan.Finding{
					{Workflow: ".github/workflows/clean.yml", Ref: "actions/checkout@main", Status: githubscan.StatusOK},
				},
			},
			{
				Source:       "project-copacetic/repo-retry",
				FilesScanned: 1,
				Findings: []githubscan.Finding{
					{
						Workflow: ".github/workflows/ci.yml",
						Ref:      "actions/checkout@deadbeef",
						Lines:    []int{12},
						Status:   githubscan.StatusRetry,
						Message:  "retry later: could not inspect actions/checkout on github.com: status 502: bad gateway",
					},
				},
			},
			{
				Source:       "project-copacetic/repo-unpinned",
				FilesScanned: 1,
				Findings: []githubscan.Finding{
					{
						Workflow: ".github/workflows/release.yml",
						Ref:      "actions/setup-go@main",
						Lines:    []int{7},
						Status:   githubscan.StatusUnpinned,
						Message:  "ref is not pinned to a tag or full commit SHA",
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := WriteText(&buf, report, TextOptions{}); err != nil {
		t.Fatalf("WriteText() error = %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "clean repositories") {
		t.Fatalf("WriteText() output = %q, want clean repositories section", got)
	}
	if !strings.Contains(got, "repositories with issues") {
		t.Fatalf("WriteText() output = %q, want issue repositories section", got)
	}
	if !strings.Contains(got, "| ACTION REFS |") || !strings.Contains(got, "| NEEDS PIN |") || !strings.Contains(got, "| RETRY |") {
		t.Fatalf("WriteText() output = %q, want issue count columns", got)
	}
	if !strings.Contains(got, "column guide: action refs=checked uses: refs") {
		t.Fatalf("WriteText() output = %q, want issue column guide", got)
	}
	if !strings.Contains(got, "project-copacetic/repo-clean") || !strings.Contains(got, "project-copacetic/repo-retry") || !strings.Contains(got, "project-copacetic/repo-unpinned") {
		t.Fatalf("WriteText() output = %q, want all repository rows", got)
	}
	if strings.Contains(got, "\nproject-copacetic/repo-clean\nfiles=") {
		t.Fatalf("WriteText() output = %q, did not expect clean repo detail section", got)
	}
	if !strings.Contains(got, "\nproject-copacetic/repo-retry\nfiles=1 action_refs=1 ok=0 invalid=0 retry=1 errors=0\n") {
		t.Fatalf("WriteText() output = %q, want retry repo detail section", got)
	}
	if !strings.Contains(got, "\nproject-copacetic/repo-unpinned\n") || !strings.Contains(got, "ref is not pinned to a tag or full commit SHA") {
		t.Fatalf("WriteText() output = %q, want unpinned repo issues listed", got)
	}
}
