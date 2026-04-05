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
	"path/filepath"
	"slices"
	"strings"
)

type Config struct {
	Imports map[string]string `json:"imports"`
	Unpm    Options           `json:"$unpm"`
}

type Options struct {
	Out  string   `json:"out,omitempty"`
	Root string   `json:"root,omitempty"`
	Pin  []string `json:"pin,omitempty"`
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
		cfg.Unpm.Root = dir
	} else {
		cfg.Unpm.Root = filepath.Join(dir, cfg.Unpm.Root)
	}

	return &cfg, nil
}

// IsPinned reports whether the given import key is pinned.
func (c *Config) IsPinned(key string) bool {
	return slices.Contains(c.Unpm.Pin, key)
}
