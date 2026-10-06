// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

// SetOnCommit registers fn as the FIRST post-commit callback, replacing any
// previously set via SetOnCommit but preserving callbacks added with
// AddOnCommit. Must be called during process wiring, before concurrent use.
// Every long-running interface (anything that mutates without exiting) MUST
// wire the auto-export path here, or its users lose the FR-15.3 mirror — the
// gap behind the 2026-05-19 data-loss incident.
func (s *Store) SetOnCommit(fn func()) {
	if len(s.onCommit) == 0 {
		s.onCommit = []func(){fn}
		return
	}
	s.onCommit[0] = fn
}

// AddOnCommit appends fn to the post-commit callbacks (MTIX-53), so a server can
// wire hook dispatch alongside the mirror exporter without one replacing the
// other. Must be called during wiring, before concurrent use.
func (s *Store) AddOnCommit(fn func()) {
	if len(s.onCommit) == 0 {
		// Reserve slot 0 for SetOnCommit so a later SetOnCommit does not
		// displace this callback.
		s.onCommit = []func(){nil}
	}
	s.onCommit = append(s.onCommit, fn)
}
