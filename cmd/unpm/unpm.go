// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// Copyright (c) 2026 Jake Lazaroff

package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/jakelazaroff/unpm/pkg/unpm"
)

const defaultOut = "vendor"

func main() {
	fetchCmd := flag.NewFlagSet("fetch", flag.ExitOnError)
	fetchConfig := fetchCmd.String("i", "unpm.json", "path to import map JSON file")
	fetchOut := fetchCmd.String("o", defaultOut, "output directory")

	checkCmd := flag.NewFlagSet("check", flag.ExitOnError)
	checkConfig := checkCmd.String("i", "unpm.json", "path to import map JSON file")
	checkOut := checkCmd.String("o", defaultOut, "vendor directory to check")

	pruneCmd := flag.NewFlagSet("prune", flag.ExitOnError)
	pruneConfig := pruneCmd.String("i", "unpm.json", "path to import map JSON file")
	pruneOut := pruneCmd.String("o", defaultOut, "vendor directory to prune")

	whyCmd := flag.NewFlagSet("why", flag.ExitOnError)
	whyConfig := whyCmd.String("i", "unpm.json", "path to import map JSON file")
	whyOut := whyCmd.String("o", defaultOut, "vendor directory")

	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: unpm <command> [flags]\n\ncommands:\n  fetch   download and vendor imports\n  check   warn about bare imports missing from import map\n  prune   remove unreachable vendored files\n  why     explain why a transitive dependency is imported\n")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "fetch":
		fetchCmd.Parse(os.Args[2:])
		cfg, err := unpm.ReadConfig(*fetchConfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		outDir := unpm.OutDir(cfg, *fetchConfig, *fetchOut, defaultOut)
		fmt.Println("unpm: fetching imports...")
		if err := unpm.Fetch(cfg, outDir); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("done.")

	case "check":
		checkCmd.Parse(os.Args[2:])
		cfg, err := unpm.ReadConfig(*checkConfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		outDir := unpm.OutDir(cfg, *checkConfig, *checkOut, defaultOut)
		fmt.Println("unpm: checking imports...")
		if err := unpm.Check(cfg, outDir); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("done.")

	case "prune":
		pruneCmd.Parse(os.Args[2:])
		cfg, err := unpm.ReadConfig(*pruneConfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		outDir := unpm.OutDir(cfg, *pruneConfig, *pruneOut, defaultOut)
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
		cfg, err := unpm.ReadConfig(*whyConfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		outDir := unpm.OutDir(cfg, *whyConfig, *whyOut, defaultOut)
		if err := unpm.Why(cfg, outDir, whyCmd.Arg(0)); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}
