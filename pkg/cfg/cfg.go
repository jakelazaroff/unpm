// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// Copyright (c) 2026 Jake Lazaroff https://github.com/jakelazaroff/unpm

package cfg

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type Config struct {
	Imports map[string]string `json:"imports"`
	Unpm    Options           `json:"$unpm"`
}

type Options struct {
	Out     string   `json:"out,omitempty"`
	Root    string   `json:"root,omitempty"`
	Pin     []string `json:"pin,omitempty"`
	Verbose bool     `json:"-"`
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

	// apply defaults, resolved relative to the config file's directory
	dir := filepath.Dir(configPath)
	if cfg.Unpm.Out == "" {
		cfg.Unpm.Out = filepath.Join(dir, "vendor")
	} else {
		cfg.Unpm.Out = filepath.Join(dir, cfg.Unpm.Out)
	}
	if cfg.Unpm.Root == "" {
		rel, err := filepath.Rel(dir, cfg.Unpm.Out)
		if err != nil {
			rel = filepath.Base(cfg.Unpm.Out)
		}
		cfg.Unpm.Root = "/" + filepath.ToSlash(rel)
	}

	return &cfg, nil
}

// IsPinned reports whether the given file path (relative to the output
// directory) matches any pin glob pattern.
func (c *Config) IsPinned(relPath string) bool {
	for _, pattern := range c.Unpm.Pin {
		if matchGlob(pattern, relPath) {
			return true
		}
	}

	return false
}

// matchGlob matches a glob pattern against a forward-slash path. It supports
// path.Match syntax plus "**" to match zero or more path segments.
func matchGlob(pattern, name string) bool {
	patParts := strings.Split(pattern, "/")
	nameParts := strings.Split(name, "/")
	return matchParts(patParts, nameParts)
}

func matchParts(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			pat = pat[1:]
			if len(pat) == 0 {
				return true // trailing ** matches everything
			}
			// Try matching the rest of the pattern at every position
			for i := 0; i <= len(name); i++ {
				if matchParts(pat, name[i:]) {
					return true
				}
			}
			return false
		}

		if len(name) == 0 {
			return false
		}

		matched, _ := path.Match(pat[0], name[0])
		if !matched {
			return false
		}

		pat = pat[1:]
		name = name[1:]
	}

	return len(name) == 0
}
