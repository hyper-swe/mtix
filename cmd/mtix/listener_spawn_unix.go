// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import "syscall"

// listenerSysProcAttr puts the listener in its own session so the waiter
// that started it can end without taking it down.
func listenerSysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
