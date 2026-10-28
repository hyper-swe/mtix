// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

// HexIDListForTest and IDListSubqueryForTest expose the id-list helper to the
// external test package (MTIX-187.7.15).
var HexIDListForTest = hexIDList

const IDListSubqueryForTest = idListSubquery

// PendingByIDSQLForTest and ReleasePushHoldsSQLForTest expose the real
// statements so plan tests run them under EXPLAIN QUERY PLAN (rule 1b).
const (
	PendingByIDSQLForTest      = pendingByIDSQL
	ReleasePushHoldsSQLForTest = releasePushHoldsSQL
	MissingLocalIDsSQLForTest  = missingLocalEventIDsSQL
)
