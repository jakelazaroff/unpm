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
	// print help if necessary
	if len(os.Args) < 2 || os.Args[1] == "help" {
		fmt.Fprintf(os.Stderr, "usage: unpm <command> [flags]\n\ncommands:\n  fetch   download and vendor imports\n  check   warn about import map issues\n  prune   remove unreachable vendored files\n  why     explain why a file is vendored\n")
		os.Exit(1)
	}

	// get the command
	cmd := os.Args[1]

	// get any flags
	var config, out, root string
	var pin stringSlice
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.StringVar(&config, "config", "unpm.json", "path to config JSON file")
	fs.StringVar(&out, "out", "", "output directory")
	fs.StringVar(&root, "root", "", "root directory for import map paths")
	fs.Var(&pin, "pin", "pin a file relative to the output directory (can be repeated)")
	// reorder args so flags can appear anywhere
	args := os.Args[2:]
	var flagArgs, posArgs []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flagArgs = append(flagArgs, args[i])
			// if the flag has a separate value (not --flag=val), consume the next arg too
			name := strings.TrimLeft(args[i], "-")
			if !strings.Contains(name, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				// check if this is a known flag that takes a value
				if f := fs.Lookup(name); f != nil {
					i++
					flagArgs = append(flagArgs, args[i])
				}
			}
		} else {
			posArgs = append(posArgs, args[i])
		}
	}
	fs.Parse(append(flagArgs, posArgs...))

	// ensure the command is valid
	switch cmd {
	case "fetch", "check", "prune", "why":
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		os.Exit(1)
	}

	// read the config file
	c, err := cfg.ReadConfig(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// have flags override any config options
	if out != "" {
		c.Unpm.Out = out
	}
	if root != "" {
		c.Unpm.Root = root
	}
	c.Unpm.Pin = append(c.Unpm.Pin, pin...)

	// run the command
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
