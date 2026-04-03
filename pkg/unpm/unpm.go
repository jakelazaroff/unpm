// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// Copyright (c) 2026 Jake Lazaroff

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
)

type Config struct {
	Imports map[string]string `json:"imports"`
	Unpm    *Options          `json:"$unpm,omitempty"`
}

type Options struct {
	Out  string `json:"out,omitempty"`
	Root string `json:"root,omitempty"`
}

type vendorer struct {
	outDir     string
	downloaded map[string]string // full URL -> path relative to outDir
	types      map[string]string // full URL -> types path relative to outDir (from x-typescript-types)
}

func ReadConfig(configPath string) (*Config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if len(cfg.Imports) == 0 {
		return nil, fmt.Errorf("no imports found in %s", configPath)
	}

	for key, url := range cfg.Imports {
		if strings.HasSuffix(key, "/") {
			return nil, fmt.Errorf("trailing-slash prefix entries are not supported: %q (list each subpath explicitly)", key)
		}
		if !strings.HasPrefix(url, "https://") {
			return nil, fmt.Errorf("URL for %q must be an https:// URL, got: %s", key, url)
		}
	}

	return &cfg, nil
}

// OutDir returns the output directory. The flag takes precedence if set;
// otherwise the config's "out" field is used (resolved relative to configPath);
// otherwise the default is returned.
func OutDir(cfg *Config, configPath, flagVal, flagDefault string) string {
	if flagVal != flagDefault {
		return flagVal
	}
	if cfg.Unpm != nil && cfg.Unpm.Out != "" {
		return filepath.Join(filepath.Dir(configPath), cfg.Unpm.Out)
	}
	return flagDefault
}

// Root returns the root directory for import map paths. The flag takes precedence
// if set; otherwise the config's "root" field is used (resolved relative to
// configPath); otherwise the default (parent of outDir) is returned.
func Root(cfg *Config, configPath, flagVal, flagDefault string) string {
	if flagVal != flagDefault {
		return flagVal
	}
	if cfg.Unpm != nil && cfg.Unpm.Root != "" {
		return filepath.Join(filepath.Dir(configPath), cfg.Unpm.Root)
	}
	return flagDefault
}

func Fetch(cfg *Config, outDir, root string) error {
	v := &vendorer{
		outDir:     outDir,
		downloaded: make(map[string]string),
		types:      make(map[string]string),
	}

	// Download all imports; relPath is relative to outDir
	downloaded := make(map[string]string) // import key -> relPath within outDir
	for key, url := range cfg.Imports {
		relPath, err := v.download(url)
		if err != nil {
			return fmt.Errorf("downloading %q: %w", key, err)
		}
		downloaded[key] = relPath
	}

	// Compute import map paths relative to root
	rewritten := make(map[string]string)
	for key, relPath := range downloaded {
		absPath := filepath.Join(outDir, filepath.FromSlash(relPath))
		fromRoot, err := filepath.Rel(root, absPath)
		if err != nil {
			return fmt.Errorf("computing relative path for %q: %w", key, err)
		}
		rewritten[key] = "./" + filepath.ToSlash(fromRoot)
	}

	if err := writeImportMap(outDir, rewritten); err != nil {
		return err
	}

	// Build types mapping: import key -> types path relative to outDir.
	// Use x-typescript-types if available, otherwise fall back to the JS file itself.
	typesMap := make(map[string]string)
	for key, url := range cfg.Imports {
		if typesRel, ok := v.types[url]; ok {
			typesMap[key] = "./" + typesRel
		} else {
			typesMap[key] = "./" + downloaded[key]
		}
	}

	if err := writeTypesDts(outDir, typesMap); err != nil {
		return err
	}

	checkBareImports(outDir, cfg.Imports)

	return nil
}

func Check(cfg *Config, outDir string) error {
	checkBareImports(outDir, cfg.Imports)
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
		fmt.Printf("  warning: %q is imported by %s but missing from import map\n", spec, strings.Join(files, ", "))
	}
}

func Why(cfg *Config, outDir, target string) error {
	// Normalize target to a relative path within outDir
	target = filepath.ToSlash(target)
	target = strings.TrimPrefix(target, filepath.ToSlash(outDir)+"/")

	// BFS from each entry point to find the shortest import chain to target
	for key, rawURL := range cfg.Imports {
		relPath, err := resolveVendorPath(rawURL, outDir)
		if err != nil {
			continue
		}

		chain := findImportChain(outDir, relPath, target)
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
			depPath := path.Join(path.Dir(cur.relPath), m[1])
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

// relImportRe matches import/export statements with relative path specifiers.
var relImportRe = regexp.MustCompile(`\b(?:import|export)\s*(?:[^"']*\bfrom\s*|)["'](\.[^"']+)["']`)

// allImportRe matches all import/export specifiers.
var allImportRe = regexp.MustCompile(`\b(?:import|export)\s*(?:[^"']*\bfrom\s*|)["']([^"']+)["']`)

func Prune(cfg *Config, outDir string) error {
	// Walk the import graph from entry points to find all reachable files.
	reachable := map[string]bool{
		// Generated outputs of fetch are always reachable.
		filepath.Clean(filepath.Join(outDir, "importmap.js")):   true,
		filepath.Clean(filepath.Join(outDir, "importmap.json")): true,
		filepath.Clean(filepath.Join(outDir, "jsconfig.json")):  true,
	}
	for key, rawURL := range cfg.Imports {
		relPath, err := resolveVendorPath(rawURL, outDir)
		if err != nil {
			return fmt.Errorf("resolving %q: %w", key, err)
		}
		if err := walkImports(outDir, relPath, reachable); err != nil {
			return err
		}
	}

	// Collect all files in the vendor directory
	var toDelete []string
	err := filepath.Walk(outDir, func(p string, info os.FileInfo, err error) error {
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
	if err := removeEmptyDirs(outDir); err != nil {
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
	case ".js", ".mjs", ".mts", ".ts", ".css", ".json", ".wasm":
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

// importRe matches import/export statements with origin-relative or relative path specifiers.
// Captures the full statement prefix (group 1), the quote char (group 2), and the path (group 3).
var importRe = regexp.MustCompile(`(\b(?:import|export)\s*(?:[^"']*\bfrom\s*|))(["'])((?:/|\.\.?/)[^"']+)(["'])`)

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

	// Derive local directory from the original URL's host + path.
	// If the URL path has a recognized file extension, the filename is the basename;
	// otherwise, treat the whole path as a directory and derive the filename from the response.
	var localDir, filename string
	ext := strings.ToLower(path.Ext(u.Path))
	switch ext {
	case ".js", ".mjs", ".mts", ".ts", ".css", ".json", ".wasm":
		localDir = path.Join(u.Host, path.Dir(u.Path))
		filename = path.Base(u.Path)
	default:
		localDir = path.Join(u.Host, u.Path)
		// Use the final (post-redirect) URL to determine filename
		finalURL := resp.Request.URL
		filename = path.Base(finalURL.Path)
		if filename == "" || filename == "/" || filename == "." {
			filename = "index.js"
		}
		fext := strings.ToLower(path.Ext(filename))
		switch fext {
		case ".js", ".mjs", ".mts", ".ts", ".css", ".json", ".wasm":
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

	// Find and recursively download origin-relative and relative imports, rewriting paths
	content := string(body)
	content, err = v.rewriteImports(content, rel, fullURL)
	if err != nil {
		return "", fmt.Errorf("rewriting imports in %s: %w", fullURL, err)
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
			fmt.Printf("  warning: failed to download types for %s: %v\n", fullURL, err)
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
		prefix := groups[1]  // e.g. `export * from `
		quote := groups[2]   // opening quote
		impPath := groups[3] // e.g. `/preact@10.19.3/es2022/preact.mjs` or `./jsx.d.ts`

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

		return prefix + quote + relFromHere + quote
	})

	return result, rewriteErr
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
