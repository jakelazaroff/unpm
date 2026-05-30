// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// Copyright (c) 2026 Jake Lazaroff https://github.com/jakelazaroff/unpm

package inspect_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jakelazaroff/unpm/internal/cfg"
	"github.com/jakelazaroff/unpm/internal/inspect"
)

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
		if err := inspect.Check(c); err != nil {
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
		err := inspect.Check(c)
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

		err := inspect.Check(c)

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
		err := inspect.Check(c)
		if err == nil {
			t.Fatal("expected check to fail for bare import not in map")
		}
		os.Remove(filepath.Join(dir, "c.js"))
	})
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
	if err := inspect.Why(c, "example.com/dep.js"); err != nil {
		t.Fatalf("expected Why to find chain: %v", err)
	}

	// Should fail for a file not in the chain
	if err := inspect.Why(c, "example.com/nonexistent.js"); err == nil {
		t.Fatal("expected Why to fail for nonexistent target")
	}
}
