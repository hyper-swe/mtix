// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

// SetProgressWeightedSource installs the reader of the progress.weighted
// switch (config key, default off). Nil, the default, means off: parent
// progress is count(done children) / count(included children) per FR-5.3.
// Call once while wiring the store. Each rollup receives the mode as an
// argument; there is no package-level state.
func (s *Store) SetProgressWeightedSource(on func() bool) { s.progressWeighted = on }

func (s *Store) progressWeightedOn() bool { return s.progressWeighted != nil && s.progressWeighted() }
