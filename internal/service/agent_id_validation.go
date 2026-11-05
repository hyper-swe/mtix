// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import "github.com/hyper-swe/mtix/internal/model"

func validateAgentID(id string) error { return model.ValidateAgentID(id) }
