// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"os"
	"strconv"
)

func wakeAckUserID() (string, error) { return strconv.Itoa(os.Geteuid()), nil }
