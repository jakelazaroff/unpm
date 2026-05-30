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
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/jakelazaroff/unpm/internal/cfg"
)

type vendorer struct {
	config   *cfg.Config
	fetched  map[string]*fetched // canonical URL -> fetched file
	aliases  map[string]string   // requested URL -> canonical URL
	misses   map[string]error    // requested URL -> error, to skip retrying known-bad URLs
	warnings []string
}

// errSkip signals that a URL was reachable but served content we don't vendor
// (typically text/html from a wrong URL). Callers should treat it as a soft
// skip rather than a hard failure.
var errSkip = errors.New("unsupported response")

// fetched is the cached result of pass 1 for a single URL. The fields above the
// blank line describe the runtime artifact written to disk; the ones below point
// at where the file's types live (or, for transpiled TS, carry the original
// source so it can be written back out).
type fetched struct {
	url       *url.URL
	content   []byte
	filename  string
	vendorRel string // path on disk relative to v.config.Unpm.Out (host/dir/filename)
	loader    api.Loader
	deps      map[string]string // import spec as written in source -> canonical dep URL
	sourceMap string            // canonical URL of referenced source map, or ""

	typesURL string         // canonical URL of x-typescript-types, or ""
	sidecar  string         // canonical URL of sidecar .d.ts found alongside, or ""
	types    *typesArtifact // original TS source, set when the runtime artifact was transpiled to JS
}

// typesArtifact is the original .ts/.tsx source of a file that was transpiled to
// JS for the runtime. It is written to disk alongside the generated JS so that
// TypeScript resolves the real source for type-checking rather than the emitted
// JavaScript.
type typesArtifact struct {
	content []byte
	rel     string // vendor path for the original source
}

// isScript reports whether the file is a script esbuild can parse (as opposed to
// a .d.ts declaration, source map, or other non-script asset).
func (f *fetched) isScript() bool {
	return f.loader != api.LoaderNone
}

func Vendor(c *cfg.Config) ([]string, error) {
	// clean the output directory, preserving pinned files
	if _, err := clean(c, c.Unpm.Out); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return []string{}, err
	}

	v := &vendorer{
		config:  c,
		fetched: make(map[string]*fetched),
		aliases: make(map[string]string),
		misses:  make(map[string]error),
	}

	// pass 1: fetch every reachable URL and discover dep edges in memory
	entries := make(map[string]string) // import key -> canonical entry URL
	for key, rawURL := range c.Imports {
		canon, err := v.fetch(rawURL)
		if err != nil {
			if errors.Is(err, errSkip) {
				continue
			}
			return v.warnings, fmt.Errorf("fetching %q: %w", key, err)
		}
		entries[key] = canon
	}

	// pass 2: write each fetched file to disk at host/dir/filename, rewriting
	// imports to relative paths between vendor locations
	for canon := range v.fetched {
		if err := v.writeOne(canon); err != nil {
			return v.warnings, fmt.Errorf("writing %s: %w", canon, err)
		}
	}

	importMap := make(map[string]string) // import key -> URL path within out directory
	typesMap := make(map[string]string)  // import key -> vendor-relative types path
	for key, entry := range entries {
		f := v.fetched[entry]
		if f == nil {
			continue
		}
		importMap[key] = path.Join(c.Unpm.Root, f.vendorRel)

		// types: x-typescript-types > sidecar .d.ts > original .ts source > entry file itself
		typesCanon := entry
		switch {
		case f.typesURL != "":
			typesCanon = f.typesURL
		case f.sidecar != "":
			typesCanon = f.sidecar
		}
		if tf, ok := v.fetched[typesCanon]; ok && typesCanon != entry {
			typesMap[key] = "./" + tf.vendorRel
		} else if f.types != nil {
			typesMap[key] = "./" + f.types.rel
		} else {
			typesMap[key] = "./" + f.vendorRel
		}
	}

	if err := writeImportMap(c.Unpm.Out, importMap, c.Unpm.Verbose); err != nil {
		return v.warnings, err
	}

	if err := writeTypesDts(c.Unpm.Out, typesMap, c.Unpm.Verbose); err != nil {
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
var importRe = regexp.MustCompile(`(\b(?:import|export)\s*(?:[^"']*\bfrom\s*|))(["'])((?:[a-zA-Z]+://|/|\.\.?/)[^"']+)(["'])|(\bimport\s*\(\s*)(["'])((?:[a-zA-Z]+://|/|\.\.?/)[^"']+)(["']\s*\))`)

// typeImportRe matches `import type ... from "spec"` and `export type ... from "spec"`
// statements. esbuild elides these from its output before OnResolve runs, so we
// can't rely on the parser to discover the deps — we have to find them ourselves.
var typeImportRe = regexp.MustCompile(`(?m)(?:^|[\s;])(?:import|export)\s+type\b[^"';\n]*?["']([^"']+)["']`)

// sourceMappingRe matches //# sourceMappingURL=... comments.
var sourceMappingRe = regexp.MustCompile(`(//[#@]\s*sourceMappingURL\s*=\s*)(\S+)`)

// fetch downloads a URL, follows x-esm-path shims, caches the bytes in memory,
// and recursively enumerates the URLs referenced by the file. It returns the
// canonical URL that identifies the file in v.fetched.
func (v *vendorer) fetch(rawURL string) (canon string, err error) {
	if canon, ok := v.aliases[rawURL]; ok {
		return canon, nil
	}
	if cached, ok := v.misses[rawURL]; ok {
		return "", cached
	}
	defer func() {
		if err != nil {
			v.misses[rawURL] = err
		}
	}()

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parsing URL %s: %w", rawURL, err)
	}

	resp, err := http.Get(rawURL)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching %s: status %d", rawURL, resp.StatusCode)
	}

	if ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); ct == "text/html" || ct == "application/xhtml+xml" {
		v.warnings = append(v.warnings, fmt.Sprintf("skipping %s: unexpected content-type %q", rawURL, ct))
		return "", errSkip
	}

	// esm.sh returns x-esm-path with the canonical resolved path;
	// skip the shim and fetch the resolved module directly
	if esmPath := resp.Header.Get("x-esm-path"); esmPath != "" {
		canonURL := u.Scheme + "://" + u.Host + esmPath
		v.aliases[rawURL] = canonURL

		canon, err := v.fetch(canonURL)
		if err != nil {
			return "", fmt.Errorf("downloading canonical path for %s: %w", rawURL, err)
		}
		if canon != canonURL {
			v.aliases[rawURL] = canon
		}

		// x-typescript-types is on this response, not the canonical one
		v.attachTypesHeader(u, resp, rawURL, v.fetched[canon])

		return canon, nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", rawURL, err)
	}

	// derive local file path from the original URL's host + path.
	// if the URL path has a recognized file extension, the filename is the basename;
	// otherwise, treat the whole path as a directory and derive the filename from the response.
	var localDir, filename string
	ext := strings.ToLower(path.Ext(u.Path))
	switch ext {
	case ".js", ".mjs", ".jsx", ".ts", ".tsx", ".mts", ".css", ".json", ".wasm", ".map":
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
		case ".js", ".mjs", ".jsx", ".ts", ".tsx", ".mts", ".css", ".json", ".wasm", ".map":
			// keep as-is
		default:
			filename += ".js"
		}
	}

	canon = u.String()
	v.aliases[rawURL] = canon

	f := &fetched{
		url:       u,
		content:   body,
		filename:  filename,
		vendorRel: path.Join(localDir, filename),
		loader:    loaderFor(filename),
		deps:      make(map[string]string),
	}
	v.fetched[canon] = f

	if err := v.discover(u, f); err != nil {
		return "", fmt.Errorf("discovering imports in %s: %w", rawURL, err)
	}

	v.attachTypesHeader(u, resp, rawURL, f)

	// try a "sidecar" .d.ts at the same URL with the extension replaced
	// (e.g. foo.mjs -> foo.d.ts). 404s are expected and silent.
	if sURL := sidecarURL(u, f); sURL != "" {
		if scanon, err := v.fetch(sURL); err == nil {
			f.sidecar = scanon
		}
	}

	if err := v.transpile(f); err != nil {
		return "", fmt.Errorf("transpiling %s: %w", rawURL, err)
	}

	return canon, nil
}

// discover enumerates the URLs a fetched file references — its imports and any
// source map — fetching each and recording the edges on f. Routing is internal:
// script files go through esbuild's parser (plus a regex pass for the
// `import type` statements esbuild elides), .d.ts and other text files use a
// regex, and source maps reference nothing of their own.
func (v *vendorer) discover(u *url.URL, f *fetched) error {
	switch {
	case f.isScript():
		if err := v.discoverESBuild(u, f); err != nil {
			return err
		}
		if f.loader == api.LoaderTS || f.loader == api.LoaderTSX {
			v.discoverTypeImports(u, f)
		}
	case isSourceMap(f.filename):
		return nil
	default:
		v.discoverRegex(u, f)
	}

	v.discoverSourceMap(u, f)
	return nil
}

// discoverSourceMap follows a //# sourceMappingURL= comment, fetching the
// referenced map and recording it on f.sourceMap. Inline data: URLs are skipped.
func (v *vendorer) discoverSourceMap(u *url.URL, f *fetched) {
	for _, m := range sourceMappingRe.FindAllStringSubmatch(string(f.content), -1) {
		mapPath := m[2]
		if strings.HasPrefix(mapPath, "data:") {
			continue
		}
		ref, err := url.Parse(mapPath)
		if err != nil {
			continue
		}
		mapURL := u.ResolveReference(ref).String()
		if smCanon, err := v.fetch(mapURL); err != nil {
			v.warnings = append(v.warnings, fmt.Sprintf("failed to download source map %s: %v", mapURL, err))
		} else {
			f.sourceMap = smCanon
		}
	}
}

// transpile converts a TS/TSX runtime artifact to JS in place, updating
// f.filename, f.vendorRel, f.content and f.loader. The original source is stashed
// on f.types so writeOne can emit it alongside the generated JS for TypeScript to
// type-check against. Non-TS files are left untouched.
func (v *vendorer) transpile(f *fetched) error {
	if f.loader != api.LoaderTS && f.loader != api.LoaderTSX {
		return nil
	}

	res := api.Transform(string(f.content), api.TransformOptions{
		Loader:     f.loader,
		Sourcefile: f.url.String(),
	})
	if len(res.Errors) > 0 {
		return errors.New(res.Errors[0].Text)
	}

	f.types = &typesArtifact{content: f.content, rel: f.vendorRel}

	origExt := path.Ext(f.filename)
	newExt := ".js"
	if strings.EqualFold(origExt, ".mts") {
		newExt = ".mjs"
	}
	f.filename = strings.TrimSuffix(f.filename, origExt) + newExt
	f.vendorRel = path.Join(path.Dir(f.vendorRel), f.filename)
	f.content = res.Code
	f.loader = api.LoaderJS

	return nil
}

// isSourceMap reports whether filename is a .map sidecar.
func isSourceMap(filename string) bool {
	return strings.HasSuffix(strings.ToLower(filename), ".map")
}

// resolveAndFetch fetches depURL, falling back to extension-search candidates
// when the importing file uses bundler-style or TypeScript-style resolution.
// For example, `import x from "./render"` in a .js file should match render.js;
// the same spec in a .ts file should also try render.ts / render.d.ts first.
// Failures are silent except for the last error returned.
func (v *vendorer) resolveAndFetch(depURL string, parentLoader api.Loader) (string, error) {
	if canon, ok := v.aliases[depURL]; ok {
		return canon, nil
	}

	canon, err := v.fetch(depURL)
	if err == nil {
		return canon, nil
	}

	candidates := resolveCandidates(depURL, parentLoader)
	if len(candidates) == 0 {
		return "", err
	}

	sawSkip := errors.Is(err, errSkip)
	for _, candidate := range candidates {
		c, e := v.fetch(candidate)
		if e == nil {
			v.aliases[depURL] = c
			return c, nil
		}
		if errors.Is(e, errSkip) {
			sawSkip = true
		}
	}
	if sawSkip {
		return "", errSkip
	}
	return "", err
}

// candidateExts is the full matrix of extension-search fallbacks, keyed by the
// importing file's loader and then by the extension of the import as written.
// Each value is the list of suffixes to try in place of that extension (the
// extension is trimmed off first, so "" means an extensionless import that gets
// bundler-style and /index.* expansion). The TS and TSX loaders share tsExts.
var (
	tsExts = map[string][]string{
		".js":  {".ts", ".tsx", ".d.ts"},
		".mjs": {".mts", ".d.mts"},
		".jsx": {".tsx"},
		"":     {".ts", ".tsx", ".d.ts", "/index.ts", "/index.tsx", "/index.d.ts", ".js", ".mjs", "/index.js", "/index.mjs"},
	}
	candidateExts = map[api.Loader]map[string][]string{
		api.LoaderTS:  tsExts,
		api.LoaderTSX: tsExts,
		api.LoaderJS: {
			"": {".js", ".mjs", "/index.js", "/index.mjs"},
		},
	}
)

// resolveCandidates returns the URLs to try for an extension-less or
// TS-source-style import that couldn't be fetched as-is, per the candidateExts
// matrix for the importing file's loader.
func resolveCandidates(depURL string, parentLoader api.Loader) []string {
	u, err := url.Parse(depURL)
	if err != nil {
		return nil
	}
	ext := strings.ToLower(path.Ext(u.Path))
	suffixes := candidateExts[parentLoader][ext]
	if len(suffixes) == 0 {
		return nil
	}

	base := strings.TrimSuffix(u.Path, ext)
	out := make([]string, 0, len(suffixes))
	for _, s := range suffixes {
		nu := *u
		nu.Path = base + s
		out = append(out, nu.String())
	}
	return out
}

// resolveSpec returns the absolute URL for an import spec resolved against the
// importing file's URL. Bare specifiers (no scheme, no leading "/", "./", or
// "../") return "" — callers should treat those as import-map entries.
func resolveSpec(base *url.URL, spec string) string {
	switch {
	case strings.Contains(spec, "://"):
		return spec
	case strings.HasPrefix(spec, "/"), strings.HasPrefix(spec, "./"), strings.HasPrefix(spec, "../"):
		ref, err := url.Parse(spec)
		if err != nil {
			return ""
		}
		return base.ResolveReference(ref).String()
	}
	return ""
}

// attachTypesHeader follows the x-typescript-types header on resp and records
// the result on f.typesURL. Errors are non-fatal — a warning is emitted.
func (v *vendorer) attachTypesHeader(u *url.URL, resp *http.Response, rawURL string, f *fetched) {
	typesURL := resp.Header.Get("x-typescript-types")
	if typesURL == "" {
		return
	}
	if strings.HasPrefix(typesURL, "/") {
		typesURL = u.Scheme + "://" + u.Host + typesURL
	}
	tcanon, err := v.fetch(typesURL)
	if err != nil {
		v.warnings = append(v.warnings, fmt.Sprintf("failed to download types for %s: %v", rawURL, err))
		return
	}
	if f != nil {
		f.typesURL = tcanon
	}
}

// sidecarURL returns the URL of the .d.ts file that may live alongside a script
// file (e.g. foo.mjs -> foo.d.ts), or "" if no sidecar lookup is appropriate.
func sidecarURL(u *url.URL, f *fetched) string {
	if f.loader == api.LoaderNone {
		return ""
	}
	ext := path.Ext(u.Path)
	if ext == "" {
		return ""
	}
	s := *u
	s.Path = strings.TrimSuffix(u.Path, ext) + ".d.ts"
	return s.String()
}

// loaderFor returns the esbuild loader for the given filename. Returns
// api.LoaderNone for files that should not be processed by esbuild (e.g.
// .d.ts declarations or non-script assets).
func loaderFor(filename string) api.Loader {
	f := strings.ToLower(filename)
	if strings.HasSuffix(f, ".d.ts") {
		return api.LoaderNone
	}
	switch filepath.Ext(f) {
	case ".ts", ".mts":
		return api.LoaderTS
	case ".tsx":
		return api.LoaderTSX
	case ".jsx":
		return api.LoaderJSX
	case ".js", ".mjs":
		return api.LoaderJS
	}
	return api.LoaderNone
}

// discoverESBuild uses esbuild's parser to enumerate the imports in f.content.
// esbuild's printed output is discarded - only its import resolution is used.
func (v *vendorer) discoverESBuild(u *url.URL, f *fetched) error {
	parentLoader := f.loader
	var resolveErr error

	api.Build(api.BuildOptions{
		Bundle:   true,
		Write:    false,
		LogLevel: api.LogLevelSilent,
		Stdin: &api.StdinOptions{
			Contents:   string(f.content),
			Sourcefile: u.String(),
			Loader:     f.loader,
		},
		Plugins: []api.Plugin{{
			Name: "unpm",
			Setup: func(build api.PluginBuild) {
				build.OnResolve(api.OnResolveOptions{Filter: ".*"}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
					external := api.OnResolveResult{Path: args.Path, External: true}
					if resolveErr != nil {
						return external, nil
					}

					spec := args.Path
					depURL := resolveSpec(u, spec)
					if depURL == "" {
						// bare specifier - findBareImports will warn later
						return external, nil
					}

					depCanon, err := v.resolveAndFetch(depURL, parentLoader)
					if err != nil {
						if errors.Is(err, errSkip) {
							return external, nil
						}
						resolveErr = err
						return external, err
					}
					f.deps[spec] = depCanon
					return external, nil
				})
			},
		}},
	})

	return resolveErr
}

// discoverTypeImports enumerates `import type` / `export type` statements in
// TS source, which esbuild elides before OnResolve sees them. Fetched deps are
// recorded on f.deps so the file ends up on disk for TS to resolve against;
// the .ts source itself is written verbatim so the original specs stay intact.
func (v *vendorer) discoverTypeImports(u *url.URL, f *fetched) {
	for _, groups := range typeImportRe.FindAllStringSubmatch(string(f.content), -1) {
		spec := groups[1]
		if _, seen := f.deps[spec]; seen {
			continue
		}
		depURL := resolveSpec(u, spec)
		if depURL == "" {
			continue
		}
		if depCanon, err := v.resolveAndFetch(depURL, api.LoaderTS); err == nil {
			f.deps[spec] = depCanon
		}
	}
}

// discoverRegex enumerates imports in non-script files (notably .d.ts) using a
// regex. Bare specifiers are skipped because importRe matches only paths that
// begin with a scheme, "/", "./", or "../".
func (v *vendorer) discoverRegex(u *url.URL, f *fetched) {
	parentLoader := api.LoaderNone
	if strings.HasSuffix(strings.ToLower(f.filename), ".d.ts") {
		parentLoader = api.LoaderTS
	}

	for _, groups := range importRe.FindAllStringSubmatch(string(f.content), -1) {
		spec := groups[3]
		if spec == "" {
			spec = groups[7]
		}
		if _, seen := f.deps[spec]; seen {
			continue
		}
		depURL := resolveSpec(u, spec)
		if depURL == "" {
			continue
		}
		depCanon, err := v.resolveAndFetch(depURL, parentLoader)
		if err != nil {
			v.warnings = append(v.warnings, fmt.Sprintf("failed to download %s: %v", depURL, err))
			continue
		}
		f.deps[spec] = depCanon
	}
}

func (v *vendorer) writeOne(canon string) error {
	f := v.fetched[canon]
	if f == nil {
		return nil
	}

	var content []byte
	if isSourceMap(f.filename) {
		content = f.content
	} else {
		content = v.rewrite(f)
	}
	if err := v.writeFile(canon, f.vendorRel, content); err != nil {
		return err
	}

	// The original .ts/.tsx source is written alongside the transpiled .js so
	// TypeScript can find it for type-checking via sibling resolution.
	if f.types != nil && f.types.rel != f.vendorRel {
		return v.writeFile(canon, f.types.rel, f.types.content)
	}
	return nil
}

func (v *vendorer) writeFile(canon, rel string, content []byte) error {
	if v.config.IsPinned(rel) {
		if v.config.Unpm.Verbose {
			fmt.Printf("%s -> %s (pinned)\n", canon, rel)
		}
		return nil
	}
	dest := filepath.Join(v.config.Unpm.Out, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", filepath.Dir(dest), err)
	}
	if err := os.WriteFile(dest, content, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", dest, err)
	}
	if v.config.Unpm.Verbose {
		fmt.Printf("%s -> %s\n", canon, rel)
	}
	return nil
}

// rewrite substitutes import specs and the sourceMappingURL in f.content with
// paths relative to f.vendorRel using the vendor locations of f's deps. Script
// files and text files (notably .d.ts) need different substitution strategies,
// so the two are handled by separate functions.
func (v *vendorer) rewrite(f *fetched) []byte {
	currentDir := path.Dir(f.vendorRel)

	rewrites := make(map[string]string, len(f.deps))
	for spec, dep := range f.deps {
		df, ok := v.fetched[dep]
		if !ok {
			continue
		}
		rewrites[spec] = relPath(currentDir, df.vendorRel)
	}

	var result string
	if f.isScript() {
		result = rewriteScriptImports(string(f.content), rewrites)
	} else {
		result = rewriteTextImports(string(f.content), rewrites)
	}

	if f.sourceMap != "" {
		result = v.rewriteSourceMapURL(result, currentDir, f.sourceMap)
	}

	return []byte(result)
}

// rewriteScriptImports replaces dep specifiers in JS source by literal string
// substitution across the three quote styles. esbuild has already normalized the
// source, so each specifier appears verbatim and a blunt replace is safe.
func rewriteScriptImports(content string, rewrites map[string]string) string {
	pairs := make([]string, 0, len(rewrites)*6)
	for spec, rewritten := range rewrites {
		if spec == rewritten {
			continue
		}
		pairs = append(pairs,
			`"`+spec+`"`, `"`+rewritten+`"`,
			`'`+spec+`'`, `'`+rewritten+`'`,
			"`"+spec+"`", "`"+rewritten+"`",
		)
	}
	return strings.NewReplacer(pairs...).Replace(content)
}

// rewriteTextImports replaces dep specifiers in non-script text files (notably
// .d.ts) using importRe, so only genuine import/export specifiers are rewritten
// rather than every matching string in the file.
func rewriteTextImports(content string, rewrites map[string]string) string {
	return importRe.ReplaceAllStringFunc(content, func(match string) string {
		groups := importRe.FindStringSubmatch(match)
		var prefix, quote, impPath, suffix string
		if groups[1] != "" {
			prefix, quote, impPath, suffix = groups[1], groups[2], groups[3], groups[4]
		} else {
			prefix, quote, impPath, suffix = groups[5], groups[6], groups[7], groups[8]
		}
		rewritten, ok := rewrites[impPath]
		if !ok {
			return match
		}
		return prefix + quote + rewritten + suffix
	})
}

// rewriteSourceMapURL points the //# sourceMappingURL comment at the vendored
// copy of the map, leaving inline data: URLs untouched.
func (v *vendorer) rewriteSourceMapURL(content, currentDir, sourceMap string) string {
	df, ok := v.fetched[sourceMap]
	if !ok {
		return content
	}
	return sourceMappingRe.ReplaceAllStringFunc(content, func(match string) string {
		groups := sourceMappingRe.FindStringSubmatch(match)
		if strings.HasPrefix(groups[2], "data:") {
			return match
		}
		return groups[1] + relPath(currentDir, df.vendorRel)
	})
}

// relPath expresses target relative to fromDir in slash form, prefixing "./" so
// it never reads as a bare specifier.
func relPath(fromDir, target string) string {
	rel, _ := filepath.Rel(fromDir, target)
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, ".") {
		rel = "./" + rel
	}
	return rel
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
