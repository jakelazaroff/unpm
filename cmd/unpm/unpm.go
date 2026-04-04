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

	"github.com/jakelazaroff/unpm/pkg/unpm"
)

// stringSlice implements flag.Value so a flag can be repeated.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ", ") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

const defaultOut = "vendor"
const defaultRoot = "."

func main() {
	var fetchConfigVal, fetchOutVal, fetchRootVal string
	var fetchPinVal stringSlice
	fetchCmd := flag.NewFlagSet("fetch", flag.ExitOnError)
	fetchCmd.StringVar(&fetchConfigVal, "config", "unpm.json", "path to config JSON file")
	fetchCmd.StringVar(&fetchOutVal, "out", defaultOut, "output directory")
	fetchCmd.StringVar(&fetchRootVal, "root", defaultRoot, "root directory for import map paths")
	fetchCmd.Var(&fetchPinVal, "pin", "pin a module specifier (can be repeated)")

	var checkConfigVal, checkOutVal string
	checkCmd := flag.NewFlagSet("check", flag.ExitOnError)
	checkCmd.StringVar(&checkConfigVal, "config", "unpm.json", "path to config JSON file")
	checkCmd.StringVar(&checkOutVal, "out", defaultOut, "vendor directory to check")

	var pruneConfigVal, pruneOutVal string
	var prunePinVal stringSlice
	pruneCmd := flag.NewFlagSet("prune", flag.ExitOnError)
	pruneCmd.StringVar(&pruneConfigVal, "config", "unpm.json", "path to config JSON file")
	pruneCmd.StringVar(&pruneOutVal, "out", defaultOut, "vendor directory to prune")
	pruneCmd.Var(&prunePinVal, "pin", "pin a module specifier (can be repeated)")

	var whyConfigVal, whyOutVal string
	whyCmd := flag.NewFlagSet("why", flag.ExitOnError)
	whyCmd.StringVar(&whyConfigVal, "config", "unpm.json", "path to config JSON file")
	whyCmd.StringVar(&whyOutVal, "out", defaultOut, "vendor directory")

	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: unpm <command> [flags]\n\ncommands:\n  fetch   download and vendor imports\n  check   warn about bare imports missing from import map\n  prune   remove unreachable vendored files\n  why     explain why a transitive dependency is imported\n")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "fetch":
		fetchCmd.Parse(os.Args[2:])
		cfg, err := unpm.ReadConfig(fetchConfigVal)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		unpm.MergePin(cfg, []string(fetchPinVal))
		outDir := unpm.OutDir(cfg, fetchConfigVal, fetchOutVal, defaultOut)
		root := unpm.Root(cfg, fetchConfigVal, fetchRootVal, defaultRoot)
		fmt.Println("unpm: fetching imports...")
		if err := unpm.Fetch(cfg, outDir, root); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("done.")

	case "check":
		checkCmd.Parse(os.Args[2:])
		cfg, err := unpm.ReadConfig(checkConfigVal)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		outDir := unpm.OutDir(cfg, checkConfigVal, checkOutVal, defaultOut)
		fmt.Println("unpm: checking imports...")
		if err := unpm.Check(cfg, outDir); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("done.")

	case "prune":
		pruneCmd.Parse(os.Args[2:])
		cfg, err := unpm.ReadConfig(pruneConfigVal)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		unpm.MergePin(cfg, []string(prunePinVal))
		outDir := unpm.OutDir(cfg, pruneConfigVal, pruneOutVal, defaultOut)
		fmt.Println("unpm: pruning vendor...")
		if err := unpm.Prune(cfg, outDir); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("done.")

	case "why":
		whyCmd.Parse(os.Args[2:])
		if whyCmd.NArg() < 1 {
			fmt.Fprintf(os.Stderr, "usage: unpm why <file>\n")
			os.Exit(1)
		}
		cfg, err := unpm.ReadConfig(whyConfigVal)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		outDir := unpm.OutDir(cfg, whyConfigVal, whyOutVal, defaultOut)
		if err := unpm.Why(cfg, outDir, whyCmd.Arg(0)); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}
