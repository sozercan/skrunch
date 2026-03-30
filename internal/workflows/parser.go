package workflows

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/braydonk/yaml"
	"github.com/sethvargo/ratchet/parser"
)

type Ref struct {
	Normalized string
	Lines      []int
}

type Document struct {
	Path string
	Refs []Ref
}

func ParseBytes(path string, data []byte) (Document, error) {
	doc := Document{Path: filepath.ToSlash(path)}
	if len(bytes.TrimSpace(data)) == 0 {
		return doc, nil
	}

	node := new(yaml.Node)
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(node); err != nil {
		return Document{}, fmt.Errorf("decode YAML: %w", err)
	}

	refs, err := (&parser.Actions{}).Parse(map[string]*yaml.Node{
		doc.Path: node,
	})
	if err != nil {
		return Document{}, fmt.Errorf("parse workflow refs: %w", err)
	}

	for _, normalized := range refs.Refs() {
		nodes := refs.All()[normalized]
		lines := make([]int, 0, len(nodes))
		for _, node := range nodes {
			if node == nil || node.Line == 0 {
				continue
			}
			lines = append(lines, node.Line)
		}
		sort.Ints(lines)

		doc.Refs = append(doc.Refs, Ref{
			Normalized: normalized,
			Lines:      lines,
		})
	}

	return doc, nil
}

func ParseFile(path string) (Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Document{}, fmt.Errorf("read file: %w", err)
	}
	return ParseBytes(path, data)
}

func DiscoverLocalFiles(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", root, err)
	}

	if !info.IsDir() {
		return []string{root}, nil
	}

	var targeted []string
	var allYAML []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}

		if !hasYAMLExt(path) {
			return nil
		}

		allYAML = append(allYAML, path)
		if isLikelyWorkflowPath(path) {
			targeted = append(targeted, path)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}

	sort.Strings(targeted)
	sort.Strings(allYAML)

	if len(targeted) > 0 {
		return targeted, nil
	}
	return allYAML, nil
}

func hasYAMLExt(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yml" || ext == ".yaml"
}

func isLikelyWorkflowPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if base == "action.yml" || base == "action.yaml" {
		return true
	}

	slashed := filepath.ToSlash(path)
	return strings.Contains(slashed, "/.github/workflows/")
}
