// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// Copyright (c) 2026 Jake Lazaroff https://github.com/jakelazaroff/unpm

package unpm_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jakelazaroff/unpm/pkg/cfg"
	"github.com/jakelazaroff/unpm/pkg/unpm"
)

// testFile defines a file served by the test server.
type testFile struct {
	body    string
	headers map[string]string
}

// newTestServer creates an httptest.Server that serves the given files by path.
func newTestServer(files map[string]testFile) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		for k, v := range f.headers {
			w.Header().Set(k, v)
		}
		w.Write([]byte(f.body))
	}))
}

func TestFetch_SingleFile(t *testing.T) {
	srv := newTestServer(map[string]testFile{
		"/mylib.js": {body: `export function hello() { return "hi"; }`},
	})
	defer srv.Close()

	outDir := filepath.Join(t.TempDir(), "vendor")
	root := t.TempDir()

	c := &cfg.Config{
		Imports: map[string]string{"mylib": srv.URL + "/mylib.js"},
		Unpm:    cfg.Options{Out: outDir, Root: root},
	}
	if err := unpm.Fetch(c); err != nil {
		t.Fatal(err)
	}

	// Check the JS file exists
	host := strings.TrimPrefix(srv.URL, "http://")
	jsPath := filepath.Join(outDir, host, "mylib.js")
	data, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("expected file at %s: %v", jsPath, err)
	}
	if !strings.Contains(string(data), "hello") {
		t.Fatalf("unexpected content: %s", data)
	}

	// Check importmap.json
	imData, err := os.ReadFile(filepath.Join(outDir, "importmap.json"))
	if err != nil {
		t.Fatal(err)
	}
	var im struct {
		Imports map[string]string `json:"imports"`
	}
	if err := json.Unmarshal(imData, &im); err != nil {
		t.Fatal(err)
	}
	if _, ok := im.Imports["mylib"]; !ok {
		t.Fatalf("importmap.json missing 'mylib': %v", im.Imports)
	}

	// Check importmap.js exists
	if _, err := os.Stat(filepath.Join(outDir, "importmap.js")); err != nil {
		t.Fatal("importmap.js not found")
	}

	// Check jsconfig.json
	if _, err := os.Stat(filepath.Join(outDir, "jsconfig.json")); err != nil {
		t.Fatal("jsconfig.json not found")
	}
}

func TestFetch_TransitiveDeps(t *testing.T) {
	srv := newTestServer(map[string]testFile{
		"/a.js": {body: `import { b } from "./b.js"; export const a = b;`},
		"/b.js": {body: `export const b = 42;`},
	})
	defer srv.Close()

	outDir := filepath.Join(t.TempDir(), "vendor")
	c := &cfg.Config{
		Imports: map[string]string{"a": srv.URL + "/a.js"},
		Unpm:    cfg.Options{Out: outDir, Root: t.TempDir()},
	}
	if err := unpm.Fetch(c); err != nil {
		t.Fatal(err)
	}

	host := strings.TrimPrefix(srv.URL, "http://")

	// Both files should exist
	for _, name := range []string{"a.js", "b.js"} {
		p := filepath.Join(outDir, host, name)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s to exist: %v", name, err)
		}
	}

	// a.js should have rewritten import (still relative)
	data, _ := os.ReadFile(filepath.Join(outDir, host, "a.js"))
	if !strings.Contains(string(data), `"./b.js"`) {
		t.Fatalf("expected rewritten import to ./b.js, got: %s", data)
	}
}

func TestFetch_OriginRelativeImport(t *testing.T) {
	srv := newTestServer(map[string]testFile{
		"/entry.js":   {body: `import { dep } from "/lib/dep.js"; export default dep;`},
		"/lib/dep.js": {body: `export const dep = "ok";`},
	})
	defer srv.Close()

	outDir := filepath.Join(t.TempDir(), "vendor")
	c := &cfg.Config{
		Imports: map[string]string{"entry": srv.URL + "/entry.js"},
		Unpm:    cfg.Options{Out: outDir, Root: t.TempDir()},
	}
	if err := unpm.Fetch(c); err != nil {
		t.Fatal(err)
	}

	host := strings.TrimPrefix(srv.URL, "http://")

	// dep.js should be downloaded
	depPath := filepath.Join(outDir, host, "lib", "dep.js")
	if _, err := os.Stat(depPath); err != nil {
		t.Fatalf("dep.js not downloaded: %v", err)
	}

	// entry.js should have rewritten import to relative path
	data, _ := os.ReadFile(filepath.Join(outDir, host, "entry.js"))
	content := string(data)
	if strings.Contains(content, `"/lib/dep.js"`) {
		t.Fatalf("origin-relative import was not rewritten: %s", content)
	}
	if !strings.Contains(content, `"./lib/dep.js"`) {
		t.Fatalf("expected rewritten import to ./lib/dep.js, got: %s", content)
	}
}

func TestFetch_ESMPath(t *testing.T) {
	srv := newTestServer(map[string]testFile{
		"/preact": {
			body:    `/* shim */`,
			headers: map[string]string{"X-ESM-Path": "/es2022/preact.mjs"},
		},
		"/es2022/preact.mjs": {body: `export function h() {}`},
	})
	defer srv.Close()

	outDir := filepath.Join(t.TempDir(), "vendor")
	c := &cfg.Config{
		Imports: map[string]string{"preact": srv.URL + "/preact"},
		Unpm:    cfg.Options{Out: outDir, Root: t.TempDir()},
	}
	if err := unpm.Fetch(c); err != nil {
		t.Fatal(err)
	}

	host := strings.TrimPrefix(srv.URL, "http://")

	// The canonical file should be downloaded
	canonPath := filepath.Join(outDir, host, "es2022", "preact.mjs")
	if _, err := os.Stat(canonPath); err != nil {
		t.Fatalf("canonical ESM file not downloaded: %v", err)
	}

	// The shim directory should NOT have a file (unpm skips the shim)
	shimDir := filepath.Join(outDir, host, "preact")
	if _, err := os.Stat(shimDir); err == nil {
		entries, _ := os.ReadDir(shimDir)
		for _, e := range entries {
			data, _ := os.ReadFile(filepath.Join(shimDir, e.Name()))
			if strings.Contains(string(data), "shim") {
				t.Fatal("shim file should not have been written")
			}
		}
	}
}

func TestFetch_TypeScriptTypes(t *testing.T) {
	srv := newTestServer(map[string]testFile{
		"/mylib.js": {
			body:    `export function hello() {}`,
			headers: map[string]string{"X-Typescript-Types": "/mylib.d.ts"},
		},
		"/mylib.d.ts": {body: `export declare function hello(): void;`},
	})
	defer srv.Close()

	outDir := filepath.Join(t.TempDir(), "vendor")
	c := &cfg.Config{
		Imports: map[string]string{"mylib": srv.URL + "/mylib.js"},
		Unpm:    cfg.Options{Out: outDir, Root: t.TempDir()},
	}
	if err := unpm.Fetch(c); err != nil {
		t.Fatal(err)
	}

	host := strings.TrimPrefix(srv.URL, "http://")

	// .d.ts file should be downloaded
	dtsPath := filepath.Join(outDir, host, "mylib.d.ts")
	if _, err := os.Stat(dtsPath); err != nil {
		t.Fatalf(".d.ts file not downloaded: %v", err)
	}

	// jsconfig.json should map to the .d.ts file
	data, _ := os.ReadFile(filepath.Join(outDir, "jsconfig.json"))
	content := string(data)
	if !strings.Contains(content, "mylib.d.ts") {
		t.Fatalf("jsconfig.json should reference .d.ts file, got: %s", content)
	}
}

func TestFetch_SourceMap(t *testing.T) {
	srv := newTestServer(map[string]testFile{
		"/app.js":     {body: "export const x = 1;\n//# sourceMappingURL=app.js.map"},
		"/app.js.map": {body: `{"version":3,"sources":["app.ts"]}`},
	})
	defer srv.Close()

	outDir := filepath.Join(t.TempDir(), "vendor")
	c := &cfg.Config{
		Imports: map[string]string{"app": srv.URL + "/app.js"},
		Unpm:    cfg.Options{Out: outDir, Root: t.TempDir()},
	}
	if err := unpm.Fetch(c); err != nil {
		t.Fatal(err)
	}

	host := strings.TrimPrefix(srv.URL, "http://")

	// .map file should be downloaded
	mapPath := filepath.Join(outDir, host, "app.js.map")
	if _, err := os.Stat(mapPath); err != nil {
		t.Fatalf("source map not downloaded: %v", err)
	}

	// The sourceMappingURL should be rewritten to a relative path
	data, _ := os.ReadFile(filepath.Join(outDir, host, "app.js"))
	content := string(data)
	if !strings.Contains(content, "sourceMappingURL=./app.js.map") {
		t.Fatalf("sourceMappingURL not rewritten, got: %s", content)
	}
}

func TestFetch_Pin(t *testing.T) {
	srv := newTestServer(map[string]testFile{
		"/a.js": {body: `export const a = 1;`},
		"/b.js": {body: `export const b = 2;`},
	})
	defer srv.Close()

	outDir := filepath.Join(t.TempDir(), "vendor")
	c := &cfg.Config{
		Imports: map[string]string{
			"a": srv.URL + "/a.js",
			"b": srv.URL + "/b.js",
		},
		Unpm: cfg.Options{Out: outDir, Root: t.TempDir(), Pin: []string{"b"}},
	}
	if err := unpm.Fetch(c); err != nil {
		t.Fatal(err)
	}

	host := strings.TrimPrefix(srv.URL, "http://")

	// a.js should exist
	if _, err := os.Stat(filepath.Join(outDir, host, "a.js")); err != nil {
		t.Fatal("a.js should be downloaded")
	}

	// b.js should NOT exist (pinned)
	if _, err := os.Stat(filepath.Join(outDir, host, "b.js")); err == nil {
		t.Fatal("b.js should not be downloaded (pinned)")
	}
}

func TestCheck(t *testing.T) {
	outDir := t.TempDir()
	host := "example.com"

	// Set up vendor files
	dir := filepath.Join(outDir, host)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "a.js"), []byte(`import { b } from "./b.js"; export const a = b;`), 0o644)
	os.WriteFile(filepath.Join(dir, "b.js"), []byte(`export const b = 1;`), 0o644)

	// Write generated files so check doesn't warn about them
	os.WriteFile(filepath.Join(outDir, "importmap.js"), []byte(""), 0o644)
	os.WriteFile(filepath.Join(outDir, "importmap.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(outDir, "jsconfig.json"), []byte("{}"), 0o644)

	t.Run("passing", func(t *testing.T) {
		c := &cfg.Config{
			Imports: map[string]string{"a": "https://example.com/a.js"},
			Unpm:    cfg.Options{Out: outDir},
		}
		if err := unpm.Check(c); err != nil {
			t.Fatalf("expected check to pass: %v", err)
		}
	})

	t.Run("missing file on disk", func(t *testing.T) {
		c := &cfg.Config{
			Imports: map[string]string{
				"a":       "https://example.com/a.js",
				"missing": "https://example.com/missing.js",
			},
			Unpm: cfg.Options{Out: outDir},
		}
		err := unpm.Check(c)
		if err == nil {
			t.Fatal("expected check to fail for missing file")
		}
	})

	t.Run("bare import not in map", func(t *testing.T) {
		os.WriteFile(filepath.Join(dir, "c.js"), []byte(`import { x } from "unknown-pkg"; export const c = x;`), 0o644)
		c := &cfg.Config{
			Imports: map[string]string{"c": "https://example.com/c.js"},
			Unpm:    cfg.Options{Out: outDir},
		}
		err := unpm.Check(c)
		if err == nil {
			t.Fatal("expected check to fail for bare import not in map")
		}
		os.Remove(filepath.Join(dir, "c.js"))
	})
}

func TestPrune(t *testing.T) {
	outDir := t.TempDir()
	host := "example.com"

	dir := filepath.Join(outDir, host)
	os.MkdirAll(dir, 0o755)

	// a.js imports b.js (reachable); c.js is unreachable
	os.WriteFile(filepath.Join(dir, "a.js"), []byte(`import { b } from "./b.js"; export const a = b;`), 0o644)
	os.WriteFile(filepath.Join(dir, "b.js"), []byte(`export const b = 1;`), 0o644)
	os.WriteFile(filepath.Join(dir, "c.js"), []byte(`export const c = "orphan";`), 0o644)

	// Create an unreachable file in a subdirectory (to test empty dir removal)
	subDir := filepath.Join(dir, "sub")
	os.MkdirAll(subDir, 0o755)
	os.WriteFile(filepath.Join(subDir, "orphan.js"), []byte(`export default 0;`), 0o644)

	// Write generated files
	os.WriteFile(filepath.Join(outDir, "importmap.js"), []byte(""), 0o644)
	os.WriteFile(filepath.Join(outDir, "importmap.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(outDir, "jsconfig.json"), []byte("{}"), 0o644)

	c := &cfg.Config{
		Imports: map[string]string{"a": "https://example.com/a.js"},
		Unpm:    cfg.Options{Out: outDir},
	}
	if err := unpm.Prune(c); err != nil {
		t.Fatal(err)
	}

	// a.js and b.js should remain
	if _, err := os.Stat(filepath.Join(dir, "a.js")); err != nil {
		t.Fatal("a.js should not be pruned")
	}
	if _, err := os.Stat(filepath.Join(dir, "b.js")); err != nil {
		t.Fatal("b.js should not be pruned")
	}

	// c.js and sub/orphan.js should be removed
	if _, err := os.Stat(filepath.Join(dir, "c.js")); !os.IsNotExist(err) {
		t.Fatal("c.js should be pruned")
	}
	if _, err := os.Stat(filepath.Join(subDir, "orphan.js")); !os.IsNotExist(err) {
		t.Fatal("sub/orphan.js should be pruned")
	}

	// sub/ directory should be removed (now empty)
	if _, err := os.Stat(subDir); !os.IsNotExist(err) {
		t.Fatal("empty sub/ directory should be removed")
	}
}

func TestWhy(t *testing.T) {
	outDir := t.TempDir()
	host := "example.com"

	dir := filepath.Join(outDir, host)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "entry.js"), []byte(`import { dep } from "./dep.js"; export default dep;`), 0o644)
	os.WriteFile(filepath.Join(dir, "dep.js"), []byte(`export const dep = 1;`), 0o644)

	c := &cfg.Config{
		Imports: map[string]string{"entry": "https://example.com/entry.js"},
		Unpm:    cfg.Options{Out: outDir},
	}

	// Should find the chain
	if err := unpm.Why(c, "example.com/dep.js"); err != nil {
		t.Fatalf("expected Why to find chain: %v", err)
	}

	// Should fail for a file not in the chain
	if err := unpm.Why(c, "example.com/nonexistent.js"); err == nil {
		t.Fatal("expected Why to fail for nonexistent target")
	}
}
