// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func boardFilePath(root string) string { return filepath.Join(root, ".mtix", "tasks.json") }

// withBoardStore opens the store at dataDir the way the real commands open it
// for an export: history.export follows the worktree's own config and the
// export carries this build's version, so the export is the one the real
// tasks.json is written from (MTIX-112.17.16.6).
func withBoardStore(logger *slog.Logger, root, dataDir string, fn func(*sqlite.Store) error) error {
	cfg, err := service.NewConfigService(filepath.Join(root, ".mtix", "config.yaml"))
	if err != nil {
		return fmt.Errorf("load the worktree config: %w", err)
	}
	return withStoreAt(logger, dataDir, func(st *sqlite.Store) error {
		st.SetHistoryExportSource(cfg.HistoryExport)
		st.SetWriterVersion(version)
		return fn(st)
	})
}

// exportBytes is the store's canonical export as JSON.
func exportBytes(ctx context.Context, st *sqlite.Store) ([]byte, error) {
	exported, err := st.Export(ctx, "", "")
	if err != nil {
		return nil, fmt.Errorf("export the store: %w", err)
	}
	body, err := json.Marshal(exported)
	if err != nil {
		return nil, fmt.Errorf("encode the export: %w", err)
	}
	return body, nil
}

// boardFileFinding proves the first rule of the enrolment: tasks.json is what
// the local store exports, compared by content over everything the export
// carries, with no exclusions, and never by file time. An absent file holds
// nothing to lose. A file that cannot be read or parsed is an error, never a
// pass (MTIX-112.17.16.6).
func boardFileFinding(ctx context.Context, tasksPath string, local *sqlite.Store) (*service.BoardFinding, error) {
	file, err := os.ReadFile(tasksPath) //nolint:gosec // G304: the worktree's own board file
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", tasksPath, err)
	}
	exported, err := exportBytes(ctx, local)
	if err != nil {
		return nil, err
	}
	return service.BoardFileFinding(file, exported)
}

// appliedBound is what a store has applied from the hub: every event at or
// below its pull cursor, plus every event in its own journal (events it
// originated, and events it mirrored when it applied them).
type appliedBound struct {
	cursor transport.PullCursor
	own    map[string]bool
	// snapshot is set for a store that never pulled and holds no journal row: it
	// came from a tasks.json snapshot, so its cursor says nothing about what it
	// holds and the hub rule compares it with the full hub clone (ruling r5
	// point 8). Any journal row, or a pull, keeps the bounded rule (point 9).
	snapshot bool
}

// holds reports whether the store has applied e. The cursor order is the pull's
// own keyset order, (lamport_clock, event_id); an empty cursor event id (a
// Lamport-only cursor) covers only the clocks below it.
func (b appliedBound) holds(e *model.SyncEvent) bool {
	if b.snapshot {
		return true
	}
	if b.own[e.EventID] {
		return true
	}
	if e.LamportClock < b.cursor.Lamport {
		return true
	}
	return e.LamportClock == b.cursor.Lamport && b.cursor.EventID != "" && e.EventID <= b.cursor.EventID
}

// readAppliedBound reads the store's pull cursor and journal. A store that
// never pulled has the empty cursor and holds only what its journal holds.
func readAppliedBound(ctx context.Context, local *sqlite.Store) (appliedBound, error) {
	cursor, err := readLastPulledClock(ctx, local)
	if errors.Is(err, sql.ErrNoRows) {
		cursor, err = transport.PullCursor{}, nil
	}
	if err != nil {
		return appliedBound{}, fmt.Errorf("read the pull cursor: %w", err)
	}
	events, err := local.LocalEvents(ctx)
	if err != nil {
		return appliedBound{}, fmt.Errorf("read the journal: %w", err)
	}
	own := make(map[string]bool, len(events))
	for _, e := range events {
		own[e.ID] = true
	}
	return appliedBound{cursor: cursor, own: own, snapshot: cursor == (transport.PullCursor{}) && len(own) == 0}, nil
}

// boundedHub serves the hub's events that a store has applied and no others, so
// a clone built from it holds exactly what the store could derive from the hub.
type boundedHub struct {
	cursorPuller
	bound appliedBound
}

// PullEvents pages like the hub, skipping events outside the bound. A page that
// holds none of them is not an end of the log: it reads on until it finds one
// or the hub has no more.
func (h boundedHub) PullEvents(ctx context.Context, after transport.PullCursor, limit int) ([]*model.SyncEvent, bool, error) {
	for {
		events, more, err := h.cursorPuller.PullEvents(ctx, after, limit)
		if err != nil {
			return nil, false, err
		}
		kept := make([]*model.SyncEvent, 0, len(events))
		for _, e := range events {
			if e != nil && h.bound.holds(e) {
				kept = append(kept, e)
			}
		}
		if len(kept) > 0 || !more || len(events) == 0 {
			return kept, more, nil
		}
		after = transport.CursorAt(events[len(events)-1])
	}
}

// boundedCloneExport exports a fresh scratch clone built from only the hub
// events local has applied (appliedBound).
func boundedCloneExport(ctx context.Context, logger *slog.Logger, root, scratch string, local *sqlite.Store, hub cursorPuller) ([]byte, error) {
	bound, err := readAppliedBound(ctx, local)
	if err != nil {
		return nil, err
	}
	body, _, err := cloneExportWithin(ctx, logger, root, scratch, bound, hub)
	return body, err
}

// cloneEvidence reads, from the clone store, which nodes it stamped closed_at
// from a plain old terminal event (MTIX-112.17.16.19.1).
func cloneEvidence(ctx context.Context, clone *sqlite.Store) (*service.CloneEvidence, error) {
	stamps, err := clone.LegacyClosedStamps(ctx)
	if err != nil {
		return nil, fmt.Errorf("read how the clone stamped completion times: %w", err)
	}
	return &service.CloneEvidence{LegacyClosedAt: stamps}, nil
}

// cloneExportWithin is boundedCloneExport over a bound already read.
func cloneExportWithin(ctx context.Context, logger *slog.Logger, root, scratch string, bound appliedBound, hub cursorPuller) (body []byte, ev *service.CloneEvidence, err error) {
	dir := filepath.Join(scratch, "clone")
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create the scratch clone directory: %w", err)
	}
	err = withBoardStore(logger, root, dir, func(clone *sqlite.Store) error {
		if e := seedFreshStore(ctx, clone, io.Discard, boundedHub{cursorPuller: hub, bound: bound}); e != nil {
			return fmt.Errorf("copy the applied hub events into the scratch clone: %w", e)
		}
		var e error
		if body, e = exportBytes(ctx, clone); e != nil {
			return e
		}
		ev, e = cloneEvidence(ctx, clone)
		return e
	})
	return body, ev, err
}

// boardHubFinding is the second rule: the local store's export equals a clone
// built from the hub events it has applied. Equal means every local state is
// derivable from hub events, so a hub that is ahead is safe
// whatever it touched; different means the store holds something no hub event
// gives it (or the hub holds an event below its cursor that it never applied: a
// false refusal, the safe direction).
func boardHubFinding(ctx context.Context, logger *slog.Logger, root, scratch string, local *sqlite.Store, hub cursorPuller) (*service.BoardFinding, error) {
	localBody, err := exportBytes(ctx, local)
	if err != nil {
		return nil, err
	}
	bound, err := readAppliedBound(ctx, local)
	if err != nil {
		return nil, err
	}
	cloneBody, ev, err := cloneExportWithin(ctx, logger, root, scratch, bound, hub)
	if err != nil {
		return nil, err
	}
	if bound.snapshot {
		return service.BoardSnapshotHubFinding(localBody, cloneBody, ev)
	}
	finding, err := service.BoardHubFinding(localBody, cloneBody, ev)
	if err != nil || finding == nil || len(finding.StoreOnlyRows()) == 0 {
		return finding, err
	}
	// The bounded clone lacks some rows this store holds. Whether the hub lacks
	// them too is only known from a clone of the hub at its head; the refusal
	// words its row advice from that (ruling r2 point 2). The bounded comparison
	// above is unchanged.
	full, fullEv, err := cloneExportWithin(ctx, logger, root, filepath.Join(scratch, "full"), appliedBound{snapshot: true}, hub)
	if err != nil {
		return nil, fmt.Errorf("read the hub at its head to word the row advice: %w", err)
	}
	if err := finding.ConfirmAgainstHub(localBody, full, fullEv); err != nil {
		return nil, err
	}
	return finding, nil
}

// verifyBoardAgainstClone repeats both proofs once the swap happened, on the old
// store (asideDir): the file rule against tasks.json as it is now, and the hub
// rule against a fresh clone built from the same applied bound. The real clone
// in the data directory is a full clone to the hub's head.
func verifyBoardAgainstClone(ctx context.Context, logger *slog.Logger, root string, dial hubDialer, asideDir string) (finding *service.BoardFinding, err error) {
	hub, closeHub, err := dial(ctx, root)
	if err != nil {
		return nil, err
	}
	defer closeHub()
	err = withScratch(logger, root, func(scratch string) error {
		return withBoardStore(logger, root, asideDir, func(local *sqlite.Store) error {
			var e error
			if finding, e = boardFileFinding(ctx, boardFilePath(root), local); e != nil || finding != nil {
				return e
			}
			finding, e = boardHubFinding(ctx, logger, root, scratch, local, hub)
			return e
		})
	})
	return finding, err
}

// lostLocalValues reads, after the real clone, which nodes the moved-aside
// store's own journal touched (events that are not mirrored hub events) and
// compares the whole old-store export with the real clone, at the hub's head,
// for those nodes. The aside store is only read (MTIX-112.17.16.18).
func lostLocalValues(ctx context.Context, logger *slog.Logger, root, asideDir string) (*service.LostLocal, error) {
	var asideBody []byte
	var own []string
	err := withBoardStore(logger, root, asideDir, func(aside *sqlite.Store) error {
		events, e := aside.LocalEvents(ctx)
		if e != nil {
			return fmt.Errorf("read the journal of the old store: %w", e)
		}
		own = service.OwnEntities(ownEvents(events))
		if len(own) == 0 {
			return nil
		}
		asideBody, e = exportBytes(ctx, aside)
		return e
	})
	if err != nil || len(own) == 0 {
		return nil, err
	}
	var cloneBody []byte
	var ev *service.CloneEvidence
	err = withBoardStore(logger, root, worktreeDataDir(root), func(clone *sqlite.Store) error {
		var e error
		if cloneBody, e = exportBytes(ctx, clone); e != nil {
			return e
		}
		ev, e = cloneEvidence(ctx, clone)
		return e
	})
	if err != nil {
		return nil, err
	}
	return service.FindLostLocal(asideBody, cloneBody, own, ev)
}

// preflightBoard proves the local board is all derivable from the hub before the
// real store moves. The file rule is checked first, without touching the hub.
// The hub rule then compares the store's export with that of one scratch clone
// built from the hub events the store has applied. Nothing touches the live
// store or tasks.json. Each enrolment clones the hub three times: this scratch
// clone, the real clone, and the scratch clone of the verify after the swap. It
// contacts the hub once per enrolment step and keeps no timer.
func preflightBoard(ctx context.Context, logger *slog.Logger, root string, dial hubDialer) (finding *service.BoardFinding, err error) {
	err = withBoardStore(logger, root, worktreeDataDir(root), func(local *sqlite.Store) error {
		var e error
		if finding, e = boardFileFinding(ctx, boardFilePath(root), local); e != nil || finding != nil {
			return e
		}
		hub, closeHub, e := dial(ctx, root)
		if e != nil {
			return e
		}
		defer closeHub()
		return withScratch(logger, root, func(scratch string) error {
			finding, e = boardHubFinding(ctx, logger, root, scratch, local, hub)
			return e
		})
	})
	return finding, err
}

// withScratch runs fn in a scratch directory under the enrolment's own
// directory and removes it, file by file, when done. Directories an earlier,
// crashed preflight left behind are removed first: the enroll lock is held, so
// none can be live.
func withScratch(_ *slog.Logger, root string, fn func(scratch string) error) (retErr error) {
	base := filepath.Join(root, ".mtix", "enroll.d")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return fmt.Errorf("create the enrolment directory: %w", err)
	}
	// The directory is listed, not globbed: the worktree's own path may hold
	// glob characters such as a bracket (review F13).
	entries, err := os.ReadDir(base)
	if err != nil {
		return fmt.Errorf("list stale scratch directories: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "preflight-") {
			continue
		}
		d := filepath.Join(base, e.Name())
		if rmErr := removeScratchTree(d); rmErr != nil {
			return fmt.Errorf("remove the stale scratch directory %s: %w", d, rmErr)
		}
	}
	scratch, err := os.MkdirTemp(base, "preflight-")
	if err != nil {
		return fmt.Errorf("create the scratch directory: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, removeScratchTree(scratch)) }()
	return fn(scratch)
}

// removeScratchTree deletes the scratch directory the preflight created: each
// directory's files first, then the emptied directory itself, bottom-up. Links
// are removed as links, never followed.
func removeScratchTree(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("list the scratch directory: %w", err)
	}
	for _, e := range entries {
		child := filepath.Join(dir, e.Name())
		if e.Type().IsDir() {
			err = errors.Join(err, removeScratchTree(child))
			continue
		}
		err = errors.Join(err, os.Remove(child))
	}
	return errors.Join(err, os.Remove(dir))
}

// ownEvents maps the journal rows to what the owner check reads: the node, the
// status (a mirrored hub event is not own) and the payload (a link's target).
func ownEvents(events []sqlite.LocalEvent) []service.OwnEvent {
	raw := make([]service.OwnEvent, len(events))
	for i, ev := range events {
		raw[i] = service.OwnEvent{NodeID: ev.NodeID, Op: ev.Op, Status: ev.Status, Payload: ev.Payload}
	}
	return raw
}
