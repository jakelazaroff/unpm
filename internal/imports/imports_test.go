// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// Copyright (c) 2026 Jake Lazaroff https://github.com/jakelazaroff/unpm

package imports_test

import (
	"net/url"
	"reflect"
	"testing"

	"github.com/jakelazaroff/unpm/internal/imports"
)

func TestScan(t *testing.T) {
	src := `
import a from "react";
import { b } from "./b.js";
export { c } from "../c.js";
const d = await import("/d.js");
import "https://example.com/e.js";
const notImport = "./decoy.js";
`
	got := imports.Scan(src)
	want := []string{"react", "./b.js", "../c.js", "/d.js", "https://example.com/e.js"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Scan = %q, want %q", got, want)
	}
}

func TestRewrite(t *testing.T) {
	src := `import a from "./a.js";` + "\n" + `import b from "react";`
	got := imports.Rewrite(src, func(spec string) (string, bool) {
		if spec == "./a.js" {
			return "./vendor/a.js", true
		}
		return "", false
	})
	want := `import a from "./vendor/a.js";` + "\n" + `import b from "react";`
	if got != want {
		t.Fatalf("Rewrite = %q, want %q", got, want)
	}
}

func TestScanTypes(t *testing.T) {
	src := `
import type { T } from "./types.js";
import { value } from "./value.js";
export type { U } from "./u.js";
`
	got := imports.ScanTypes(src)
	want := []string{"./types.js", "./u.js"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ScanTypes = %q, want %q", got, want)
	}
}

func TestScanSourceMaps(t *testing.T) {
	src := "code();\n//# sourceMappingURL=app.js.map\n//# sourceMappingURL=data:application/json;base64,abc\n"
	got := imports.ScanSourceMaps(src)
	want := []string{"app.js.map"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ScanSourceMaps = %q, want %q", got, want)
	}
}

func TestRewriteSourceMap(t *testing.T) {
	src := "code();\n//# sourceMappingURL=app.js.map\n"
	got := imports.RewriteSourceMap(src, func(string) string { return "./vendor/app.js.map" })
	want := "code();\n//# sourceMappingURL=./vendor/app.js.map\n"
	if got != want {
		t.Fatalf("RewriteSourceMap = %q, want %q", got, want)
	}

	// data: URLs are left untouched.
	inline := "//# sourceMappingURL=data:application/json;base64,abc\n"
	if out := imports.RewriteSourceMap(inline, func(string) string { return "x" }); out != inline {
		t.Fatalf("RewriteSourceMap mangled inline map: %q", out)
	}
}

func TestResolve(t *testing.T) {
	base, _ := url.Parse("https://esm.sh/preact@10/hooks/index.js")
	cases := map[string]string{
		"react":                  "", // bare
		"./util.js":              "https://esm.sh/preact@10/hooks/util.js",
		"../signals.js":          "https://esm.sh/preact@10/signals.js",
		"/absolute.js":           "https://esm.sh/absolute.js",
		"https://other.com/x.js": "https://other.com/x.js",
	}
	for spec, want := range cases {
		if got := imports.Resolve(base, spec); got != want {
			t.Errorf("Resolve(%q) = %q, want %q", spec, got, want)
		}
	}
}
