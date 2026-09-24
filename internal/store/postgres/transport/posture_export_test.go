// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import "github.com/jackc/pgx/v5/pgxpool"

// PoolConfigForTest exposes poolConfig, the step of NewWithDefaults that
// turns an Approval into the configuration handed to pgxpool, to the
// external test package (MTIX-95.25).
func PoolConfigForTest(a *Approval, defs PoolDefaults) *pgxpool.Config {
	return poolConfig(a, defs)
}

// DSNForTest returns the normalized DSN an Approval carries, which the
// package keeps unexported (MTIX-95.25).
func (a *Approval) DSNForTest() string { return a.dsn }
