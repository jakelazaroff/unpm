// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// Copyright (c) 2026 Jake Lazaroff https://github.com/jakelazaroff/unpm

package unpm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jakelazaroff/unpm/pkg/cfg"
)

type vendorer struct {
	outDir     string
	downloaded map[string]string // full URL -> path relative to outDir
	types      map[string]string // full URL -> types path relative to outDir (from x-typescript-types)
	esmPaths   map[string]string // X-ESM-Path value -> path relative to outDir
}

func Fetch(c *cfg.Config) error {
	v := &vendorer{
		outDir:     c.Unpm.Out,
		downloaded: make(map[string]string),
		types:      make(map[string]string),
		esmPaths:   make(map[string]string),
	}

	// Download all imports; relPath is relative to outDir
	downloaded := make(map[string]string) // import key -> relPath within outDir
	for key, rawURL := range c.Imports {
		if c.IsPinned(key) {
			continue
		}
		relPath, err := v.download(rawURL)
		if err != nil {
			return fmt.Errorf("downloading %q: %w", key, err)
		}
		downloaded[key] = relPath
	}

	// Compute absolute import map paths from root
	rewritten := make(map[string]string)
	for key, relPath := range downloaded {
		absPath := filepath.Join(c.Unpm.Out, filepath.FromSlash(relPath))
		fromRoot, err := filepath.Rel(c.Unpm.Root, absPath)
		if err != nil {
			return fmt.Errorf("computing relative path for %q: %w", key, err)
		}
		rewritten[key] = "/" + filepath.ToSlash(fromRoot)
	}

	if err := writeImportMap(c.Unpm.Out, rewritten); err != nil {
		return err
	}

	// Build types mapping: import key -> types path relative to outDir.
	// Use x-typescript-types if available, otherwise fall back to the JS file itself.
	typesMap := make(map[string]string)
	for key, rawURL := range c.Imports {
		if c.IsPinned(key) {
			continue
		}
		if typesRel, ok := v.types[rawURL]; ok {
			typesMap[key] = "./" + typesRel
		} else {
			typesMap[key] = "./" + downloaded[key]
		}
	}

	if err := writeTypesDts(c.Unpm.Out, typesMap); err != nil {
		return err
	}

	checkBareImports(c.Unpm.Out, c.Imports)

	return nil
}

func Check(c *cfg.Config) error {
	var errors []string

	// 1. Error: bare module specifiers in vendored files not in the import map.
	missing := map[string][]string{}
	filepath.Walk(c.Unpm.Out, func(p string, info os.FileInfo, err error) error {
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
		relPath, _ := filepath.Rel(c.Unpm.Out, p)
		relPath = filepath.ToSlash(relPath)
		for _, m := range allImportRe.FindAllStringSubmatch(string(data), -1) {
			spec := m[1]
			if spec == "" {
				spec = m[2]
			}
			if strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") || strings.Contains(spec, "://") {
				continue
			}
			if _, ok := c.Imports[spec]; !ok {
				missing[spec] = append(missing[spec], relPath)
			}
		}
		return nil
	})
	for spec, files := range missing {
		errors = append(errors, fmt.Sprintf("%q is imported by %s but missing from import map", spec, strings.Join(files, ", ")))
	}

	// 2. Error: import map entries with no corresponding file on disk.
	for key, rawURL := range c.Imports {
		relPath, err := resolveVendorPath(rawURL, c.Unpm.Out)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%q (%s): %v", key, rawURL, err))
			continue
		}
		absPath := filepath.Join(c.Unpm.Out, filepath.FromSlash(relPath))
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			errors = append(errors, fmt.Sprintf("%q: expected file %s not found on disk", key, relPath))
		}
	}

	// 3. Warning: files on disk not reachable from any import map entry.
	reachable := map[string]bool{
		filepath.Clean(filepath.Join(c.Unpm.Out, "importmap.js")):   true,
		filepath.Clean(filepath.Join(c.Unpm.Out, "importmap.json")): true,
		filepath.Clean(filepath.Join(c.Unpm.Out, "jsconfig.json")):  true,
	}
	for key, rawURL := range c.Imports {
		relPath, err := resolveVendorPath(rawURL, c.Unpm.Out)
		if err != nil {
			continue // already reported above
		}
		if err := walkImports(c.Unpm.Out, relPath, reachable); err != nil {
			fmt.Fprintf(os.Stderr, "  \033[33mwarning:\033[0m could not walk imports for %q: %v\n", key, err)
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
		fmt.Fprintf(os.Stderr, "  \033[33mwarning:\033[0m %s is not reachable from any import map entry\n", f)
	}

	if len(errors) > 0 {
		sort.Strings(errors)
		for _, e := range errors {
			fmt.Fprintf(os.Stderr, "  \033[31merror:\033[0m %s\n", e)
		}
		return fmt.Errorf("check failed with %d error(s)", len(errors))
	}

	if len(unreachable) == 0 {
		fmt.Println("  all checks passed")
	}

	return nil
}

// checkBareImports walks all vendored files and warns about bare import
// specifiers (e.g. "preact/hooks") that are not present in the import map.
func checkBareImports(outDir string, imports map[string]string) {
	missing := map[string][]string{} // bare specifier -> list of files that use it

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

	if len(missing) == 0 {
		fmt.Println("  no missing imports")
		return
	}

	for spec, files := range missing {
		fmt.Printf("  \033[33mwarning:\033[0m %q is imported by %s but missing from import map\n", spec, strings.Join(files, ", "))
	}
}

func Why(c *cfg.Config, target string) error {
	// Normalize target to a relative path within outDir
	target = filepath.ToSlash(target)
	target = strings.TrimPrefix(target, filepath.ToSlash(c.Unpm.Out)+"/")

	// BFS from each entry point to find the shortest import chain to target
	for key, rawURL := range c.Imports {
		relPath, err := resolveVendorPath(rawURL, c.Unpm.Out)
		if err != nil {
			continue
		}

		chain := findImportChain(c.Unpm.Out, relPath, target)
		if chain != nil {
			fmt.Printf("  %s", key)
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

func Prune(c *cfg.Config) error {
	// Walk the import graph from entry points to find all reachable files.
	reachable := map[string]bool{
		// Generated outputs of fetch are always reachable.
		filepath.Clean(filepath.Join(c.Unpm.Out, "importmap.js")):   true,
		filepath.Clean(filepath.Join(c.Unpm.Out, "importmap.json")): true,
		filepath.Clean(filepath.Join(c.Unpm.Out, "jsconfig.json")):  true,
	}
	for key, rawURL := range c.Imports {
		relPath, err := resolveVendorPath(rawURL, c.Unpm.Out)
		if err != nil {
			return fmt.Errorf("resolving %q: %w", key, err)
		}
		if err := walkImports(c.Unpm.Out, relPath, reachable); err != nil {
			return err
		}
	}

	// Collect all files in the vendor directory
	var toDelete []string
	err := filepath.Walk(c.Unpm.Out, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !reachable[filepath.Clean(p)] {
			toDelete = append(toDelete, p)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walking vendor directory: %w", err)
	}

	for _, p := range toDelete {
		fmt.Printf("  removing %s\n", p)
		if err := os.Remove(p); err != nil {
			return fmt.Errorf("removing %s: %w", p, err)
		}
	}

	// Remove empty directories (walk bottom-up)
	if err := removeEmptyDirs(c.Unpm.Out); err != nil {
		return fmt.Errorf("cleaning empty directories: %w", err)
	}

	if len(toDelete) == 0 {
		fmt.Println("  nothing to prune")
	}

	return nil
}

// resolveVendorPath figures out the relative vendor path for a URL by looking
// at what's on disk. For URLs with a file extension the path is deterministic;
// for others (e.g. "https://esm.sh/preact@10.19.3") the filename was chosen at
// fetch time, so we find whatever file exists in the expected directory.
func resolveVendorPath(rawURL, outDir string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parsing URL: %w", err)
	}

	ext := strings.ToLower(path.Ext(u.Path))
	switch ext {
	case ".js", ".mjs", ".mts", ".ts", ".css", ".json", ".wasm", ".map":
		return filepath.ToSlash(filepath.Join(u.Host, filepath.FromSlash(u.Path))), nil
	}

	// No file extension — look for the file in the expected directory
	dir := filepath.Join(outDir, u.Host, filepath.FromSlash(u.Path))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("reading directory %s: %w (run 'unpm fetch' first)", dir, err)
	}

	for _, e := range entries {
		if !e.IsDir() {
			rel, _ := filepath.Rel(outDir, filepath.Join(dir, e.Name()))
			return filepath.ToSlash(rel), nil
		}
	}

	return "", fmt.Errorf("no vendored file found in %s", dir)
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

	return nil
}

func removeEmptyDirs(root string) error {
	// Walk bottom-up by collecting dirs first, then checking in reverse
	var dirs []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && p != root {
			dirs = append(dirs, p)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Process deepest directories first
	for i := len(dirs) - 1; i >= 0; i-- {
		entries, err := os.ReadDir(dirs[i])
		if err != nil {
			continue
		}
		if len(entries) == 0 {
			os.Remove(dirs[i])
		}
	}
	return nil
}

// importRe matches import/export statements and dynamic import() calls with origin-relative or relative path specifiers.
// For static imports: captures the statement prefix (group 1), quote char (group 2), path (group 3), closing quote (group 4).
// For dynamic imports: captures "import(" prefix (group 5), quote char (group 6), path (group 7), closing quote + ")" (group 8).
var importRe = regexp.MustCompile(`(\b(?:import|export)\s*(?:[^"']*\bfrom\s*|))(["'])((?:/|\.\.?/)[^"']+)(["'])|(\bimport\s*\(\s*)(["'])((?:/|\.\.?/)[^"']+)(["']\s*\))`)

// sourceMappingRe matches //# sourceMappingURL=... comments.
var sourceMappingRe = regexp.MustCompile(`(//[#@]\s*sourceMappingURL\s*=\s*)(\S+)`)

func (v *vendorer) download(rawURL string) (string, error) {
	// Bare paths like "/preact@10.19.3/..." are not supported at the top level;
	// they are resolved to full URLs by rewriteImports before calling download.
	fullURL := rawURL

	// Already downloaded?
	if rel, ok := v.downloaded[fullURL]; ok {
		return rel, nil
	}

	u, err := url.Parse(fullURL)
	if err != nil {
		return "", fmt.Errorf("parsing URL %s: %w", fullURL, err)
	}

	resp, err := http.Get(fullURL)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", fullURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching %s: status %d", fullURL, resp.StatusCode)
	}

	// esm.sh returns X-ESM-Path with the canonical resolved path. Skip the
	// shim and download the resolved module directly.
	if esmPath := resp.Header.Get("X-ESM-Path"); esmPath != "" {
		if rel, ok := v.esmPaths[esmPath]; ok {
			v.downloaded[fullURL] = rel
			return rel, nil
		}
		canonicalURL := u.Scheme + "://" + u.Host + esmPath
		rel, err := v.download(canonicalURL)
		if err != nil {
			return "", fmt.Errorf("downloading canonical path for %s: %w", fullURL, err)
		}
		v.downloaded[fullURL] = rel
		v.esmPaths[esmPath] = rel
		return rel, nil
	}

	// Derive local directory from the original URL's host + path.
	// If the URL path has a recognized file extension, the filename is the basename;
	// otherwise, treat the whole path as a directory and derive the filename from the response.
	var localDir, filename string
	ext := strings.ToLower(path.Ext(u.Path))
	switch ext {
	case ".js", ".mjs", ".mts", ".ts", ".css", ".json", ".wasm", ".map":
		localDir = path.Join(u.Host, path.Dir(u.Path))
		filename = path.Base(u.Path)
	default:
		localDir = path.Join(u.Host, u.Path)
		// Use the final (post-redirect) URL to determine filename
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

	dir := filepath.Join(v.outDir, filepath.FromSlash(localDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating directory %s: %w", dir, err)
	}

	destPath := filepath.Join(dir, filename)
	rel, _ := filepath.Rel(v.outDir, destPath)
	rel = filepath.ToSlash(rel)

	// Register before recursing to prevent cycles
	v.downloaded[fullURL] = rel

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", fullURL, err)
	}

	content := string(body)

	// Only rewrite imports and source maps in code files, not in .map files
	if strings.ToLower(path.Ext(u.Path)) != ".map" {
		// Find and recursively download origin-relative and relative imports, rewriting paths
		content, err = v.rewriteImports(content, rel, fullURL)
		if err != nil {
			return "", fmt.Errorf("rewriting imports in %s: %w", fullURL, err)
		}

		// Download source maps referenced by //# sourceMappingURL=...
		content = v.rewriteSourceMap(content, rel, fullURL)
	}

	if err := os.WriteFile(destPath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("writing %s: %w", destPath, err)
	}

	fmt.Printf("  %s -> %s\n", fullURL, rel)

	// If the response includes type definitions, download them too
	if typesURL := resp.Header.Get("X-Typescript-Types"); typesURL != "" {
		if strings.HasPrefix(typesURL, "/") {
			typesURL = u.Scheme + "://" + u.Host + typesURL
		}
		if typesRel, err := v.download(typesURL); err != nil {
			fmt.Printf("  \033[33mwarning:\033[0m failed to download types for %s: %v\n", fullURL, err)
		} else {
			v.types[fullURL] = typesRel
		}
	}

	return rel, nil
}

func (v *vendorer) rewriteImports(content, currentFileRel, currentURL string) (string, error) {
	currentDir := path.Dir(currentFileRel)
	u, _ := url.Parse(currentURL)
	origin := u.Scheme + "://" + u.Host
	var rewriteErr error

	result := importRe.ReplaceAllStringFunc(content, func(match string) string {
		if rewriteErr != nil {
			return match
		}

		groups := importRe.FindStringSubmatch(match)

		// Determine prefix, quote, and path from either static (groups 1-4) or dynamic (groups 5-8) branch
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

func (v *vendorer) rewriteSourceMap(content, currentFileRel, currentURL string) string {
	currentDir := path.Dir(currentFileRel)
	u, _ := url.Parse(currentURL)
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
			fmt.Printf("  \033[33mwarning:\033[0m failed to download source map %s: %v\n", mapURL, err)
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

func writeImportMap(outDir string, rewritten map[string]string) error {
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

	fmt.Printf("  wrote %s\n", dest)

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

	fmt.Printf("  wrote %s\n", jsonDest)
	return nil
}

func writeTypesDts(outDir string, typesMap map[string]string) error {
	if len(typesMap) == 0 {
		return nil
	}

	paths := make(map[string][]string)
	for key, typesPath := range typesMap {
		paths[key] = []string{typesPath}
	}

	data, err := json.MarshalIndent(map[string]any{
		"compilerOptions": map[string]any{
			"paths": paths,
		},
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling jsconfig.json: %w", err)
	}

	dest := filepath.Join(outDir, "jsconfig.json")
	if err := os.WriteFile(dest, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing jsconfig.json: %w", err)
	}

	fmt.Printf("  wrote %s\n", dest)
	return nil
}
