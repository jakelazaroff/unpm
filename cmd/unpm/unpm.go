// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// Copyright (c) 2026 Jake Lazaroff https://github.com/jakelazaroff/unpm

package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jakelazaroff/unpm/pkg/cfg"
	"github.com/jakelazaroff/unpm/pkg/unpm"
)

// stringSlice implements flag.Value so a flag can be repeated.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ", ") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: unpm <command> [flags]\n\ncommands:\n  fetch   download and vendor imports\n  check   warn about bare imports missing from import map\n  prune   remove unreachable vendored files\n  why     explain why a transitive dependency is imported\n")
		os.Exit(1)
	}

	cmd := os.Args[1]

	var configVal, outVal, rootVal string
	var pinVal stringSlice
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.StringVar(&configVal, "config", "unpm.json", "path to config JSON file")
	fs.StringVar(&outVal, "out", "", "output directory")
	fs.StringVar(&rootVal, "root", "", "root directory for import map paths")
	fs.Var(&pinVal, "pin", "pin a module specifier (can be repeated)")
	fs.Parse(os.Args[2:])

	switch cmd {
	case "fetch", "check", "prune", "why":
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		os.Exit(1)
	}

	c, err := cfg.ReadConfig(configVal)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if outVal != "" {
		c.Unpm.Out = outVal
	}
	if rootVal != "" {
		c.Unpm.Root = rootVal
	}
	c.Unpm.Pin = append(c.Unpm.Pin, pinVal...)

	switch cmd {
	case "fetch":
		fmt.Println("unpm: fetching imports...")
		if err := unpm.Fetch(c); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("done.")

	case "check":
		fmt.Println("unpm: checking imports...")
		if err := unpm.Check(c); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("done.")

	case "prune":
		fmt.Println("unpm: pruning vendor...")
		if err := unpm.Prune(c); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("done.")

	case "why":
		if fs.NArg() < 1 {
			fmt.Fprintf(os.Stderr, "usage: unpm why <file>\n")
			os.Exit(1)
		}
		if err := unpm.Why(c, fs.Arg(0)); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}
}
