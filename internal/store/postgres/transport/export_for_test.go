// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

// SetAfterCommitForTest sets the push commit seam of p (MTIX-95.3, review
// F-80): hook runs after each push transaction's COMMIT succeeded, and a
// non-nil error it returns is reported in place of the commit's, as a lost
// commit acknowledgement would be. Compiled into tests only; the seam is a
// field of this Pool, never package state.
func SetAfterCommitForTest(p *Pool, hook func() error) {
	p.afterCommit = hook
}

// SetAfterSessionLockForTest sets the session-lock seam of p (MTIX-107.85):
// hook runs right after EnsureRegistryIndex takes its session advisory
// lock. Compiled into tests only; the seam is a field of this Pool, never
// package state.
func SetAfterSessionLockForTest(p *Pool, hook func()) {
	p.afterSessionLock = hook
}
