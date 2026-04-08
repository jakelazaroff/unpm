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

func TestVendor_SingleFile(t *testing.T) {
	srv := newTestServer(map[string]testFile{
		"/mylib.js": {body: `export function hello() { return "hi"; }`},
	})
	defer srv.Close()

	outDir := filepath.Join(t.TempDir(), "vendor")

	c := &cfg.Config{
		Imports: map[string]string{"mylib": srv.URL + "/mylib.js"},
		Unpm:    cfg.Options{Out: outDir, Root: "/"},
	}
	if _, err := unpm.Vendor(c); err != nil {
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

func TestVendor_TransitiveDeps(t *testing.T) {
	srv := newTestServer(map[string]testFile{
		"/a.js": {body: `import { b } from "./b.js"; export const a = b;`},
		"/b.js": {body: `export const b = 42;`},
	})
	defer srv.Close()

	outDir := filepath.Join(t.TempDir(), "vendor")
	c := &cfg.Config{
		Imports: map[string]string{"a": srv.URL + "/a.js"},
		Unpm:    cfg.Options{Out: outDir, Root: "/"},
	}
	if _, err := unpm.Vendor(c); err != nil {
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

func TestVendor_OriginRelativeImport(t *testing.T) {
	srv := newTestServer(map[string]testFile{
		"/entry.js":   {body: `import { dep } from "/lib/dep.js"; export default dep;`},
		"/lib/dep.js": {body: `export const dep = "ok";`},
	})
	defer srv.Close()

	outDir := filepath.Join(t.TempDir(), "vendor")
	c := &cfg.Config{
		Imports: map[string]string{"entry": srv.URL + "/entry.js"},
		Unpm:    cfg.Options{Out: outDir, Root: "/"},
	}
	if _, err := unpm.Vendor(c); err != nil {
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

func TestVendor_ESMPath(t *testing.T) {
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
		Unpm:    cfg.Options{Out: outDir, Root: "/"},
	}
	if _, err := unpm.Vendor(c); err != nil {
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

func TestVendor_TypeScriptTypes(t *testing.T) {
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
		Unpm:    cfg.Options{Out: outDir, Root: "/"},
	}
	if _, err := unpm.Vendor(c); err != nil {
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

func TestVendor_SourceMap(t *testing.T) {
	srv := newTestServer(map[string]testFile{
		"/app.js":     {body: "export const x = 1;\n//# sourceMappingURL=app.js.map"},
		"/app.js.map": {body: `{"version":3,"sources":["app.ts"]}`},
	})
	defer srv.Close()

	outDir := filepath.Join(t.TempDir(), "vendor")
	c := &cfg.Config{
		Imports: map[string]string{"app": srv.URL + "/app.js"},
		Unpm:    cfg.Options{Out: outDir, Root: "/"},
	}
	if _, err := unpm.Vendor(c); err != nil {
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

func TestVendor_DynamicImport(t *testing.T) {
	srv := newTestServer(map[string]testFile{
		"/entry.js": {body: `const mod = import("./lazy.js"); export default mod;`},
		"/lazy.js":  {body: `export const lazy = "loaded";`},
	})
	defer srv.Close()

	outDir := filepath.Join(t.TempDir(), "vendor")
	c := &cfg.Config{
		Imports: map[string]string{"entry": srv.URL + "/entry.js"},
		Unpm:    cfg.Options{Out: outDir, Root: "/"},
	}
	if _, err := unpm.Vendor(c); err != nil {
		t.Fatal(err)
	}

	host := strings.TrimPrefix(srv.URL, "http://")

	// Both files should be downloaded
	for _, name := range []string{"entry.js", "lazy.js"} {
		p := filepath.Join(outDir, host, name)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s to exist: %v", name, err)
		}
	}

	// entry.js should have rewritten dynamic import path
	data, _ := os.ReadFile(filepath.Join(outDir, host, "entry.js"))
	content := string(data)
	if !strings.Contains(content, `import("./lazy.js")`) {
		t.Fatalf("expected rewritten dynamic import to ./lazy.js, got: %s", content)
	}
}

func TestVendor_Pin(t *testing.T) {
	srv := newTestServer(map[string]testFile{
		"/a.js": {body: `export const a = 1;`},
		"/b.js": {body: `export const b = 2;`},
	})
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")

	t.Run("exact path", func(t *testing.T) {
		root := t.TempDir()
		outDir := filepath.Join(root, "vendor")

		// Pre-create b.js with local modifications
		os.MkdirAll(filepath.Join(outDir, host), 0o755)
		os.WriteFile(filepath.Join(outDir, host, "b.js"), []byte(`export const b = "local";`), 0o644)

		c := &cfg.Config{
			Imports: map[string]string{
				"a": srv.URL + "/a.js",
				"b": srv.URL + "/b.js",
			},
			Unpm: cfg.Options{Out: outDir, Root: "/", Pin: []string{host + "/b.js"}},
		}
		if _, err := unpm.Vendor(c); err != nil {
			t.Fatal(err)
		}

		// a.js should be downloaded from the server
		data, err := os.ReadFile(filepath.Join(outDir, host, "a.js"))
		if err != nil {
			t.Fatal("a.js should be downloaded")
		}
		if string(data) != `export const a = 1;` {
			t.Fatalf("a.js has unexpected content: %s", data)
		}

		// b.js should still have its local content (pinned)
		data, err = os.ReadFile(filepath.Join(outDir, host, "b.js"))
		if err != nil {
			t.Fatal("b.js should still exist")
		}
		if string(data) != `export const b = "local";` {
			t.Fatalf("b.js should not be overwritten (pinned), got: %s", data)
		}
	})

	t.Run("glob pattern", func(t *testing.T) {
		root := t.TempDir()
		outDir := filepath.Join(root, "vendor")

		// Pre-create both files with local modifications
		os.MkdirAll(filepath.Join(outDir, host), 0o755)
		os.WriteFile(filepath.Join(outDir, host, "a.js"), []byte(`export const a = "local a";`), 0o644)
		os.WriteFile(filepath.Join(outDir, host, "b.js"), []byte(`export const b = "local b";`), 0o644)

		c := &cfg.Config{
			Imports: map[string]string{
				"a": srv.URL + "/a.js",
				"b": srv.URL + "/b.js",
			},
			Unpm: cfg.Options{Out: outDir, Root: "/", Pin: []string{host + "/**"}},
		}
		if _, err := unpm.Vendor(c); err != nil {
			t.Fatal(err)
		}

		// Both files should still have their local content (glob pinned)
		for _, name := range []string{"a.js", "b.js"} {
			data, err := os.ReadFile(filepath.Join(outDir, host, name))
			if err != nil {
				t.Fatalf("%s should still exist", name)
			}
			if !strings.Contains(string(data), "local") {
				t.Fatalf("%s should not be overwritten (pinned), got: %s", name, data)
			}
		}
	})
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
	os.WriteFile(filepath.Join(outDir, "importmap.json"), []byte(`{"imports":{"a":"/example.com/a.js"}}`), 0o644)
	os.WriteFile(filepath.Join(outDir, "jsconfig.json"), []byte("{}"), 0o644)

	t.Run("passing", func(t *testing.T) {
		c := &cfg.Config{
			Imports: map[string]string{"a": "https://example.com/a.js"},
			Unpm:    cfg.Options{Out: outDir, Root: "/"},
		}
		if err := unpm.Check(c); err != nil {
			t.Fatalf("expected check to pass: %v", err)
		}
	})

	t.Run("missing file on disk", func(t *testing.T) {
		os.WriteFile(filepath.Join(outDir, "importmap.json"), []byte(`{"imports":{"a":"/example.com/a.js","missing":"/example.com/missing.js"}}`), 0o644)
		c := &cfg.Config{
			Imports: map[string]string{
				"a":       "https://example.com/a.js",
				"missing": "https://example.com/missing.js",
			},
			Unpm: cfg.Options{Out: outDir, Root: "/"},
		}
		err := unpm.Check(c)
		if err == nil {
			t.Fatal("expected check to fail for missing file")
		}
		// Restore importmap for subsequent tests
		os.WriteFile(filepath.Join(outDir, "importmap.json"), []byte(`{"imports":{"a":"/example.com/a.js"}}`), 0o644)
	})

	t.Run("source map not flagged as unreachable", func(t *testing.T) {
		// a.js references a.js.map via sourceMappingURL — it should be considered reachable
		os.WriteFile(filepath.Join(dir, "a.js"), []byte("export const a = 1;\n//# sourceMappingURL=a.js.map"), 0o644)
		os.WriteFile(filepath.Join(dir, "a.js.map"), []byte(`{"version":3}`), 0o644)
		os.WriteFile(filepath.Join(outDir, "importmap.json"), []byte(`{"imports":{"a":"/example.com/a.js"}}`), 0o644)
		c := &cfg.Config{
			Imports: map[string]string{"a": "https://example.com/a.js"},
			Unpm:    cfg.Options{Out: outDir, Root: "/"},
		}

		// Capture stderr to check for spurious warnings
		oldStderr := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w

		err := unpm.Check(c)

		w.Close()
		var buf [4096]byte
		n, _ := r.Read(buf[:])
		os.Stderr = oldStderr
		stderr := string(buf[:n])

		if err != nil {
			t.Fatalf("expected check to pass: %v", err)
		}
		if strings.Contains(stderr, "a.js.map") {
			t.Fatalf("source map should not be flagged as unreachable, got: %s", stderr)
		}

		// Restore original a.js for subsequent tests
		os.WriteFile(filepath.Join(dir, "a.js"), []byte(`import { b } from "./b.js"; export const a = b;`), 0o644)
		os.Remove(filepath.Join(dir, "a.js.map"))
	})

	t.Run("bare import not in map", func(t *testing.T) {
		os.WriteFile(filepath.Join(dir, "c.js"), []byte(`import { x } from "unknown-pkg"; export const c = x;`), 0o644)
		os.WriteFile(filepath.Join(outDir, "importmap.json"), []byte(`{"imports":{"c":"/example.com/c.js"}}`), 0o644)
		c := &cfg.Config{
			Imports: map[string]string{"c": "https://example.com/c.js"},
			Unpm:    cfg.Options{Out: outDir, Root: "/"},
		}
		err := unpm.Check(c)
		if err == nil {
			t.Fatal("expected check to fail for bare import not in map")
		}
		os.Remove(filepath.Join(dir, "c.js"))
	})
}

func TestVendor_CleansOldFiles(t *testing.T) {
	srv := newTestServer(map[string]testFile{
		"/a.js": {body: `export const a = 1;`},
	})
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	root := t.TempDir()
	outDir := filepath.Join(root, "vendor")

	// Pre-create stale files that should be cleaned up
	staleDir := filepath.Join(outDir, "old-host.com")
	os.MkdirAll(staleDir, 0o755)
	os.WriteFile(filepath.Join(staleDir, "stale.js"), []byte(`export const stale = 1;`), 0o644)

	c := &cfg.Config{
		Imports: map[string]string{"a": srv.URL + "/a.js"},
		Unpm:    cfg.Options{Out: outDir, Root: "/"},
	}
	if _, err := unpm.Vendor(c); err != nil {
		t.Fatal(err)
	}

	// a.js should exist
	if _, err := os.Stat(filepath.Join(outDir, host, "a.js")); err != nil {
		t.Fatal("a.js should be downloaded")
	}

	// stale file and its directory should be removed
	if _, err := os.Stat(filepath.Join(staleDir, "stale.js")); !os.IsNotExist(err) {
		t.Fatal("stale.js should be removed")
	}
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatal("empty stale directory should be removed")
	}
}

func TestWhy(t *testing.T) {
	outDir := t.TempDir()
	host := "example.com"

	dir := filepath.Join(outDir, host)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "entry.js"), []byte(`import { dep } from "./dep.js"; export default dep;`), 0o644)
	os.WriteFile(filepath.Join(dir, "dep.js"), []byte(`export const dep = 1;`), 0o644)
	os.WriteFile(filepath.Join(outDir, "importmap.json"), []byte(`{"imports":{"entry":"/example.com/entry.js"}}`), 0o644)

	c := &cfg.Config{
		Imports: map[string]string{"entry": "https://example.com/entry.js"},
		Unpm:    cfg.Options{Out: outDir, Root: "/"},
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
