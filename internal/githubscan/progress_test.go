package githubscan

import "testing"

func TestProgressIndicatorSnapshotRepo(t *testing.T) {
	indicator := &progressIndicator{
		mode:      progressModeRepo,
		label:     "octo/repo",
		fileDone:  2,
		fileTotal: 5,
	}

	if got, want := indicator.snapshotLocked("|"), "| scanning octo/repo: files 2/5 complete"; got != want {
		t.Fatalf("snapshotLocked() = %q, want %q", got, want)
	}
}

func TestProgressIndicatorSnapshotRepoBeforeDiscovery(t *testing.T) {
	indicator := &progressIndicator{
		mode:  progressModeRepo,
		label: "octo/repo",
	}

	if got, want := indicator.snapshotLocked("|"), "| scanning octo/repo: discovering workflow files"; got != want {
		t.Fatalf("snapshotLocked() = %q, want %q", got, want)
	}
}

func TestProgressIndicatorSnapshotOrg(t *testing.T) {
	indicator := &progressIndicator{
		mode:       progressModeOrg,
		label:      "octo-org",
		repoDone:   3,
		repoTotal:  10,
		repoActive: 2,
		fileDone:   8,
		fileTotal:  17,
	}

	if got, want := indicator.snapshotLocked("|"), "| scanning octo-org: repos 3/10 complete, files 8/17 complete, active 2"; got != want {
		t.Fatalf("snapshotLocked() = %q, want %q", got, want)
	}
}

func TestProgressIndicatorSnapshotOrgBeforeListing(t *testing.T) {
	indicator := &progressIndicator{
		mode:  progressModeOrg,
		label: "octo-org",
	}

	if got, want := indicator.snapshotLocked("|"), "| scanning octo-org: listing repositories"; got != want {
		t.Fatalf("snapshotLocked() = %q, want %q", got, want)
	}
}

func TestProgressIndicatorFinalSnapshotOrgWithoutFiles(t *testing.T) {
	indicator := &progressIndicator{
		mode:      progressModeOrg,
		label:     "octo-org",
		repoDone:  4,
		repoTotal: 4,
	}

	if got, want := indicator.finalSnapshot(), "scanned octo-org: repos 4/4 complete"; got != want {
		t.Fatalf("finalSnapshot() = %q, want %q", got, want)
	}
}
