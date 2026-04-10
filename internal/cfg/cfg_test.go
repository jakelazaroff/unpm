// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// Copyright (c) 2026 Jake Lazaroff https://github.com/jakelazaroff/unpm

package cfg_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jakelazaroff/unpm/internal/cfg"
)

func TestReadConfig(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "unpm.json")
		os.WriteFile(p, []byte(`{"imports":{"foo":"https://example.com/foo.js"}}`), 0o644)

		c, err := cfg.ReadConfig(p)
		if err != nil {
			t.Fatal(err)
		}
		if c.Imports["foo"] != "https://example.com/foo.js" {
			t.Fatalf("unexpected import: %v", c.Imports)
		}
	})

	t.Run("no imports", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "unpm.json")
		os.WriteFile(p, []byte(`{}`), 0o644)

		_, err := cfg.ReadConfig(p)
		if err == nil || !strings.Contains(err.Error(), "no imports") {
			t.Fatalf("expected 'no imports' error, got: %v", err)
		}
	})

	t.Run("trailing slash", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "unpm.json")
		os.WriteFile(p, []byte(`{"imports":{"foo/":"https://example.com/foo.js"}}`), 0o644)

		_, err := cfg.ReadConfig(p)
		if err == nil || !strings.Contains(err.Error(), "trailing-slash") {
			t.Fatalf("expected trailing-slash error, got: %v", err)
		}
	})

	t.Run("non-https", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "unpm.json")
		os.WriteFile(p, []byte(`{"imports":{"foo":"http://example.com/foo.js"}}`), 0o644)

		_, err := cfg.ReadConfig(p)
		if err == nil || !strings.Contains(err.Error(), "https://") {
			t.Fatalf("expected https error, got: %v", err)
		}
	})

	t.Run("default out and root", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "unpm.json")
		os.WriteFile(p, []byte(`{"imports":{"foo":"https://example.com/foo.js"}}`), 0o644)

		c, err := cfg.ReadConfig(p)
		if err != nil {
			t.Fatal(err)
		}
		if c.Unpm.Out != filepath.Join(dir, "vendor") {
			t.Fatalf("expected Out=%q, got %q", filepath.Join(dir, "vendor"), c.Unpm.Out)
		}
		if c.Unpm.Root != "/vendor" {
			t.Fatalf("expected Root=%q, got %q", "/vendor", c.Unpm.Root)
		}
	})

	t.Run("custom out and root", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "unpm.json")
		os.WriteFile(p, []byte(`{"imports":{"foo":"https://example.com/foo.js"},"unpm":{"out":"dist","root":"/assets/dist"}}`), 0o644)

		c, err := cfg.ReadConfig(p)
		if err != nil {
			t.Fatal(err)
		}
		if c.Unpm.Out != filepath.Join(dir, "dist") {
			t.Fatalf("expected Out=%q, got %q", filepath.Join(dir, "dist"), c.Unpm.Out)
		}
		if c.Unpm.Root != "/assets/dist" {
			t.Fatalf("expected Root=%q, got %q", "/assets/dist", c.Unpm.Root)
		}
	})
}

func TestIsPinned(t *testing.T) {
	tests := []struct {
		name     string
		pin      []string
		path     string
		expected bool
	}{
		{"exact match", []string{"esm.sh/preact.js"}, "esm.sh/preact.js", true},
		{"exact no match", []string{"esm.sh/preact.js"}, "esm.sh/other.js", false},
		{"star in segment", []string{"esm.sh/*.js"}, "esm.sh/preact.js", true},
		{"star does not cross slash", []string{"esm.sh/*.js"}, "esm.sh/sub/preact.js", false},
		{"doublestar trailing", []string{"esm.sh/**"}, "esm.sh/preact.js", true},
		{"doublestar nested", []string{"esm.sh/**"}, "esm.sh/v10/es2022/preact.mjs", true},
		{"doublestar middle", []string{"esm.sh/**/preact.mjs"}, "esm.sh/v10/es2022/preact.mjs", true},
		{"doublestar middle no match", []string{"esm.sh/**/preact.mjs"}, "esm.sh/v10/es2022/hooks.mjs", false},
		{"doublestar zero segments", []string{"esm.sh/**/preact.mjs"}, "esm.sh/preact.mjs", true},
		{"no match different host", []string{"esm.sh/**"}, "cdn.js/preact.js", false},
		{"multiple patterns", []string{"esm.sh/**", "cdn.js/foo.js"}, "cdn.js/foo.js", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &cfg.Config{Unpm: cfg.Options{Pin: tt.pin}}
			if got := c.IsPinned(tt.path); got != tt.expected {
				t.Fatalf("IsPinned(%q) with pin=%v: got %v, want %v", tt.path, tt.pin, got, tt.expected)
			}
		})
	}
}
