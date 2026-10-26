// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import "github.com/hyper-swe/mtix/internal/store/sqlite"

// SetWriterRollbackForTest injects the post-commit-failure rollback (MTIX-198).
func (svc *AgentService) SetWriterRollbackForTest(fn sqlite.RollbackWriterFunc) {
	svc.rollbackWriter = fn
}
