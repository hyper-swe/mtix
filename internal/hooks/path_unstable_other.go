// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package hooks

// unstableLink reports whether a link names a different file for each
// process. Only the Linux per-process pseudo filesystem does (FR-19.2).
func unstableLink(string) bool { return false }
