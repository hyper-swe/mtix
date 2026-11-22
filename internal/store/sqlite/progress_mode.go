// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"database/sql"
	"sync"
)

// txWeighted records, per open write transaction, whether the progress.weighted
// switch (FR-5.8) was on when the transaction began. recomputeNodeProgress
// reads it so every rollup inside one transaction uses one mode, whichever
// code path (transition, claim, sync apply, import) reaches the rollup.
var txWeighted sync.Map // *sql.Tx -> bool

// SetProgressWeightedSource installs the reader of the progress.weighted
// switch (config key, default off). Nil, the default, means off: parent
// progress is count(done children) / count(included children) per FR-5.3.
// Call once while wiring the store.
func (s *Store) SetProgressWeightedSource(on func() bool) { s.progressWeighted = on }

func (s *Store) progressWeightedOn() bool { return s.progressWeighted != nil && s.progressWeighted() }

// markProgressMode registers tx's progress mode and returns the release func.
func (s *Store) markProgressMode(tx *sql.Tx) func() {
	txWeighted.Store(tx, s.progressWeightedOn())
	return func() { txWeighted.Delete(tx) }
}

func weightedFor(tx *sql.Tx) bool {
	v, ok := txWeighted.Load(tx)
	on, _ := v.(bool)
	return ok && on
}
