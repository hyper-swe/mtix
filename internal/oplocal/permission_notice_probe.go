// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import "context"

// WithPermissionNoticeProbe returns an independent accessor with a read-only
// notice probe. State contains no locks; mutation still takes the host file lock.
func (s *State) WithPermissionNoticeProbe(probe func(context.Context, PermissionBatch) (bool, error)) *State {
	accessor := *s
	accessor.permissionNoticeProbe = probe
	return &accessor
}
