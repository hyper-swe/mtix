// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

// Windows does not support Go's directory File.Sync. Requests and publication
// files still use File.Sync and Close, ensuring process-restart persistence.
// Power-loss durability of directory entries is not promised on Windows.
func syncExportDirectory(_ string) error { return nil }
