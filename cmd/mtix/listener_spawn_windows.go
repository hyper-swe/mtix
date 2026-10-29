// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package main

import "syscall"

// detachedProcess is the Windows DETACHED_PROCESS creation flag: no console.
const detachedProcess = 0x00000008

// listenerSysProcAttr detaches the listener from the waiter's console, so
// closing that console does not end a listener other waiters still use.
func listenerSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess}
}
