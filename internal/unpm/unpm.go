// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// Copyright (c) 2026 Jake Lazaroff https://github.com/jakelazaroff/unpm

package unpm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/jakelazaroff/unpm/internal/cfg"
)

type vendorer struct {
	config     *cfg.Config
	downloaded map[string]string // full URL -> path
	types      map[string]string // full URL -> types path (from x-typescript-types)
	esmPaths   map[string]string // x-esm-path value -> path
	warnings   []string
}

func Vendor(c *cfg.Config) ([]string, error) {
	// clean the output directory, preserving pinned files
	if _, err := clean(c, c.Unpm.Out); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return []string{}, err
	}

	v := &vendorer{
		config:     c,
		downloaded: make(map[string]string),
		types:      make(map[string]string),
		esmPaths:   make(map[string]string),
	}

	// download all imports
	imports := make(map[string]string) // import key -> relative path within out directory
	for key, rawURL := range c.Imports {
		relPath, err := v.download(rawURL)
		if err != nil {
			return v.warnings, fmt.Errorf("downloading %q: %w", key, err)
		}

		imports[key] = path.Join(c.Unpm.Root, relPath)
	}

	if err := writeImportMap(c.Unpm.Out, imports, c.Unpm.Verbose); err != nil {
		return v.warnings, err
	}

	// build types mapping: import key -> types path within out directory
	// use x-typescript-types if available, otherwise fall back to the JS file itself
	types := make(map[string]string)
	for key, rawURL := range c.Imports {
		if typesRel, ok := v.types[rawURL]; ok {
			types[key] = "./" + typesRel
		} else {
			types[key] = "./" + v.downloaded[rawURL]
		}
	}

	if err := writeTypesDts(c.Unpm.Out, types, c.Unpm.Verbose); err != nil {
		return v.warnings, err
	}

	for spec, files := range findBareImports(c.Unpm.Out, c.Imports) {
		fmt.Printf("\033[33mwarning:\033[0m %q is imported by %s but missing from import map\n", spec, strings.Join(files, ", "))
	}

	return v.warnings, nil
}

// recursively remove a directory's children while leaving pinned files,
// returning whether the cleaned directory should be deleted
func clean(config *cfg.Config, path string) (bool, error) {
	// if the file is pinned, don't clean it
	relPath, _ := filepath.Rel(config.Unpm.Out, path)
	if config.IsPinned(filepath.ToSlash(relPath)) {
		return false, nil
	}

	// if the path is a leaf file, it should be deleted
	entries, err := os.ReadDir(path)
	if err != nil {
		if errors.Is(err, syscall.ENOTDIR) {
			return true, nil
		}

		return false, err
	}

	// iterate through the directory, removing any files and now-empty child directories
	empty := true
	for _, entry := range entries {
		child := filepath.Join(path, entry.Name())
		delete, err := clean(config, child)
		if err != nil {
			return false, err
		}

		empty = empty && delete
		if delete {
			if err := os.Remove(child); err != nil {
				return false, err
			}
		}
	}

	return empty, nil
}

func Check(c *cfg.Config) error {
	var errors []string

	// 1. Error: bare module specifiers in vendored files not in the import map.
	for spec, files := range findBareImports(c.Unpm.Out, c.Imports) {
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

// findBareImports walks all vendored JS files and returns bare import specifiers
// (e.g. "preact/hooks") that are not present in the import map.
func findBareImports(outDir string, imports map[string]string) map[string][]string {
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

		for _, m := range allImportRe.FindAllStringSubmatch(string(data), -1) {
			spec := m[1]
			if spec == "" {
				spec = m[2]
			}
			if strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") || strings.Contains(spec, "://") {
				continue
			}
			if _, ok := imports[spec]; !ok {
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

		for _, m := range relImportRe.FindAllStringSubmatch(string(data), -1) {
			spec := m[1]
			if spec == "" {
				spec = m[2]
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

// relImportRe matches import/export statements and dynamic import() calls with relative path specifiers.
var relImportRe = regexp.MustCompile(`(?:\b(?:import|export)\s*(?:[^"']*\bfrom\s*|)["'](\.[^"']+)["']|\bimport\s*\(\s*["'](\.[^"']+)["']\s*\))`)

// allImportRe matches all import/export specifiers and dynamic import() calls.
var allImportRe = regexp.MustCompile(`(?:\b(?:import|export)\s*(?:[^"']*\bfrom\s*|)["']([^"']+)["']|\bimport\s*\(\s*["']([^"']+)["']\s*\))`)

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

	matches := relImportRe.FindAllStringSubmatch(string(data), -1)
	for _, m := range matches {
		depRel := m[1]
		if depRel == "" {
			depRel = m[2]
		}
		// Resolve relative to the current file's directory
		depPath := path.Join(path.Dir(relPath), depRel)
		if err := walkImports(outDir, depPath, reachable); err != nil {
			return err
		}
	}

	// Mark source maps referenced by //# sourceMappingURL=... as reachable
	for _, m := range sourceMappingRe.FindAllStringSubmatch(string(data), -1) {
		mapRel := m[2]
		if strings.HasPrefix(mapRel, "data:") {
			continue
		}
		mapPath := filepath.Clean(filepath.Join(outDir, path.Join(path.Dir(relPath), mapRel)))
		reachable[mapPath] = true
	}

	return nil
}

// importRe matches import/export statements and dynamic import() calls with origin-relative or relative path specifiers.
// For static imports: captures the statement prefix (group 1), quote char (group 2), path (group 3), closing quote (group 4).
// For dynamic imports: captures "import(" prefix (group 5), quote char (group 6), path (group 7), closing quote + ")" (group 8).
var importRe = regexp.MustCompile(`(\b(?:import|export)\s*(?:[^"']*\bfrom\s*|))(["'])((?:/|\.\.?/)[^"']+)(["'])|(\bimport\s*\(\s*)(["'])((?:/|\.\.?/)[^"']+)(["']\s*\))`)

// sourceMappingRe matches //# sourceMappingURL=... comments.
var sourceMappingRe = regexp.MustCompile(`(//[#@]\s*sourceMappingURL\s*=\s*)(\S+)`)

func (v *vendorer) download(fullURL string) (string, error) {
	// if already downloaded, no need to continue
	if relativePath, ok := v.downloaded[fullURL]; ok {
		return relativePath, nil
	}

	// fetch the url
	u, err := url.Parse(fullURL)
	if err != nil {
		return "", fmt.Errorf("parsing URL %s: %w", fullURL, err)
	}

	// download the file
	resp, err := http.Get(fullURL)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", fullURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching %s: status %d", fullURL, resp.StatusCode)
	}

	// esm.sh returns x-esm-path with the canonical resolved path;
	// skip the shim and download the resolved module directly
	if esmPath := resp.Header.Get("x-esm-path"); esmPath != "" {
		if resolved, ok := v.esmPaths[esmPath]; ok {
			v.downloaded[fullURL] = resolved
			return resolved, nil
		}

		// construct canonical URL
		canonicalURL := u.Scheme + "://" + u.Host + esmPath
		rel, err := v.download(canonicalURL)
		if err != nil {
			return "", fmt.Errorf("downloading canonical path for %s: %w", fullURL, err)
		}

		v.downloaded[fullURL] = rel
		v.esmPaths[esmPath] = rel

		// Check for type definitions before returning —
		// the x-typescript-types header is on this response, not the canonical one
		if typesURL := resp.Header.Get("x-typescript-types"); typesURL != "" {
			if strings.HasPrefix(typesURL, "/") {
				typesURL = u.Scheme + "://" + u.Host + typesURL
			}
			if typesRel, err := v.download(typesURL); err != nil {
				v.warnings = append(v.warnings, fmt.Sprintf("failed to download types for %s: %v", fullURL, err))
			} else {
				v.types[fullURL] = typesRel
			}
		}

		return rel, nil
	}

	// derive local file path from the original URL's host + path.
	// if the URL path has a recognized file extension, the filename is the basename;
	// otherwise, treat the whole path as a directory and derive the filename from the response.
	var localDir, filename string
	ext := strings.ToLower(path.Ext(u.Path))
	switch ext {
	case ".js", ".mjs", ".mts", ".ts", ".css", ".json", ".wasm", ".map":
		localDir = path.Join(u.Host, path.Dir(u.Path))
		filename = path.Base(u.Path)
	default:
		localDir = path.Join(u.Host, u.Path)

		// use the final (post-redirect) URL to determine filename
		filename = path.Base(resp.Request.URL.Path)
		if filename == "" || filename == "/" || filename == "." {
			filename = "index.js"
		}

		fext := strings.ToLower(path.Ext(filename))
		switch fext {
		case ".js", ".mjs", ".mts", ".ts", ".css", ".json", ".wasm", ".map":
			// keep as-is
		default:
			filename += ".js"
		}
	}

	// create the directory
	dir := filepath.Join(v.config.Unpm.Out, filepath.FromSlash(localDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating directory %s: %w", dir, err)
	}

	destPath := filepath.Join(dir, filename)
	rel, _ := filepath.Rel(v.config.Unpm.Out, destPath)
	rel = filepath.ToSlash(rel)

	// register before recursing to prevent cycles
	v.downloaded[fullURL] = rel

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", fullURL, err)
	}

	// if the file is pinned, skip writing to preserve local modifications
	if v.config.IsPinned(rel) {
		if v.config.Unpm.Verbose {
			fmt.Printf("%s -> %s (pinned)\n", fullURL, rel)
		}
	} else {
		content := string(body)

		// Only rewrite imports and source maps in code files, not in .map files
		if strings.ToLower(path.Ext(u.Path)) != ".map" {
			// Find and recursively download origin-relative and relative imports, rewriting paths
			content, err = v.rewriteImports(u, content, rel)
			if err != nil {
				return "", fmt.Errorf("rewriting imports in %s: %w", fullURL, err)
			}

			// Download source maps referenced by //# sourceMappingURL=...
			content = v.rewriteSourceMap(u, content, rel)
		}

		if err := os.WriteFile(destPath, []byte(content), 0o644); err != nil {
			return "", fmt.Errorf("writing %s: %w", destPath, err)
		}

		if v.config.Unpm.Verbose {
			fmt.Printf("%s -> %s\n", fullURL, rel)
		}
	}

	// If the response includes type definitions, download them too
	if typesURL := resp.Header.Get("x-typescript-types"); typesURL != "" {
		if strings.HasPrefix(typesURL, "/") {
			typesURL = u.Scheme + "://" + u.Host + typesURL
		}
		if typesRel, err := v.download(typesURL); err != nil {
			v.warnings = append(v.warnings, fmt.Sprintf("failed to download types for %s: %v", fullURL, err))
		} else {
			v.types[fullURL] = typesRel
		}
	}

	return rel, nil
}

func (v *vendorer) rewriteImports(u *url.URL, content, currentFileRel string) (string, error) {
	currentDir := path.Dir(currentFileRel)
	origin := u.Scheme + "://" + u.Host
	var rewriteErr error

	result := importRe.ReplaceAllStringFunc(content, func(match string) string {
		if rewriteErr != nil {
			return match
		}

		groups := importRe.FindStringSubmatch(match)

		// determine prefix, quote, and path from either static (groups 1-4) or dynamic (groups 5-8) branch
		var prefix, quote, impPath, suffix string
		if groups[1] != "" {
			prefix = groups[1]
			quote = groups[2]
			impPath = groups[3]
			suffix = groups[4]
		} else {
			prefix = groups[5]
			quote = groups[6]
			impPath = groups[7]
			suffix = groups[8]
		}

		var depURL string
		if strings.HasPrefix(impPath, "/") {
			// Origin-relative path
			depURL = origin + impPath
		} else {
			// Relative path — resolve against the current file's URL
			depURL = origin + path.Join(path.Dir(u.Path), impPath)
		}

		depRel, err := v.download(depURL)
		if err != nil {
			rewriteErr = err
			return match
		}

		// Compute relative path from current file's directory to the downloaded dep
		relFromHere, _ := filepath.Rel(currentDir, depRel)
		relFromHere = filepath.ToSlash(relFromHere)
		if !strings.HasPrefix(relFromHere, ".") {
			relFromHere = "./" + relFromHere
		}

		return prefix + quote + relFromHere + suffix
	})

	return result, rewriteErr
}

func (v *vendorer) rewriteSourceMap(u *url.URL, content, currentFileRel string) string {
	currentDir := path.Dir(currentFileRel)
	origin := u.Scheme + "://" + u.Host

	return sourceMappingRe.ReplaceAllStringFunc(content, func(match string) string {
		groups := sourceMappingRe.FindStringSubmatch(match)
		prefix := groups[1]
		mapPath := groups[2]

		// Skip data: URLs
		if strings.HasPrefix(mapPath, "data:") {
			return match
		}

		var mapURL string
		if strings.HasPrefix(mapPath, "/") {
			mapURL = origin + mapPath
		} else {
			mapURL = origin + path.Join(path.Dir(u.Path), mapPath)
		}

		mapRel, err := v.download(mapURL)
		if err != nil {
			v.warnings = append(v.warnings, fmt.Sprintf("failed to download source map %s: %v", mapURL, err))
			return match
		}

		relFromHere, _ := filepath.Rel(currentDir, mapRel)
		relFromHere = filepath.ToSlash(relFromHere)
		if !strings.HasPrefix(relFromHere, ".") {
			relFromHere = "./" + relFromHere
		}

		return prefix + relFromHere
	})
}

func writeImportMap(outDir string, rewritten map[string]string, verbose bool) error {
	// Build the imports object as JS key-value pairs
	var entries []string
	for key, val := range rewritten {
		entries = append(entries, fmt.Sprintf("    %q: %q", key, val))
	}
	sort.Strings(entries)

	js := fmt.Sprintf(`const importmap = document.createElement("script");
importmap.type = "importmap";
importmap.textContent = JSON.stringify({
  imports: {
%s,
  },
});
document.currentScript.after(importmap);
`, strings.Join(entries, ",\n"))

	dest := filepath.Join(outDir, "importmap.js")
	if err := os.WriteFile(dest, []byte(js), 0o644); err != nil {
		return fmt.Errorf("writing importmap.js: %w", err)
	}

	if verbose {
		fmt.Printf("wrote %s\n", dest)
	}

	// Also write importmap.json for use with Node or SSR
	jsonData, err := json.MarshalIndent(map[string]any{
		"imports": rewritten,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling importmap.json: %w", err)
	}

	jsonDest := filepath.Join(outDir, "importmap.json")
	if err := os.WriteFile(jsonDest, append(jsonData, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing importmap.json: %w", err)
	}

	if verbose {
		fmt.Printf("wrote %s\n", jsonDest)
	}
	return nil
}

func writeTypesDts(dir string, types map[string]string, verbose bool) error {
	if len(types) == 0 {
		return nil
	}

	paths := make(map[string][]string)
	for key, path := range types {
		paths[key] = []string{path}
	}

	data, err := json.MarshalIndent(map[string]any{
		"compilerOptions": map[string]any{
			"paths": paths,
		},
		"exclude": []string{"."},
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling jsconfig.json: %w", err)
	}

	dest := filepath.Join(dir, "jsconfig.json")
	if err := os.WriteFile(dest, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing jsconfig.json: %w", err)
	}

	if verbose {
		fmt.Printf("wrote %s\n", dest)
	}
	return nil
}
