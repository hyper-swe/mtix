// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// maxQueuedPayload is the payload size the drain accepts.
const maxQueuedPayload = 4096

// queuedRows is the attach queue of a board as doctor reports it: the rows the
// store owns (safe to build a command from) and a count of the rest.
type queuedRows struct {
	Valid    []sqlite.AttachPending
	Rejected int
}

// productionPendingRows reads the attach queue of the store the predicate
// resolved, and only when it is the board doctor itself opened: doctor never
// opens a second store to look for rows. Rows are untrusted (a committed store
// can carry anything), so they are filtered through acceptQueuedRows against
// this store's own board key and path.
func productionPendingRows(ctx context.Context, d attach.GlueDecision) (queuedRows, error) {
	if app.store == nil || app.mtixDir == "" || d.StoreRoot == "" {
		return queuedRows{}, nil
	}
	opened, err := filepath.EvalSymlinks(filepath.Dir(app.mtixDir))
	if err != nil || opened != d.StoreRoot {
		return queuedRows{}, nil //nolint:nilerr // a board doctor did not open has no rows to report
	}
	rows, err := app.store.ListAttachPending(ctx)
	if err != nil {
		return queuedRows{}, fmt.Errorf("list attach queue: %w", err)
	}
	valid, rejected := acceptQueuedRows(rows, queueOwnerKey(ctx), d.StoreRoot)
	return queuedRows{Valid: valid, Rejected: rejected}, nil
}

// queueOwnerKey is the board key a registration in the board doctor opened
// writes on its queue rows: the committed board id when there is exactly one,
// else the store instance id. It is empty for a legacy store or an ambiguous
// board id, which own no rows.
func queueOwnerKey(ctx context.Context) string {
	instance, err := app.store.StoreInstanceID(ctx)
	if err != nil || instance == "" {
		return ""
	}
	ids, err := service.ReadBoardIDs(app.mtixDir)
	switch {
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return ""
	case len(ids) > 1:
		return ""
	case len(ids) == 1:
		return ids[0]
	}
	return instance
}

// acceptQueuedRows splits rows into those this store owns and a count of the
// rest, applying the same owner test the drain does (board key and path equal
// this store's, a known kind, a bounded strict payload) plus that the lane
// lies inside the store. Nothing is built from a rejected row.
func acceptQueuedRows(rows []sqlite.AttachPending, key, storeRoot string) (valid []sqlite.AttachPending, rejected int) {
	for _, row := range rows {
		if key == "" || row.BoardKey != key || row.Path != storeRoot || !ownedLane(row, storeRoot) {
			rejected++
			continue
		}
		valid = append(valid, row)
	}
	return valid, rejected
}

// ownedLane reports whether row is a lane or list row with a well formed
// payload whose agent id is plain and whose lane directory is inside storeRoot.
func ownedLane(row sqlite.AttachPending, storeRoot string) bool {
	if (row.Kind != "lane" && row.Kind != "list") || len(row.Payload) > maxQueuedPayload {
		return false
	}
	var q queuedLane
	dec := json.NewDecoder(bytes.NewReader([]byte(row.Payload)))
	dec.DisallowUnknownFields()
	if dec.Decode(&q) != nil || !queuedAgentID.MatchString(q.AgentID) {
		return false
	}
	if !filepath.IsAbs(q.LanePath) || filepath.Clean(q.LanePath) != q.LanePath {
		return false
	}
	rel, err := filepath.Rel(storeRoot, q.LanePath)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
