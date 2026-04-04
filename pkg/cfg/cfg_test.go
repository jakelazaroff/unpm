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

	"github.com/jakelazaroff/unpm/pkg/cfg"
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
		if c.Unpm.Root != dir {
			t.Fatalf("expected Root=%q, got %q", dir, c.Unpm.Root)
		}
	})

	t.Run("custom out and root", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "unpm.json")
		os.WriteFile(p, []byte(`{"imports":{"foo":"https://example.com/foo.js"},"$unpm":{"out":"dist","root":"public"}}`), 0o644)

		c, err := cfg.ReadConfig(p)
		if err != nil {
			t.Fatal(err)
		}
		if c.Unpm.Out != filepath.Join(dir, "dist") {
			t.Fatalf("expected Out=%q, got %q", filepath.Join(dir, "dist"), c.Unpm.Out)
		}
		if c.Unpm.Root != filepath.Join(dir, "public") {
			t.Fatalf("expected Root=%q, got %q", filepath.Join(dir, "public"), c.Unpm.Root)
		}
	})
}

func TestIsPinned(t *testing.T) {
	c := &cfg.Config{
		Imports: map[string]string{
			"a": "https://x.com/a.js",
			"b": "https://x.com/b.js",
		},
		Unpm: cfg.Options{Pin: []string{"b"}},
	}

	if c.IsPinned("a") {
		t.Fatal("a should not be pinned")
	}
	if !c.IsPinned("b") {
		t.Fatal("b should be pinned")
	}
}
