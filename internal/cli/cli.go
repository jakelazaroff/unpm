// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// Copyright (c) 2026 Jake Lazaroff https://github.com/jakelazaroff/unpm

package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jakelazaroff/unpm/internal/cfg"
)

// stringSlice implements flag.Value so a flag can be repeated.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ", ") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

type App struct {
	Vendor func(*cfg.Config) ([]string, error)
	Check  func(*cfg.Config) error
	Why    func(*cfg.Config, string) error
}

// Run parses args and executes the appropriate command, writing output to
// stdout and stderr. It returns the exit code.
func (a *App) Run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 || args[1] == "help" {
		fmt.Fprintf(stderr, "usage: unpm <command> [flags]\n\ncommands:\n  vendor  download and vendor imports\n  check   warn about import map issues\n  why     explain why a file is vendored\n")
		return 1
	}

	cmd := args[1]

	var config, out, root string
	var pin stringSlice
	var verbose bool
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&config, "config", "unpm.json", "path to config JSON file")
	fs.StringVar(&out, "out", "", "output directory")
	fs.StringVar(&root, "root", "", "root directory for import map paths")
	fs.Var(&pin, "pin", "pin a file relative to the output directory (can be repeated)")
	fs.BoolVar(&verbose, "verbose", false, "show detailed output")

	// reorder args so flags can appear anywhere
	rawArgs := args[2:]
	var flagArgs, posArgs []string
	for i := 0; i < len(rawArgs); i++ {
		if strings.HasPrefix(rawArgs[i], "-") {
			flagArgs = append(flagArgs, rawArgs[i])
			name := strings.TrimLeft(rawArgs[i], "-")
			if !strings.Contains(name, "=") && i+1 < len(rawArgs) && !strings.HasPrefix(rawArgs[i+1], "-") {
				if f := fs.Lookup(name); f != nil {
					i++
					flagArgs = append(flagArgs, rawArgs[i])
				}
			}
		} else {
			posArgs = append(posArgs, rawArgs[i])
		}
	}
	if err := fs.Parse(append(flagArgs, posArgs...)); err != nil {
		return 1
	}

	switch cmd {
	case "vendor", "check", "why":
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n", cmd)
		return 1
	}

	c, err := cfg.ReadConfig(config)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	if out != "" {
		c.Unpm.Out = out
	}
	if root != "" {
		c.Unpm.Root = root
	}
	c.Unpm.Pin = append(c.Unpm.Pin, pin...)
	c.Unpm.Verbose = verbose

	switch cmd {
	case "vendor":
		var stop func()
		if !verbose {
			stop = spinner(stdout)
		}
		warnings, err := a.Vendor(c)
		if stop != nil {
			stop()
			fmt.Fprint(stdout, "\r\033[K")
		}
		for _, w := range warnings {
			fmt.Fprintf(stderr, "\033[33mwarning:\033[0m %s\n", w)
		}
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "done.")

	case "check":
		if err := a.Check(c); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "done.")

	case "why":
		if fs.NArg() < 1 {
			fmt.Fprintf(stderr, "usage: unpm why <file>\n")
			return 1
		}
		if err := a.Why(c, fs.Arg(0)); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
	}

	return 0
}

func spinner(w io.Writer) func() {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	done := make(chan struct{})
	go func() {
		i := 0
		for {
			select {
			case <-done:
				return
			default:
				fmt.Fprintf(w, "\r%s", frames[i%len(frames)])
				i++
				time.Sleep(80 * time.Millisecond)
			}
		}
	}()
	return func() { close(done) }
}
