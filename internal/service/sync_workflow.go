// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"github.com/hyper-swe/mtix/internal/sync/workflow"
)

// DetectState owns local sync workflow reads per FR-18.17. It opens no hub
// connection and leaves redaction/rendering to the existing workflow contract.
func (s *SyncService) DetectState(ctx context.Context, mtixDir string) (workflow.Report, error) {
	report, err := workflow.DetectState(ctx, s.store.ReadDB(), mtixDir)
	if err != nil {
		return workflow.Report{}, wrapAPIOperation("detect sync workflow", err)
	}
	return report, nil
}
