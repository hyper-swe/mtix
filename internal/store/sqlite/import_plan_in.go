// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

// stagePageRows is how many staged nodes are read at a time.
const stagePageRows = 64

// stagedBody is one staged node as stored.
type stagedBody struct {
	ord  int
	body []byte
}

// readNodePage reads up to stagePageRows staged nodes after ord after, and
// closes the cursor before returning, so the caller can write meanwhile.
func readNodePage(ctx context.Context, q planQuerier, after int) ([]stagedBody, error) {
	rows, err := q.QueryContext(ctx, stageNodePageSQL, int(stageNodes), after, stagePageRows)
	if err != nil {
		return nil, fmt.Errorf("read staged nodes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var page []stagedBody
	for rows.Next() {
		var b stagedBody
		if err := rows.Scan(&b.ord, &b.body); err != nil {
			return nil, fmt.Errorf("scan staged node: %w", err)
		}
		page = append(page, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read staged nodes: %w", err)
	}
	return page, nil
}

// eachStagedNode decodes every staged node in file order, one at a time.
func (st *StagedExport) eachStagedNode(ctx context.Context, q planQuerier, fn func(ord int, n *exportNode) error) error {
	for after := -1; ; {
		page, err := readNodePage(ctx, q, after)
		if err != nil {
			return err
		}
		for _, b := range page {
			var n exportNode
			if err := json.Unmarshal(b.body, &n); err != nil {
				return fmt.Errorf("decode staged node %d: %w", b.ord, err)
			}
			if err := fn(b.ord, &n); err != nil {
				return err
			}
			after = b.ord
		}
		if len(page) < stagePageRows {
			return nil
		}
	}
}

// stagedNodeAt decodes the staged node at place ord.
func (st *StagedExport) stagedNodeAt(ctx context.Context, q planQuerier, ord int) (exportNode, error) {
	var body []byte
	var n exportNode
	err := q.QueryRowContext(ctx, stageNodeAtSQL, int(stageNodes), ord).Scan(&body)
	if err != nil {
		return n, fmt.Errorf("read staged node %d: %w", ord, err)
	}
	if err := json.Unmarshal(body, &n); err != nil {
		return n, fmt.Errorf("decode staged node %d: %w", ord, err)
	}
	return n, nil
}

// loadIncoming indexes the identity columns of every staged node in plan_in.
func (p *ImportPlan) loadIncoming(ctx context.Context) error {
	return p.st.eachStagedNode(ctx, p.q, func(ord int, n *exportNode) error {
		if _, err := p.q.ExecContext(ctx, planInsertIncomingSQL,
			ord, n.ID, n.UID, n.ParentID, n.Project, n.Depth, n.Seq, n.CreatedAt, n.Title,
			model.IsProvisional(n.ID), n.ID, n.UID, n.ParentID, n.Seq); err != nil {
			return fmt.Errorf("index incoming node %d: %w", ord, err)
		}
		return nil
	})
}

// inNode is one row of plan_in.
type inNode struct {
	ord                                 int
	id, uid, parent, project, createdAt string
	title, fID, fUID, fParent           string
	depth, seq, fSeq                    int
}

func scanInNode(row rowScanner) (inNode, error) {
	var n inNode
	err := row.Scan(&n.ord, &n.id, &n.uid, &n.parent, &n.project, &n.depth, &n.seq, &n.createdAt,
		&n.title, &n.fID, &n.fUID, &n.fParent, &n.fSeq)
	return n, err
}

// incomingKey names the column an incoming node is looked up by.
type incomingKey int

const (
	incomingByOrd  incomingKey = iota // the place in the file
	incomingByID                      // the id the file holds it under (the last of a repeated id)
	incomingByFUID                    // the uid it ends with (the last)
)

// incomingQuery returns the constant statement that looks incoming nodes up by key.
func incomingQuery(key incomingKey) string {
	switch key {
	case incomingByID:
		return planIncomingByIDSQL
	case incomingByFUID:
		return planIncomingByFUIDSQL
	default:
		return planIncomingByOrdSQL
	}
}

// incomingBy returns the incoming node the key selects (the last one of a
// repeated id, as the in-memory plan's maps keep it), and whether there is one.
func (p *ImportPlan) incomingBy(ctx context.Context, key incomingKey, arg any) (inNode, bool, error) {
	n, err := scanInNode(p.q.QueryRowContext(ctx, incomingQuery(key), arg))
	if errors.Is(err, sql.ErrNoRows) {
		return n, false, nil
	}
	if err != nil {
		return n, false, fmt.Errorf("read incoming node: %w", err)
	}
	return n, true, nil
}

// incomingAt reads the plan_in row at ord.
func (p *ImportPlan) incomingAt(ctx context.Context, ord int) (inNode, error) {
	n, ok, err := p.incomingBy(ctx, incomingByOrd, ord)
	if err == nil && !ok {
		err = fmt.Errorf("incoming node %d: %w", ord, model.ErrNotFound)
	}
	return n, err
}

// validateUIDs detects duplicate uids within the export and validates each
// incoming uid against the local store (ADR-003 §6, F-3), recording conflicts,
// idempotent no-ops and re-stamps, as validateImportUIDs does.
func (p *ImportPlan) validateUIDs(ctx context.Context) error {
	if err := p.exportDuplicateUIDConflicts(ctx); err != nil {
		return err
	}
	if err := p.classifyAgainstLocal(ctx); err != nil {
		return err
	}
	return p.rejectConflicts(ctx)
}

// exportDuplicateUIDConflicts records one conflict per uid that appears on
// more than one node of the export, at its second node, pointing at the first
// (exportDuplicateUIDConflicts).
func (p *ImportPlan) exportDuplicateUIDConflicts(ctx context.Context) error {
	res, err := p.q.ExecContext(ctx, planDuplicateUIDsSQL,
		int(secConflict), p.counts[secConflict], int(ConflictExportDuplicateUID))
	if err != nil {
		return fmt.Errorf("find duplicate uids in the export: %w", err)
	}
	n, err := res.RowsAffected()
	p.counts[secConflict] += int(n)
	return err
}

// classifyAgainstLocal validates each incoming uid against the local store
// (classifyAgainstLocal): an identical path is a no-op, a different one a
// conflict, a merge's renumber, or, with ForceRename, a re-stamp.
func (p *ImportPlan) classifyAgainstLocal(ctx context.Context) error {
	for ord := 0; ord < p.st.NodeCount(); ord++ {
		n, err := p.incomingAt(ctx, ord)
		if err != nil {
			return err
		}
		if n.uid == "" {
			continue
		}
		if err := p.classifyOne(ctx, &n); err != nil {
			return err
		}
	}
	return nil
}

func (p *ImportPlan) classifyOne(ctx context.Context, n *inNode) error {
	localPath, err := p.s.ResolveDisplayPathByUID(ctx, n.uid)
	if errors.Is(err, model.ErrNotFound) {
		return nil // uid is new to this store — nothing to reconcile.
	}
	if err != nil {
		return fmt.Errorf("validate import uid %s: %w", n.uid, err)
	}
	switch {
	case localPath == n.id:
		p.idempotent++ // identical uid + display_path — a no-op.
		return nil
	case p.opts.Mode == ImportModeMerge && !p.opts.ForceRename:
		return nil // MTIX-95.31.4: planLocalRenumbers moves it or rejects it
	case !p.opts.ForceRename:
		return p.add(ctx, secConflict, reportRow{
			c: [5]string{n.uid, n.id, localPath}, n: int(ConflictLocalUIDMismatch)})
	}
	freshUID, mintErr := clock.NewEventID()
	if mintErr != nil {
		return fmt.Errorf("mint replacement uid for %s: %w", n.id, mintErr)
	}
	if err := p.addRemap(ctx, secRenamed, n.uid, n.id, n.id, false); err != nil {
		return err
	}
	// The incoming node takes the fresh uid.
	if _, err := p.q.ExecContext(ctx, planRestampSQL, freshUID, n.ord); err != nil {
		return fmt.Errorf("re-stamp incoming node %s: %w", n.id, err)
	}
	return nil
}

// planProvisionalRemaps renumbers every incoming provisional node to the
// lowest free sibling number under its (rebased) parent, shallowest first, as
// planProvisionalRemaps does, recording each move in the remap list and the
// new id, parent and number in plan_in.
func (p *ImportPlan) planProvisionalRemaps(ctx context.Context) error {
	if _, err := p.q.ExecContext(ctx, planProvOrderSQL); err != nil {
		return fmt.Errorf("order the provisional nodes: %w", err)
	}
	var count int
	if err := p.q.QueryRowContext(ctx, planProvCountSQL).Scan(&count); err != nil {
		return fmt.Errorf("count the provisional nodes: %w", err)
	}
	for pos := 0; pos < count; pos++ {
		var ord int
		if err := p.q.QueryRowContext(ctx, planProvAtSQL, pos).Scan(&ord); err != nil {
			return fmt.Errorf("read the provisional order: %w", err)
		}
		if err := p.remapProvisional(ctx, ord); err != nil {
			return err
		}
	}
	return nil
}

// remapProvisional renumbers the provisional node at ord.
func (p *ImportPlan) remapProvisional(ctx context.Context, ord int) error {
	n, err := p.incomingAt(ctx, ord)
	if err != nil {
		return err
	}
	newParent, err := p.applyRebase(ctx, n.parent)
	if err != nil {
		return err
	}
	if newParent == "" {
		return fmt.Errorf("provisional node %s has no parent: %w", n.id, model.ErrInvalidInput)
	}
	seq, err := p.nextFreeChildSeq(ctx, newParent)
	if err != nil {
		return err
	}
	newID := model.BuildID(n.project, newParent, seq)
	if _, err := p.q.ExecContext(ctx, planRenumberInSQL, newID, newParent, seq, ord); err != nil {
		return fmt.Errorf("renumber incoming node %s: %w", n.id, err)
	}
	if _, err := p.q.ExecContext(ctx, planRebaseSetSQL, n.id, newID); err != nil {
		return fmt.Errorf("record the renumbered path of %s: %w", n.id, err)
	}
	return p.addRemap(ctx, secRemap, n.fUID, n.id, newID, false)
}

// applyRebase rewrites a display_path at or under any renumbered ancestor
// (applyRebase): the exact path first, then the longest ancestor prefix.
func (p *ImportPlan) applyRebase(ctx context.Context, path string) (string, error) {
	if path == "" {
		return "", nil
	}
	for end := len(path); end > 0; {
		var mapped string
		err := p.q.QueryRowContext(ctx, planRebaseGetSQL, path[:end]).Scan(&mapped)
		if err == nil {
			return mapped + path[end:], nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("read the renumbered path of %s: %w", path[:end], err)
		}
		end = strings.LastIndex(path[:end], ".")
		if end < 0 {
			break
		}
	}
	return path, nil
}

// nextFreeChildSeq returns the lowest sibling number under parentID free in
// the store and the plan, and takes it (nextFreeChildSeq). Numbers only get
// taken, never freed, so the search starts at the lowest number not yet known
// to be unavailable (plan_hint) instead of at 1: renumbering a large family of
// siblings stays linear.
func (p *ImportPlan) nextFreeChildSeq(ctx context.Context, parentID string) (int, error) {
	start := 1
	err := p.q.QueryRowContext(ctx, planHintGetSQL, parentID).Scan(&start)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("read the number hint under %s: %w", parentID, err)
	}
	for seq := start; ; seq++ {
		taken, err := p.isTaken(ctx, parentID, seq)
		if err != nil {
			return 0, err
		}
		if taken {
			continue
		}
		exists, err := p.s.nodeIDExists(ctx, model.BuildID("", parentID, seq))
		if err != nil {
			return 0, err
		}
		if exists {
			continue
		}
		// Every number below seq is unavailable now; seq is taken.
		if _, err := p.q.ExecContext(ctx, planHintSetSQL, parentID, seq+1); err != nil {
			return 0, fmt.Errorf("record the number hint under %s: %w", parentID, err)
		}
		return seq, p.take(ctx, parentID, seq)
	}
}
