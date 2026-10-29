// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import "time"

// Positive completions return as soon as ready; this only bounds a real hang.
const wsCompletionHangBound = 10 * time.Second

// Server.Shutdown may already have closed the hub before fixture cleanup.
func closeWSTestHub(hub *WSHub) {
	select {
	case <-hub.done:
	default:
		hub.Close()
	}
}
