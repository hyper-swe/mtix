// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// importRefusalFile is the board file an import refusal names.
const importRefusalFile = ".mtix/tasks.json"

// localOnlyListed is how many added task ids the local-only notice names.
const localOnlyListed = 5

// ErrRefusalRecordUnreadable marks a refusal record that exists but cannot be
// read or parsed, so the doctor can tell it from an unreadable board.
var ErrRefusalRecordUnreadable = errors.New("import refusal record unreadable")

// ImportRefusalStatus is what the doctor reports about the last automatic
// import mtix refused (MTIX-112.17.16.16).
type ImportRefusalStatus struct {
	// Pending is true while the refusal still holds for the tasks.json on disk.
	Pending bool
	// Kind is what stopped the import (lossy, conflict, newer_schema, ...).
	Kind string
	// File is the refused board file, relative to the project root.
	File string
	// Reason says why the import was refused.
	Reason string
	// Fix is the exact way out, as resolutionFor words it for the kind.
	Fix string
}

// PendingImportRefusal reports the refusal of an automatic import that
// still awaits the user's decision, with its kind, the file and the exact
// fix (resolutionFor). Nothing pending, or no tasks.json, is a zero status.
func (s *SyncService) PendingImportRefusal(mtixDir string) (ImportRefusalStatus, error) {
	// #nosec G304 G703 -- caller selects the local project directory; the filename is fixed.
	fileBytes, err := os.ReadFile(filepath.Join(mtixDir, "tasks.json"))
	if os.IsNotExist(err) {
		return ImportRefusalStatus{}, nil
	}
	if err != nil {
		return ImportRefusalStatus{}, fmt.Errorf("read tasks.json: %w", err)
	}
	// A record that cannot be read is an error, never "nothing pending": the
	// refusal it held may still apply, and the check must say so.
	loaded, path, err := s.loadRefusal(mtixDir)
	if err != nil {
		return ImportRefusalStatus{}, fmt.Errorf("read the import refusal record %s: %w: %w", path, ErrRefusalRecordUnreadable, err)
	}
	if loaded == nil {
		return ImportRefusalStatus{}, nil
	}
	refusal := s.protectRestoredInboxRefusal(loaded)
	if !s.refusalPending(mtixDir, refusal, fileBytes) {
		return ImportRefusalStatus{}, nil
	}
	return ImportRefusalStatus{
		Pending: true, Kind: refusal.Kind, File: importRefusalFile,
		Reason: refusal.Reason, Fix: resolutionFor(refusal),
	}, nil
}

// StoreSyncs reports whether the local store syncs with a hub or relay, for
// the import handlers, which refuse a merge or replace there. It fails
// closed: an unreadable store is an error, not "does not sync".
func (s *SyncService) StoreSyncs(ctx context.Context) (bool, error) {
	synced, err := s.store.HasSyncedWithHub(ctx)
	if err != nil {
		return false, fmt.Errorf("read whether the store syncs with a hub: %w", err)
	}
	return synced, nil
}

// StoreHoldsTasks reports whether the local store holds any task, for the
// replace import guard, which lets a replace into an empty store run.
func (s *SyncService) StoreHoldsTasks(ctx context.Context) (bool, error) {
	return s.store.HoldsNodes(ctx)
}

// InboxSafeRecoveryGuide returns the recovery command to name in a refusal,
// or fallback when the store holds no restored inbox that changes it.
func (s *SyncService) InboxSafeRecoveryGuide(ctx context.Context, fallback string) (string, error) {
	return s.store.InboxSafeRecoveryGuide(ctx, fallback)
}

// localOnlyAdded reports whether an import only added tasks on a store that
// syncs (MTIX-112.17.16.16), and the added ids to name (at most
// localOnlyListed, then a count of the rest). A solo store, or an import that
// added nothing, reports false. A
// store that cannot say whether it syncs reports true: saying too much is safe.
func (s *SyncService) localOnlyAdded(ctx context.Context, diff *sqlite.ReplaceDiff, assumeSynced bool) (string, bool) {
	// A task updated by an import is refused earlier on a store that syncs
	// (refuseSyncedUpdates), so an import that reaches here only adds, or
	// also removes tasks no sync event covers; either way the added tasks
	// wrote no sync event.
	if len(diff.Added) == 0 {
		return "", false
	}
	synced := assumeSynced
	if !synced {
		var err error
		if synced, err = s.storeSyncs(ctx); err != nil {
			s.logger.Warn("could not read whether the store syncs, printing the local-only notice", "error", err)
			synced = true
		}
	}
	if !synced {
		return "", false
	}
	ids, more := diff.Added, ""
	if len(ids) > localOnlyListed {
		more = fmt.Sprintf(" and %d more", len(ids)-localOnlyListed)
		ids = ids[:localOnlyListed]
	}
	return strings.Join(ids, ", ") + more, true
}
