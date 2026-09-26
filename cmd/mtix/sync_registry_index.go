// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// registryIndexRebuild is the fix for a node-number registry index that is
// not valid or not ready: mtix sync migrate --yes, run as the table owner
// while the version gate is open, drops the index and builds it again
// (MTIX-95.44).
const registryIndexRebuild = "run mtix sync migrate --yes as the table owner while the version gate is open; " +
	"it drops the index and builds it again"

// registryIndexNotUsable describes a registry index that is not valid or
// not ready, with its pg_index flags and what it still does: one that is
// not ready checks no new create; one that is ready but not valid still
// refuses a duplicate create, but queries do not use it and it must be
// built again (MTIX-95.44).
func registryIndexNotUsable(s transport.RegistryIndexState) string {
	what := "it checks no new create"
	if s.Ready {
		what = "it still refuses a duplicate create, but queries do not use it and it must be built again"
	}
	return fmt.Sprintf("the node-number registry index %s is not valid or not ready "+
		"(indisvalid %t, indisready %t): %s", s.QualifiedName(), s.Valid, s.Ready, what)
}

// withRegistryIndexFix adds the exact fix to a migration error that is
// PostgreSQL's refusal to build the registry index over duplicate creates
// (migration 009 on a hub without the index), and returns any other error
// as it is (MTIX-95.44).
func withRegistryIndexFix(err error) error {
	if !transport.IsRegistryIndexConflict(err) {
		return err
	}
	return fmt.Errorf("%w; the hub holds duplicate creates, so migration 009 cannot build the node-number "+
		"registry index: as the table owner, run mtix sync migrate --yes while the version gate is open, which "+
		"records the duplicates and builds the index without them, then run mtix sync init again", err)
}

// warnRegistryIndex checks the registry index after mtix sync init's
// migrations, whose CREATE UNIQUE INDEX IF NOT EXISTS skips an index of the
// same name that is not valid, and prints a WARN with the exact fix when
// the index is not valid or not ready (MTIX-95.44).
func warnRegistryIndex(ctx context.Context, stderr io.Writer, pool *transport.Pool) {
	state, err := pool.RegistryIndex(ctx)
	if err != nil {
		warnSync(stderr, "WARN: node-number registry index check skipped", err)
		return
	}
	if state.NotUsable() {
		fmt.Fprintf(stderr, "WARN: %s; fix: %s\n", registryIndexNotUsable(state), registryIndexRebuild)
	}
}

// registryIndexGap is the doctor's gap and fix step for a registry index
// that is not valid or not ready, or "" for an index that is valid and
// ready or absent (MTIX-95.44).
func registryIndexGap(s transport.RegistryIndexState) (gap, step string) {
	if !s.NotUsable() {
		return "", ""
	}
	return registryIndexNotUsable(s), "mtix sync migrate --yes while the version gate is open " +
		"(it drops the index and builds it again)"
}
