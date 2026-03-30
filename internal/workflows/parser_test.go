package workflows

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestParseBytesCollectsActionRefsAndLineNumbers(t *testing.T) {
	path := filepath.Join("nested", "workflow.yml")

	doc, err := ParseBytes(path, []byte(`name: CI
on:
  push:
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/checkout@v4
      - uses: docker/login-action@v3
`))
	if err != nil {
		t.Fatalf("ParseBytes() error = %v", err)
	}

	if got, want := doc.Path, "nested/workflow.yml"; got != want {
		t.Fatalf("ParseBytes() path = %q, want %q", got, want)
	}

	got := make(map[string][]int, len(doc.Refs))
	for _, ref := range doc.Refs {
		got[ref.Normalized] = ref.Lines
	}

	want := map[string][]int{
		"actions://actions/checkout@v4":    {8, 9},
		"actions://docker/login-action@v3": {10},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseBytes() refs = %#v, want %#v", got, want)
	}
}

func TestParseBytesEmptyDocumentReturnsNoRefs(t *testing.T) {
	path := filepath.Join("nested", "empty.yml")

	doc, err := ParseBytes(path, []byte(" \n\t"))
	if err != nil {
		t.Fatalf("ParseBytes() error = %v", err)
	}

	if got, want := doc.Path, "nested/empty.yml"; got != want {
		t.Fatalf("ParseBytes() path = %q, want %q", got, want)
	}
	if len(doc.Refs) != 0 {
		t.Fatalf("ParseBytes() refs = %#v, want no refs", doc.Refs)
	}
}

func TestDiscoverLocalFilesPrefersWorkflowAndActionFiles(t *testing.T) {
	root := t.TempDir()

	workflowFile := filepath.Join(root, ".github", "workflows", "ci.yml")
	actionFile := filepath.Join(root, "action.yml")
	otherYAML := filepath.Join(root, "docs", "notes.yaml")

	writeWorkflowTestFile(t, workflowFile, "name: CI\n")
	writeWorkflowTestFile(t, actionFile, "name: setup\n")
	writeWorkflowTestFile(t, otherYAML, "name: notes\n")

	files, err := DiscoverLocalFiles(root)
	if err != nil {
		t.Fatalf("DiscoverLocalFiles() error = %v", err)
	}

	want := []string{workflowFile, actionFile}
	sort.Strings(want)
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("DiscoverLocalFiles() = %#v, want %#v", files, want)
	}
}

func TestDiscoverLocalFilesFallsBackToAllYAMLAndSkipsGit(t *testing.T) {
	root := t.TempDir()

	first := filepath.Join(root, "docs", "config.yaml")
	second := filepath.Join(root, "nested", "values.yml")
	ignored := filepath.Join(root, ".git", "ignored.yml")

	writeWorkflowTestFile(t, first, "name: config\n")
	writeWorkflowTestFile(t, second, "name: values\n")
	writeWorkflowTestFile(t, ignored, "name: ignored\n")

	files, err := DiscoverLocalFiles(root)
	if err != nil {
		t.Fatalf("DiscoverLocalFiles() error = %v", err)
	}

	want := []string{first, second}
	sort.Strings(want)
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("DiscoverLocalFiles() = %#v, want %#v", files, want)
	}
}

func TestDiscoverLocalFilesReturnsRootFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "workflow.yaml")
	writeWorkflowTestFile(t, file, "name: workflow\n")

	files, err := DiscoverLocalFiles(file)
	if err != nil {
		t.Fatalf("DiscoverLocalFiles() error = %v", err)
	}

	want := []string{file}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("DiscoverLocalFiles() = %#v, want %#v", files, want)
	}
}

func writeWorkflowTestFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
