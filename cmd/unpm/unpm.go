// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// Copyright (c) 2026 Jake Lazaroff https://github.com/jakelazaroff/unpm

package main

import (
	"os"

	"github.com/jakelazaroff/unpm/internal/cli"
	"github.com/jakelazaroff/unpm/internal/unpm"
)

func main() {
	app := cli.App{
		Vendor: unpm.Vendor,
		Check:  unpm.Check,
		Why:    unpm.Why,
	}

	os.Exit(app.Run(os.Args, os.Stdout, os.Stderr))
}
