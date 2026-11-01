// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package channel

import "time"

// Positive completions return as soon as ready; this only bounds a real hang.
const channelCompletionHangBound = 10 * time.Second
