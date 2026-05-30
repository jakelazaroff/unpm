// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// Copyright (c) 2026 Jake Lazaroff https://github.com/jakelazaroff/unpm

// Package inspect analyzes an already-vendored output tree: it powers the
// `check` and `why` commands by walking the relative-import graph that the
// vendoring engine writes to disk. It reads files and importmap.json; it never
// fetches anything.
package inspect

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jakelazaroff/unpm/internal/cfg"
	"github.com/jakelazaroff/unpm/internal/imports"
)

func Check(c *cfg.Config) error {
	var errors []string

	// 1. Error: bare module specifiers in vendored files not in the import map.
	for spec, files := range FindBareImports(c.Unpm.Out, c.Imports) {
		errors = append(errors, fmt.Sprintf("%q is imported by %s but missing from import map", spec, strings.Join(files, ", ")))
	}

	// 2. Error: import map entries with no corresponding file on disk.
	// 3. Warning: files on disk not reachable from any import map entry.
	entryPoints, err := readEntryPoints(c)
	if err != nil {
		return err
	}
	for key := range c.Imports {
		relPath, ok := entryPoints[key]
		if !ok {
			errors = append(errors, fmt.Sprintf("%q: not found in importmap.json (run 'unpm vendor')", key))
			continue
		}
		absPath := filepath.Join(c.Unpm.Out, filepath.FromSlash(relPath))
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			errors = append(errors, fmt.Sprintf("%q: expected file %s not found on disk", key, relPath))
		}
	}

	reachable := map[string]bool{
		filepath.Clean(filepath.Join(c.Unpm.Out, "importmap.js")):   true,
		filepath.Clean(filepath.Join(c.Unpm.Out, "importmap.json")): true,
		filepath.Clean(filepath.Join(c.Unpm.Out, "jsconfig.json")):  true,
	}
	for key, relPath := range entryPoints {
		if err := walkImports(c.Unpm.Out, relPath, reachable); err != nil {
			fmt.Fprintf(os.Stderr, "\033[33mwarning:\033[0m could not walk imports for %q: %v\n", key, err)
		}
	}
	var unreachable []string
	filepath.Walk(c.Unpm.Out, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !reachable[filepath.Clean(p)] {
			rel, _ := filepath.Rel(c.Unpm.Out, p)
			unreachable = append(unreachable, filepath.ToSlash(rel))
		}
		return nil
	})
	for _, f := range unreachable {
		fmt.Fprintf(os.Stderr, "\033[33mwarning:\033[0m %s is not reachable from any import map entry\n", f)
	}

	if len(errors) > 0 {
		sort.Strings(errors)
		for _, e := range errors {
			fmt.Fprintf(os.Stderr, "\033[31merror:\033[0m %s\n", e)
		}
		return fmt.Errorf("check failed with %d error(s)", len(errors))
	}

	if len(unreachable) == 0 {
		fmt.Println("all checks passed")
	}

	return nil
}

// FindBareImports walks all vendored JS files and returns bare import specifiers
// (e.g. "preact/hooks") that are not present in the import map.
func FindBareImports(outDir string, importMap map[string]string) map[string][]string {
	missing := map[string][]string{}

	filepath.Walk(outDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(p))
		if ext != ".js" && ext != ".mjs" {
			return nil
		}

		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}

		relPath, _ := filepath.Rel(outDir, p)
		relPath = filepath.ToSlash(relPath)

		for _, spec := range imports.Scan(string(data)) {
			if strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") || strings.Contains(spec, "://") {
				continue
			}
			if _, ok := importMap[spec]; !ok {
				missing[spec] = append(missing[spec], relPath)
			}
		}
		return nil
	})

	return missing
}

func Why(c *cfg.Config, target string) error {
	// Normalize target to a relative path within outDir
	target = filepath.ToSlash(target)
	target = strings.TrimPrefix(target, filepath.ToSlash(c.Unpm.Out)+"/")

	entryPoints, err := readEntryPoints(c)
	if err != nil {
		return err
	}

	// BFS from each entry point to find the shortest import chain to target
	for key, relPath := range entryPoints {

		chain := findImportChain(c.Unpm.Out, relPath, target)
		if chain != nil {
			fmt.Printf("%s", key)
			for _, link := range chain {
				fmt.Printf(" -> %s", link)
			}
			fmt.Println()
			return nil
		}
	}

	return fmt.Errorf("%s is not imported by any entry point", target)
}

// findImportChain does a BFS from start to find target, returning the chain of files.
func findImportChain(outDir, start, target string) []string {
	type node struct {
		relPath string
		chain   []string
	}

	visited := map[string]bool{}
	queue := []node{{relPath: start, chain: []string{start}}}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		if cur.relPath == target {
			return cur.chain
		}

		absPath := filepath.Clean(filepath.Join(outDir, cur.relPath))
		if visited[absPath] {
			continue
		}
		visited[absPath] = true

		data, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}

		for _, spec := range imports.Scan(string(data)) {
			if !strings.HasPrefix(spec, ".") {
				continue
			}
			depPath := path.Join(path.Dir(cur.relPath), spec)
			if !visited[filepath.Clean(filepath.Join(outDir, depPath))] {
				queue = append(queue, node{
					relPath: depPath,
					chain:   append(append([]string{}, cur.chain...), depPath),
				})
			}
		}
	}

	return nil
}

// readEntryPoints reads importmap.json from the vendor directory and returns a
// map of import key -> relative path within outDir. Since vendor rewrites all
// imports to relative paths, walking from these entry points is sufficient to
// reach every vendored file.
func readEntryPoints(c *cfg.Config) (map[string]string, error) {
	outDir := c.Unpm.Out
	data, err := os.ReadFile(filepath.Join(outDir, "importmap.json"))
	if err != nil {
		return nil, fmt.Errorf("reading importmap.json: %w (run 'unpm vendor' first)", err)
	}

	var im struct {
		Imports map[string]string `json:"imports"`
	}
	if err := json.Unmarshal(data, &im); err != nil {
		return nil, fmt.Errorf("parsing importmap.json: %w", err)
	}

	// Convert absolute URL paths (e.g. "/vendor/esm.sh/...") to paths relative to outDir
	// by stripping the root URL prefix
	root := c.Unpm.Root
	if !strings.HasSuffix(root, "/") {
		root += "/"
	}
	result := make(map[string]string)
	for key, urlPath := range im.Imports {
		rel := strings.TrimPrefix(urlPath, root)
		result[key] = rel
	}

	return result, nil
}

// walkImports recursively follows relative imports from a file, adding each to the reachable set.
func walkImports(outDir, relPath string, reachable map[string]bool) error {
	absPath := filepath.Clean(filepath.Join(outDir, relPath))
	if reachable[absPath] {
		return nil
	}
	reachable[absPath] = true

	data, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", absPath, err)
	}

	for _, spec := range imports.Scan(string(data)) {
		if !strings.HasPrefix(spec, ".") {
			continue
		}
		// Resolve relative to the current file's directory
		depPath := path.Join(path.Dir(relPath), spec)
		if err := walkImports(outDir, depPath, reachable); err != nil {
			return err
		}
	}

	// Mark source maps referenced by //# sourceMappingURL=... as reachable
	for _, mapRel := range imports.ScanSourceMaps(string(data)) {
		mapPath := filepath.Clean(filepath.Join(outDir, path.Join(path.Dir(relPath), mapRel)))
		reachable[mapPath] = true
	}

	return nil
}
