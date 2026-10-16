// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"github.com/hyper-swe/mtix/internal/oplocal"
	"os"
)

func hookStdinTerminal() bool {
	return oplocal.OperatorTerminal(os.Stdin)
}
