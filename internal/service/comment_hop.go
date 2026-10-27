// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"log/slog"
	"os"
	"strconv"
)

// addressedCommentHop derives process-carried context per FR-20.3 and ruling10044.
// It never infers a turn from retained ledger rows, acknowledgements or time.
func addressedCommentHop(to string, logger *slog.Logger) int64 {
	if to == "" {
		return 0
	}
	value, set := os.LookupEnv("MTIX_WAKE_HOP")
	if !set {
		return 0
	}
	hop, err := strconv.ParseInt(value, 10, 64)
	if err != nil || hop < 0 || hop == 1<<63-1 {
		logger.Warn("invalid MTIX_WAKE_HOP; addressed send starts at hop 0")
		return 0
	}
	return hop + 1
}
