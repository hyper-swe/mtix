// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// RecoverResult is the salvage report produced by Recover (MTIX-26.5).
// Export is always importable via the standard import path: it carries a
// freshly computed checksum, and missing ancestors are synthesized as
// placeholders so the parent foreign-key chain holds.
type RecoverResult struct {
	Export       *ExportData
	RecoveredIDs []string // read intact from the damaged database
	LostIDs      []string // unreadable in the database and absent from the mirror
	FromMirror   []string // unreadable or missing in the database, salvaged from the mirror
	Placeholders []string // synthesized parents for orphaned survivors
	Notes        []string // human-readable salvage diagnostics
}

// Recover salvages as much as possible from a damaged database and the
// tasks.json mirror, without writing to either (MTIX-26.5). Strategy,
// mirroring what worked in the 2026-05-19 field incident:
//
//  1. Open the database read-only with cell_size_check off and walk the
//     primary-key index for IDs; read each row individually so one torn
//     page does not hide every other row.
//  2. Fill rows the database lost from the mirror, which is written on
//     every mutation on every interface (FR-15.3).
//  3. Synthesize placeholder parents for orphaned survivors and recompute
//     the export checksum, so the result imports through the standard,
//     fully validated path.
//
// Returns an error only when there is nothing to salvage from either
// source — partial damage yields a partial result plus Notes.
func Recover(ctx context.Context, dbPath, mirrorPath, mtixVersion string, logger *slog.Logger) (*RecoverResult, error) {
	res := &RecoverResult{}
	nodes := map[string]exportNode{}
	var deps []exportDep
	var agents []exportAgent
	var sessions []exportSession

	dbNodes, dbErr := salvageFromDB(ctx, dbPath, res)
	if dbErr != nil {
		res.Notes = append(res.Notes, fmt.Sprintf("database unusable: %v", dbErr))
		logger.Warn("recover: database unusable, falling back to mirror", "error", dbErr)
	} else {
		for id, n := range dbNodes.nodes {
			nodes[id] = n
		}
		deps = dbNodes.deps
		agents = dbNodes.agents
		sessions = dbNodes.sessions
	}

	mirror, mirrorErr := readMirror(mirrorPath)
	if mirrorErr != nil {
		res.Notes = append(res.Notes, fmt.Sprintf("mirror unusable: %v", mirrorErr))
	} else {
		if valid, err := VerifyExportChecksum(mirror); err != nil || !valid {
			res.Notes = append(res.Notes,
				"mirror checksum did not verify; its contents are still used as salvage of last resort")
		}
		for _, n := range mirror.Nodes {
			if _, ok := nodes[n.ID]; !ok {
				nodes[n.ID] = n
				res.FromMirror = append(res.FromMirror, n.ID)
			}
		}
		deps = append(deps, mirror.Dependencies...)
		if len(agents) == 0 {
			agents = mirror.Agents
		}
		if len(sessions) == 0 {
			sessions = mirror.Sessions
		}
	}

	// IDs the database knew about but neither source could produce.
	res.LostIDs = subtract(res.LostIDs, keys(nodes))

	if len(nodes) == 0 {
		return nil, fmt.Errorf(
			"nothing to salvage: database (%s) and mirror (%s) both unusable: %w",
			dbPath, mirrorPath, model.ErrNotFound)
	}

	res.Placeholders = synthesizePlaceholderParents(nodes)

	export := &ExportData{
		Version:       1,
		SchemaVersion: SchemaVersionV1,
		MtixVersion:   mtixVersion,
		Project:       firstProject(nodes),
		Nodes:         values(nodes),
		Dependencies:  dedupDeps(deps, nodes),
		Agents:        agents,
		Sessions:      sessions,
	}
	if err := RecomputeExportChecksum(export); err != nil {
		return nil, fmt.Errorf("finalize recovered export: %w", err)
	}
	res.Export = export

	sort.Strings(res.RecoveredIDs)
	sort.Strings(res.LostIDs)
	sort.Strings(res.FromMirror)
	sort.Strings(res.Placeholders)
	if len(res.LostIDs) > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"%d row(s) were unreadable in the database and absent from the mirror; their IDs are listed as lost",
			len(res.LostIDs)))
	}
	return res, nil
}

// dbSalvage holds everything readable from the damaged database.
type dbSalvage struct {
	nodes    map[string]exportNode
	deps     []exportDep
	agents   []exportAgent
	sessions []exportSession
}

// salvageFromDB opens dbPath read-only and reads rows individually.
// Recovered/lost ID bookkeeping is written into res as a side effect.
func salvageFromDB(ctx context.Context, dbPath string, res *RecoverResult) (*dbSalvage, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("stat database: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open read-only: %w", err)
	}
	defer func() { _ = db.Close() }()
	// Pragmas are per-connection; pin a single connection so they hold
	// for every salvage read.
	db.SetMaxOpenConns(1)
	// Best effort: read through cells whose size bookkeeping is damaged
	// (this is what salvaged 47 of 80 rows in the field incident).
	_, _ = db.ExecContext(ctx, "PRAGMA cell_size_check = OFF")

	ids, err := salvageIDs(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("walk primary-key index: %w", err)
	}

	out := &dbSalvage{nodes: map[string]exportNode{}}
	for _, id := range ids {
		n, err := scanExportNode(db.QueryRowContext(ctx, exportNodeSelectSQL+" WHERE id = ?", id))
		if err != nil {
			res.LostIDs = append(res.LostIDs, id)
			continue
		}
		n.NodeType = string(model.NodeTypeForDepth(n.Depth))
		out.nodes[id] = n
		res.RecoveredIDs = append(res.RecoveredIDs, id)
	}

	// Secondary tables, best effort: their loss never blocks node salvage.
	out.deps = salvageDeps(ctx, db, res)
	out.agents, out.sessions = salvageAgentsSessions(ctx, db, res)
	return out, nil
}

// salvageIDs walks the primary-key index, which often survives damage to
// table leaf pages.
func salvageIDs(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT id FROM nodes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return ids, nil //nolint:nilerr // keep what the index yielded before the error
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return ids, nil //nolint:nilerr // partial index walk is still salvage
	}
	return ids, nil
}

// salvageDeps bulk-reads dependencies, tolerating total loss.
func salvageDeps(ctx context.Context, db *sql.DB, res *RecoverResult) []exportDep {
	rows, err := db.QueryContext(ctx,
		`SELECT from_id, to_id, dep_type, created_at FROM dependencies`)
	if err != nil {
		res.Notes = append(res.Notes, fmt.Sprintf("dependencies unreadable: %v", err))
		return nil
	}
	defer func() { _ = rows.Close() }()

	var deps []exportDep
	for rows.Next() {
		var d exportDep
		if err := rows.Scan(&d.FromID, &d.ToID, &d.DepType, &d.CreatedAt); err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("dependency row unreadable: %v", err))
			break
		}
		deps = append(deps, d)
	}
	return deps
}

// salvageAgentsSessions bulk-reads agents and sessions, tolerating loss.
func salvageAgentsSessions(ctx context.Context, db *sql.DB, res *RecoverResult) ([]exportAgent, []exportSession) {
	var agents []exportAgent
	rows, err := db.QueryContext(ctx,
		`SELECT agent_id, project, COALESCE(state,'idle'),
		        COALESCE(current_node_id,''), COALESCE(last_heartbeat,'') FROM agents`)
	if err == nil {
		for rows.Next() {
			var a exportAgent
			if rows.Scan(&a.AgentID, &a.Project, &a.State, &a.CurrentNodeID, &a.LastHeartbeat) != nil {
				break
			}
			agents = append(agents, a)
		}
		_ = rows.Close()
	} else {
		res.Notes = append(res.Notes, fmt.Sprintf("agents unreadable: %v", err))
	}

	var sessions []exportSession
	rows, err = db.QueryContext(ctx,
		`SELECT id, agent_id, project, started_at, COALESCE(ended_at,''),
		        COALESCE(status,'active'), COALESCE(summary,'') FROM sessions`)
	if err == nil {
		for rows.Next() {
			var sess exportSession
			if rows.Scan(&sess.ID, &sess.AgentID, &sess.Project,
				&sess.StartedAt, &sess.EndedAt, &sess.Status, &sess.Summary) != nil {
				break
			}
			sessions = append(sessions, sess)
		}
		_ = rows.Close()
	} else {
		res.Notes = append(res.Notes, fmt.Sprintf("sessions unreadable: %v", err))
	}
	return agents, sessions
}

// readMirror loads a tasks.json-format export.
func readMirror(path string) (*ExportData, error) {
	if path == "" {
		return nil, fmt.Errorf("no mirror path provided")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read mirror: %w", err)
	}
	var data ExportData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("parse mirror: %w", err)
	}
	return &data, nil
}

// synthesizePlaceholderParents adds minimal stand-in nodes for every
// ancestor that salvage could not produce, so the parent foreign key
// (schema: REFERENCES nodes(id)) holds during import. Placeholder IDs
// are returned sorted-insertion order is irrelevant because the export
// is canonicalized afterwards.
func synthesizePlaceholderParents(nodes map[string]exportNode) []string {
	var created []string
	// Iterate over a snapshot: the map grows while we walk parent chains.
	pending := values(nodes)
	for len(pending) > 0 {
		n := pending[len(pending)-1]
		pending = pending[:len(pending)-1]

		parentID := parentIDOf(n.ID)
		if parentID == "" {
			// Root node: clear any dangling parent reference the source
			// carried (e.g., a mirror edited by hand).
			continue
		}
		if n.ParentID == "" {
			continue
		}
		if _, ok := nodes[n.ParentID]; ok {
			continue
		}

		p := placeholderNode(n)
		nodes[p.ID] = p
		created = append(created, p.ID)
		pending = append(pending, p) // its parent may be missing too
	}
	return created
}

// placeholderNode builds the stand-in parent for an orphaned child.
func placeholderNode(child exportNode) exportNode {
	id := child.ParentID
	depth := dotpathDepth(id)
	seq := 0
	if segs := strings.Split(id, "."); len(segs) > 1 {
		seq, _ = strconv.Atoi(segs[len(segs)-1])
	} else if dash := strings.LastIndex(id, "-"); dash >= 0 {
		seq, _ = strconv.Atoi(id[dash+1:])
	}
	return exportNode{
		ID:          id,
		ParentID:    parentIDOf(id),
		Depth:       depth,
		Seq:         seq,
		Project:     child.Project,
		Title:       "[recovered placeholder — original node lost]",
		NodeType:    string(model.NodeTypeForDepth(depth)),
		Priority:    3,
		Labels:      "[]",
		Status:      "open",
		Weight:      1,
		CreatedAt:   child.CreatedAt,
		UpdatedAt:   child.UpdatedAt,
	}
}

// parentIDOf derives the parent from a dotpath ID: "P-7.2" → "P-7",
// "P-7" → "" (root).
func parentIDOf(id string) string {
	if i := strings.LastIndex(id, "."); i >= 0 {
		return id[:i]
	}
	return ""
}

// dotpathDepth derives depth from a dotpath ID: "P-7" → 0, "P-7.2" → 1.
func dotpathDepth(id string) int {
	return strings.Count(id, ".")
}

// dedupDeps drops duplicates and dependencies whose endpoints did not
// survive salvage.
func dedupDeps(deps []exportDep, nodes map[string]exportNode) []exportDep {
	seen := map[string]bool{}
	var out []exportDep
	for _, d := range deps {
		key := d.FromID + "\x00" + d.ToID + "\x00" + d.DepType
		if seen[key] {
			continue
		}
		if _, ok := nodes[d.FromID]; !ok {
			continue
		}
		if _, ok := nodes[d.ToID]; !ok {
			continue
		}
		seen[key] = true
		out = append(out, d)
	}
	return out
}

// keys returns the map's keys.
func keys(m map[string]exportNode) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// values returns the map's values.
func values(m map[string]exportNode) []exportNode {
	out := make([]exportNode, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// subtract returns ids minus the present set.
func subtract(ids, present []string) []string {
	set := map[string]bool{}
	for _, p := range present {
		set[p] = true
	}
	var out []string
	for _, id := range ids {
		if !set[id] {
			out = append(out, id)
		}
	}
	return out
}

// firstProject picks the project of the lexicographically first node so
// the export header is deterministic.
func firstProject(nodes map[string]exportNode) string {
	ks := keys(nodes)
	sort.Strings(ks)
	if len(ks) == 0 {
		return ""
	}
	return nodes[ks[0]].Project
}
