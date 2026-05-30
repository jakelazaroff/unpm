// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// Copyright (c) 2026 Jake Lazaroff https://github.com/jakelazaroff/unpm

// Package imports scans JavaScript and TypeScript source for the references
// unpm cares about — import/export specifiers, type-only imports, and source
// map comments — and rewrites them. It is the single home for "what counts as
// an import," shared by the vendoring engine and the check/why inspector.
package imports

import (
	"net/url"
	"regexp"
	"strings"
)

// importRe matches a static import/export statement or a dynamic import() call
// whose specifier is a string literal. The specifier character class is broad
// ([^"']+) so one pattern covers bare, relative, absolute, and URL specifiers;
// callers filter to the kinds they care about (see Resolve).
//
// Static form:  prefix(1) quote(2) spec(3) quote(4)
// Dynamic form: prefix(5) quote(6) spec(7) quote(8)
var importRe = regexp.MustCompile(`(\b(?:import|export)\s*(?:[^"']*\bfrom\s*|))(["'])([^"']+)(["'])|(\bimport\s*\(\s*)(["'])([^"']+)(["']\s*\))`)

// typeImportRe matches `import type ... from "spec"` and `export type ... from
// "spec"` statements. esbuild elides these before its resolver runs, so they
// have to be found separately.
var typeImportRe = regexp.MustCompile(`(?m)(?:^|[\s;])(?:import|export)\s+type\b[^"';\n]*?["']([^"']+)["']`)

// sourceMappingRe matches //# sourceMappingURL=... comments: prefix(1) url(2).
var sourceMappingRe = regexp.MustCompile(`(//[#@]\s*sourceMappingURL\s*=\s*)(\S+)`)

// specOf returns the specifier from a full importRe submatch — group 3 for a
// static import, group 7 for a dynamic one.
func specOf(groups []string) string {
	if groups[3] != "" {
		return groups[3]
	}
	return groups[7]
}

// Scan returns every import/export/dynamic-import specifier in src, in source
// order, including duplicates and bare specifiers.
func Scan(src string) []string {
	matches := importRe.FindAllStringSubmatch(src, -1)
	specs := make([]string, 0, len(matches))
	for _, g := range matches {
		specs = append(specs, specOf(g))
	}
	return specs
}

// Rewrite replaces import specifiers in src. replace is called for each
// specifier; when it returns ok the specifier is swapped for the returned value,
// otherwise the statement is left untouched.
func Rewrite(src string, replace func(spec string) (string, bool)) string {
	return importRe.ReplaceAllStringFunc(src, func(match string) string {
		g := importRe.FindStringSubmatch(match)
		var prefix, quote, spec, suffix string
		if g[1] != "" {
			prefix, quote, spec, suffix = g[1], g[2], g[3], g[4]
		} else {
			prefix, quote, spec, suffix = g[5], g[6], g[7], g[8]
		}
		rewritten, ok := replace(spec)
		if !ok {
			return match
		}
		return prefix + quote + rewritten + suffix
	})
}

// ScanTypes returns the specifiers of `import type` / `export type` statements,
// in source order.
func ScanTypes(src string) []string {
	matches := typeImportRe.FindAllStringSubmatch(src, -1)
	specs := make([]string, 0, len(matches))
	for _, g := range matches {
		specs = append(specs, g[1])
	}
	return specs
}

// ScanSourceMaps returns the targets of //# sourceMappingURL comments in src,
// omitting inline data: URLs.
func ScanSourceMaps(src string) []string {
	var urls []string
	for _, g := range sourceMappingRe.FindAllStringSubmatch(src, -1) {
		if strings.HasPrefix(g[2], "data:") {
			continue
		}
		urls = append(urls, g[2])
	}
	return urls
}

// RewriteSourceMap replaces the target of each //# sourceMappingURL comment in
// src with the value returned by replace. Inline data: URLs are left unchanged.
func RewriteSourceMap(src string, replace func(target string) string) string {
	return sourceMappingRe.ReplaceAllStringFunc(src, func(match string) string {
		g := sourceMappingRe.FindStringSubmatch(match)
		if strings.HasPrefix(g[2], "data:") {
			return match
		}
		return g[1] + replace(g[2])
	})
}

// Resolve returns the absolute URL for spec resolved against the importing
// file's URL. Bare specifiers (no scheme and no leading "/", "./", or "../")
// return "" — callers should treat those as import-map entries.
func Resolve(base *url.URL, spec string) string {
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
