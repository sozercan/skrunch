package output

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/sozercan/skrunch/internal/githubscan"
)

type TextOptions struct {
	ShowOK bool
}

type workflowGroup struct {
	Workflow string
	Findings []githubscan.Finding
}

func WriteText(w io.Writer, report *githubscan.Report, opts TextOptions) error {
	if report == nil {
		return fmt.Errorf("report is required")
	}

	summary := report.Summary()
	if _, err := fmt.Fprintf(
		w,
		"%s %s\nrepos=%d files=%d action_refs=%d ok=%d invalid=%d retry=%d errors=%d\n",
		strings.ToUpper(report.Mode),
		report.Target,
		summary.Repositories,
		summary.Files,
		summary.Refs,
		summary.OK,
		summary.Invalid,
		summary.Retry,
		summary.Errors,
	); err != nil {
		return err
	}

	printedSection := false
	if report.Mode == "org" && len(report.Repositories) > 0 {
		cleanRepos, issueRepos := partitionRepositories(report.Repositories)
		if len(cleanRepos) > 0 {
			if _, err := fmt.Fprintln(w, "\nclean repositories"); err != nil {
				return err
			}
			if err := writeCleanRepositoryTable(w, cleanRepos); err != nil {
				return err
			}
			printedSection = true
		}
		if len(issueRepos) > 0 {
			if _, err := fmt.Fprintln(w, "\nrepositories with issues"); err != nil {
				return err
			}
			if err := writeIssueRepositoryTable(w, issueRepos); err != nil {
				return err
			}
			if err := writeIssueColumnGuide(w); err != nil {
				return err
			}
			printedSection = true
		}
	}

	for _, repo := range report.Repositories {
		visible := visibleFindings(repo.Findings, opts.ShowOK)
		showRepo := shouldShowRepositoryDetails(report.Mode, repo, opts.ShowOK)
		if !showRepo {
			continue
		}

		printedSection = true
		repoSummary := summarizeRepo(repo)
		if _, err := fmt.Fprintf(
			w,
			"\n%s\nfiles=%d action_refs=%d ok=%d invalid=%d retry=%d errors=%d\n",
			repo.Source,
			repo.FilesScanned,
			len(repo.Findings),
			repoSummary.OK,
			repoSummary.Invalid,
			repoSummary.Retry,
			repoSummary.Errors,
		); err != nil {
			return err
		}

		if len(visible) == 0 && len(repo.ScanErrors) == 0 {
			if _, err := fmt.Fprintln(w, "clean"); err != nil {
				return err
			}
			continue
		}

		for _, group := range groupFindingsByWorkflow(visible) {
			if _, err := fmt.Fprintln(w, group.Workflow); err != nil {
				return err
			}
			if err := writeFindingsTable(w, group.Findings); err != nil {
				return err
			}
		}

		if len(repo.ScanErrors) > 0 {
			if _, err := fmt.Fprintln(w, "scan errors"); err != nil {
				return err
			}
			if err := writeScanErrorsTable(w, repo.ScanErrors); err != nil {
				return err
			}
		}
	}

	if !printedSection {
		_, err := fmt.Fprintln(w, "\nno issues found")
		return err
	}

	return nil
}

func visibleFindings(findings []githubscan.Finding, showOK bool) []githubscan.Finding {
	if showOK {
		return findings
	}

	out := make([]githubscan.Finding, 0, len(findings))
	for _, finding := range findings {
		if finding.Status == githubscan.StatusOK {
			continue
		}
		out = append(out, finding)
	}
	return out
}

func summarizeRepo(repo githubscan.RepositoryResult) githubscan.Summary {
	summary := githubscan.Summary{
		Repositories: 1,
		Files:        repo.FilesScanned,
		Refs:         len(repo.Findings),
		Errors:       len(repo.ScanErrors),
	}

	for _, finding := range repo.Findings {
		switch finding.Status {
		case githubscan.StatusOK:
			summary.OK++
		case githubscan.StatusUnpinned:
			summary.Unpinned++
		case githubscan.StatusInvalid:
			summary.Invalid++
		case githubscan.StatusRetry:
			summary.Retry++
		case githubscan.StatusError:
			summary.Errors++
		}
	}

	return summary
}

func partitionRepositories(repositories []githubscan.RepositoryResult) ([]githubscan.RepositoryResult, []githubscan.RepositoryResult) {
	cleanRepos := make([]githubscan.RepositoryResult, 0, len(repositories))
	issueRepos := make([]githubscan.RepositoryResult, 0, len(repositories))
	for _, repo := range repositories {
		if repoHasIssues(repo) {
			issueRepos = append(issueRepos, repo)
			continue
		}
		cleanRepos = append(cleanRepos, repo)
	}
	return cleanRepos, issueRepos
}

func repoHasIssues(repo githubscan.RepositoryResult) bool {
	summary := summarizeRepo(repo)
	return summary.Unpinned > 0 || summary.Invalid > 0 || summary.Retry > 0 || summary.Errors > 0
}

func shouldShowRepositoryDetails(mode string, repo githubscan.RepositoryResult, showOK bool) bool {
	if mode != "org" {
		return true
	}
	return showOK || repoHasIssues(repo)
}

func groupFindingsByWorkflow(findings []githubscan.Finding) []workflowGroup {
	var groups []workflowGroup
	for _, finding := range findings {
		if len(groups) == 0 || groups[len(groups)-1].Workflow != finding.Workflow {
			groups = append(groups, workflowGroup{Workflow: finding.Workflow})
		}
		groups[len(groups)-1].Findings = append(groups[len(groups)-1].Findings, finding)
	}
	return groups
}

func writeFindingsTable(w io.Writer, findings []githubscan.Finding) error {
	table := newTable(w, []string{"Status", "Action Ref", "Lines", "Details"})
	for _, finding := range findings {
		table.Append([]string{
			strings.ToUpper(string(finding.Status)),
			finding.Ref,
			formatLines(finding.Lines),
			finding.Message,
		})
	}
	table.Render()
	_, err := fmt.Fprintln(w)
	return err
}

func writeScanErrorsTable(w io.Writer, scanErrors []string) error {
	table := newTable(w, []string{"Status", "Details"})
	for _, scanErr := range scanErrors {
		table.Append([]string{"ERROR", scanErr})
	}
	table.Render()
	_, err := fmt.Fprintln(w)
	return err
}

func writeCleanRepositoryTable(w io.Writer, repositories []githubscan.RepositoryResult) error {
	table := newTable(w, []string{"Repository", "Files", "Action Refs", "OK"})
	for _, repo := range repositories {
		summary := summarizeRepo(repo)
		table.Append([]string{
			repo.Source,
			strconv.Itoa(repo.FilesScanned),
			strconv.Itoa(len(repo.Findings)),
			strconv.Itoa(summary.OK),
		})
	}
	table.Render()
	_, err := fmt.Fprintln(w)
	return err
}

func writeIssueRepositoryTable(w io.Writer, repositories []githubscan.RepositoryResult) error {
	table := newTable(w, []string{"Repository", "Files", "Action Refs", "Needs Pin", "Invalid", "Retry", "Errors"})
	for _, repo := range repositories {
		summary := summarizeRepo(repo)
		table.Append([]string{
			repo.Source,
			strconv.Itoa(repo.FilesScanned),
			strconv.Itoa(len(repo.Findings)),
			strconv.Itoa(summary.Unpinned),
			strconv.Itoa(summary.Invalid),
			strconv.Itoa(summary.Retry),
			strconv.Itoa(summary.Errors),
		})
	}
	table.Render()
	_, err := fmt.Fprintln(w)
	return err
}

func writeIssueColumnGuide(w io.Writer) error {
	_, err := fmt.Fprintln(
		w,
		"column guide: action refs=checked uses: refs, needs pin=not pinned to a tag or full commit SHA, invalid=bad or unreachable ref, retry=temporary GitHub/API problem, errors=scan or non-retry validation problem",
	)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w)
	return err
}

func newTable(w io.Writer, header []string) *tablewriter.Table {
	table := tablewriter.NewWriter(w)
	table.SetHeader(header)
	table.SetAutoWrapText(false)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	alignments := make([]int, 0, len(header))
	for range header {
		alignments = append(alignments, tablewriter.ALIGN_LEFT)
	}
	table.SetColumnAlignment(alignments)
	return table
}

func formatLines(lines []int) string {
	if len(lines) == 0 {
		return "[]"
	}

	values := make([]string, 0, len(lines))
	for _, line := range lines {
		values = append(values, strconv.Itoa(line))
	}

	return "[" + strings.Join(values, ", ") + "]"
}
