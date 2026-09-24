// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/hyper-swe/mtix/internal/model"
)

// SchemaVersionV1 is the schema_version this build writes for export format
// generation 1 (the envelope's version field) per FR-15.2g. Auto-import and
// mtix import refuse a file with a higher major version.
//
// History:
//   - 1.0.0 (mtix 0.5.3 and earlier): nodes without annotations, the
//     activity stream and the other columns listed as added in 2.0.0 above
//     exportNode.
//   - 2.0.0 (MTIX-95.31.1): every nodes column is exported. The major
//     version rose because a 1.x reader would drop the added keys and then
//     rewrite the file without them; it now skips the file as newer than it
//     supports. The added keys are omitted when empty, so a node without
//     them encodes and hashes as it did in 1.0.0, and a merge import reads
//     their absence from a 1.x file as "not carried", never as "cleared".
const SchemaVersionV1 = "2.0.0"

// ExportData represents the complete export format per FR-7.8, FR-15.1.
// The schema_version field (FR-15.2g) enables auto-import compatibility checks.
type ExportData struct {
	Version       int             `json:"version"`
	SchemaVersion string          `json:"schema_version"`
	ExportedAt    string          `json:"exported_at"`
	MtixVersion   string          `json:"mtix_version"`
	Project       string          `json:"project"`
	Nodes         []exportNode    `json:"nodes"`
	Dependencies  []exportDep     `json:"dependencies"`
	Agents        []exportAgent   `json:"agents"`
	Sessions      []exportSession `json:"sessions"`
	NodeCount     int             `json:"node_count"`
	Checksum      string          `json:"checksum"`
}

// exportNode is the JSON representation of a node in the export (FR-7.8,
// FR-15.1). Column audit (MTIX-95.31.1): it carries every column of the
// nodes table, each under the column's own name.
//
//   - Exported since 1.0.0: id, parent_id, depth, seq, project, title,
//     description, prompt, acceptance, node_type, issue_type, priority,
//     labels, status, progress, assignee, creator, agent_state, weight,
//     content_hash, created_at, updated_at, closed_at, defer_until,
//     deleted_at, uid.
//   - Added in 2.0.0: previous_status, estimate_min, actual_min, code_refs,
//     commit_refs, annotations, invalidated_at, invalidated_by,
//     invalidation_reason, activity, deleted_by, metadata, session_id.
//     annotations, code_refs and commit_refs carry the structure mtix show
//     --json returns, activity the entries mtix show lists; metadata
//     carries the column's JSON text, as labels does.
//   - Deliberately excluded: none. The derived columns are exported too:
//     node_type is re-derived from depth on export and on import, and
//     progress and content_hash are written back as exported. SQLite's
//     implicit rowid is not a declared column: it is a local storage key,
//     and import rebuilds the full-text index (nodes_fts) from the rows it
//     writes.
//
// Every 2.0.0 field is omitempty, so a node without them encodes, and
// hashes, exactly as it did in a 1.0.0 file.
type exportNode struct {
	ID          string  `json:"id"`
	ParentID    string  `json:"parent_id"`
	Depth       int     `json:"depth"`
	Seq         int     `json:"seq"`
	Project     string  `json:"project"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Prompt      string  `json:"prompt"`
	Acceptance  string  `json:"acceptance"`
	NodeType    string  `json:"node_type"`
	IssueType   string  `json:"issue_type"`
	Priority    int     `json:"priority"`
	Labels      string  `json:"labels"`
	Status      string  `json:"status"`
	Progress    float64 `json:"progress"`
	Assignee    string  `json:"assignee"`
	Creator     string  `json:"creator"`
	AgentState  string  `json:"agent_state"`
	Weight      float64 `json:"weight"`
	ContentHash string  `json:"content_hash"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	ClosedAt    string  `json:"closed_at,omitempty"`
	DeferUntil  string  `json:"defer_until,omitempty"`
	DeletedAt   string  `json:"deleted_at,omitempty"`
	// UID carries the node's durable internal identity (ADR-003 §2, §7)
	// so re-import stays consistent. omitempty for pre-v3 exports.
	UID string `json:"uid,omitempty"`

	// Columns added in schema 2.0.0 (MTIX-95.31.1); see the audit above.
	PreviousStatus     string                `json:"previous_status,omitempty"`
	EstimateMin        *int                  `json:"estimate_min,omitempty"`
	ActualMin          *int                  `json:"actual_min,omitempty"`
	CodeRefs           []model.CodeRef       `json:"code_refs,omitempty"`
	CommitRefs         []string              `json:"commit_refs,omitempty"`
	Annotations        []model.Annotation    `json:"annotations,omitempty"`
	InvalidatedAt      string                `json:"invalidated_at,omitempty"`
	InvalidatedBy      string                `json:"invalidated_by,omitempty"`
	InvalidationReason string                `json:"invalidation_reason,omitempty"`
	Activity           []model.ActivityEntry `json:"activity,omitempty"`
	DeletedBy          string                `json:"deleted_by,omitempty"`
	Metadata           string                `json:"metadata,omitempty"`
	SessionID          string                `json:"session_id,omitempty"`
}

// exportDep is the JSON representation of a dependency in the export.
type exportDep struct {
	FromID    string `json:"from_id"`
	ToID      string `json:"to_id"`
	DepType   string `json:"dep_type"`
	CreatedAt string `json:"created_at"`
}

// exportAgent is the JSON representation of an agent in the export.
type exportAgent struct {
	AgentID       string `json:"agent_id"`
	Project       string `json:"project"`
	State         string `json:"state"`
	CurrentNodeID string `json:"current_node_id,omitempty"`
	LastHeartbeat string `json:"last_heartbeat,omitempty"`
}

// exportSession is the JSON representation of a session in the export.
type exportSession struct {
	ID        string `json:"id"`
	AgentID   string `json:"agent_id"`
	Project   string `json:"project"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at,omitempty"`
	Status    string `json:"status"`
	Summary   string `json:"summary,omitempty"`
}

// Export produces a complete JSON export of the database per FR-7.8.
// Includes all nodes (including soft-deleted within retention), dependencies,
// agents, sessions, with node_count and SHA-256 checksum for integrity.
func (s *Store) Export(ctx context.Context, project, mtixVersion string) (*ExportData, error) {
	nodes, err := s.exportNodes(ctx)
	if err != nil {
		return nil, exportReadError("export nodes", err)
	}

	deps, err := s.exportDependencies(ctx)
	if err != nil {
		return nil, exportReadError("export dependencies", err)
	}

	agents, err := s.exportAgents(ctx)
	if err != nil {
		return nil, exportReadError("export agents", err)
	}

	sessions, err := s.exportSessions(ctx)
	if err != nil {
		return nil, exportReadError("export sessions", err)
	}

	// Sort nodes by ID for canonical checksum.
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	sort.Slice(deps, func(i, j int) bool {
		if deps[i].FromID != deps[j].FromID {
			return deps[i].FromID < deps[j].FromID
		}
		return deps[i].ToID < deps[j].ToID
	})

	// Compute checksum over canonical JSON of nodes and deps.
	checksum, err := computeExportChecksum(nodes, deps)
	if err != nil {
		return nil, fmt.Errorf("compute checksum: %w", err)
	}

	return &ExportData{
		Version:       1,
		SchemaVersion: SchemaVersionV1,
		ExportedAt:    time.Now().UTC().Format(time.RFC3339),
		MtixVersion:   mtixVersion,
		Project:       project,
		Nodes:         nodes,
		Dependencies:  deps,
		Agents:        agents,
		Sessions:      sessions,
		NodeCount:     len(nodes),
		Checksum:      checksum,
	}, nil
}

// exportReadError wraps a failure to read the store for an export with the
// remedy (MTIX-95.31.1): an export that cannot read a row or a column (for
// example a JSON cell that does not parse, named in err) cannot be written,
// and mtix recover salvages everything that is readable.
func exportReadError(what string, err error) error {
	return fmt.Errorf("%s: %w (run 'mtix recover' to salvage everything readable)", what, err)
}

// exportNodeSelectSQL is the canonical node projection shared by bulk
// export, merge import (the local copy of a node) and the per-row salvage
// path in recover.go. It reads every nodes column (MTIX-95.31.1). Column
// order MUST stay in sync with scanExportNode.
const exportNodeSelectSQL = `SELECT id, COALESCE(parent_id,''), depth, seq, project,
		        title, COALESCE(description,''), COALESCE(prompt,''),
		        COALESCE(acceptance,''), COALESCE(node_type,'auto'),
		        COALESCE(issue_type,''), priority, COALESCE(labels,'[]'),
		        status, progress, COALESCE(assignee,''), COALESCE(creator,''),
		        COALESCE(agent_state,''), weight, COALESCE(content_hash,''),
		        created_at, updated_at, COALESCE(closed_at,''),
		        COALESCE(defer_until,''), COALESCE(deleted_at,''),
		        COALESCE(uid,''), COALESCE(previous_status,''),
		        estimate_min, actual_min, code_refs, commit_refs, annotations,
		        COALESCE(invalidated_at,''), COALESCE(invalidated_by,''),
		        COALESCE(invalidation_reason,''), activity,
		        COALESCE(deleted_by,''), COALESCE(metadata,''),
		        COALESCE(session_id,'')
		 FROM nodes`

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

// scanExportNode reads one exportNode from a row produced by
// exportNodeSelectSQL, decoding the JSON columns into the structure mtix
// show --json returns (MTIX-95.31.1). A JSON column that does not parse is
// left empty and reported with errUnreadableNodeColumn; the rest of the
// node is still returned.
func scanExportNode(row rowScanner) (exportNode, error) {
	var n exportNode
	var j exportNodeJSON
	err := row.Scan(
		&n.ID, &n.ParentID, &n.Depth, &n.Seq, &n.Project,
		&n.Title, &n.Description, &n.Prompt, &n.Acceptance, &n.NodeType,
		&n.IssueType, &n.Priority, &n.Labels, &n.Status, &n.Progress,
		&n.Assignee, &n.Creator, &n.AgentState, &n.Weight, &n.ContentHash,
		&n.CreatedAt, &n.UpdatedAt, &n.ClosedAt, &n.DeferUntil, &n.DeletedAt,
		&n.UID, &n.PreviousStatus, &j.estimateMin, &j.actualMin,
		&j.codeRefs, &j.commitRefs, &j.annotations,
		&n.InvalidatedAt, &n.InvalidatedBy, &n.InvalidationReason, &j.activity,
		&n.DeletedBy, &n.Metadata, &n.SessionID,
	)
	if err != nil {
		return n, err
	}
	decodeErr := j.decodeInto(&n)
	return n, decodeErr
}

// exportNodes reads all nodes (including soft-deleted) for export.
func (s *Store) exportNodes(ctx context.Context) ([]exportNode, error) {
	rows, err := s.readDB.QueryContext(ctx, exportNodeSelectSQL+" ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("query nodes: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			s.logger.Error("failed to close export node rows", "error", closeErr)
		}
	}()

	var nodes []exportNode
	for rows.Next() {
		n, err := scanExportNode(rows)
		if err != nil {
			return nil, fmt.Errorf("scan export node: %w", err)
		}
		// node_type is canonical = depth-derived. Override any stored value
		// to match the import-side normalization (see import.go:232,255).
		// This makes export -> import -> export byte-idempotent (modulo
		// exported_at) and self-heals legacy DBs from pre-v0.1.1-beta where
		// the depth-to-type mapping was inverted (MTIX-12).
		canonical := string(model.NodeTypeForDepth(n.Depth))
		if n.NodeType != canonical {
			s.logger.Debug("export normalized stored node_type",
				"id", n.ID, "depth", n.Depth, "stored", n.NodeType, "canonical", canonical)
			n.NodeType = canonical
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// exportDependencies reads all dependencies for export.
func (s *Store) exportDependencies(ctx context.Context) ([]exportDep, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT from_id, to_id, dep_type, created_at
		 FROM dependencies ORDER BY from_id, to_id`)
	if err != nil {
		return nil, fmt.Errorf("query deps: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			s.logger.Error("failed to close export dep rows", "error", closeErr)
		}
	}()

	var deps []exportDep
	for rows.Next() {
		var d exportDep
		if err := rows.Scan(&d.FromID, &d.ToID, &d.DepType, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan export dep: %w", err)
		}
		deps = append(deps, d)
	}
	return deps, rows.Err()
}

// exportAgents reads all agents for export.
func (s *Store) exportAgents(ctx context.Context) ([]exportAgent, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT agent_id, project, COALESCE(state,'idle'),
		        COALESCE(current_node_id,''), COALESCE(last_heartbeat,'')
		 FROM agents ORDER BY agent_id`)
	if err != nil {
		return nil, fmt.Errorf("query agents: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			s.logger.Error("failed to close export agent rows", "error", closeErr)
		}
	}()

	var agents []exportAgent
	for rows.Next() {
		var a exportAgent
		if err := rows.Scan(&a.AgentID, &a.Project, &a.State,
			&a.CurrentNodeID, &a.LastHeartbeat); err != nil {
			return nil, fmt.Errorf("scan export agent: %w", err)
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

// exportSessions reads all sessions for export.
func (s *Store) exportSessions(ctx context.Context) ([]exportSession, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT id, agent_id, project, started_at, COALESCE(ended_at,''),
		        COALESCE(status,'active'), COALESCE(summary,'')
		 FROM sessions ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			s.logger.Error("failed to close export session rows", "error", closeErr)
		}
	}()

	var sessions []exportSession
	for rows.Next() {
		var sess exportSession
		if err := rows.Scan(&sess.ID, &sess.AgentID, &sess.Project,
			&sess.StartedAt, &sess.EndedAt, &sess.Status, &sess.Summary); err != nil {
			return nil, fmt.Errorf("scan export session: %w", err)
		}
		sessions = append(sessions, sess)
	}
	return sessions, rows.Err()
}

// exportChecksumDoc is the canonical document the export checksum hashes:
// the nodes and dependencies, sorted by primary key (FR-7.8).
type exportChecksumDoc struct {
	Nodes []exportNode `json:"nodes"`
	Deps  []exportDep  `json:"deps"`
}

// computeExportChecksum computes the SHA-256 checksum of the canonical JSON
// of the sorted nodes and dependencies per FR-7.8, annotations and every
// other exported column included (MTIX-95.31.1).
//
// It hashes the export as every reader decodes it (MTIX-107.39).
// encoding/json cannot write invalid UTF-8: it writes each such byte as the
// escape \ufffd, a reader decodes that escape to U+FFFD, and U+FFFD encodes
// as the raw character. Hashing the first encoding described bytes no
// reader could reproduce, so a tasks.json whose text held invalid UTF-8
// never verified, not even the copy mtix had just written. The canonical
// JSON is therefore encoded, decoded and encoded again, so the writer and
// every reader hash the same bytes. For valid UTF-8 the second encoding
// equals the first, so the checksum of every other export is unchanged.
func computeExportChecksum(nodes []exportNode, deps []exportDep) (string, error) {
	first, err := json.Marshal(exportChecksumDoc{Nodes: nodes, Deps: deps})
	if err != nil {
		return "", fmt.Errorf("marshal for checksum: %w", err)
	}
	var decoded exportChecksumDoc
	if decodeErr := json.Unmarshal(first, &decoded); decodeErr != nil {
		return "", fmt.Errorf("decode for checksum: %w", decodeErr)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return "", fmt.Errorf("re-encode for checksum: %w", err)
	}

	hash := sha256.Sum256(canonical)
	return fmt.Sprintf("%x", hash), nil
}

// legacyReplacementChecksum returns the checksum mtix 0.5.3 and earlier
// wrote for this content when its text held invalid UTF-8 (MTIX-107.39), or
// "" when the content holds no U+FFFD and so has no such alternative. Those
// versions hashed the first encoding, in which each invalid byte appeared
// as the six-character escape \ufffd, and a reader decodes each escape to
// U+FFFD. Spelling every U+FFFD of the canonical JSON as that escape
// restores the bytes they hashed, provided the stored text held no genuine
// U+FFFD; a file holding both still fails verification.
func legacyReplacementChecksum(nodes []exportNode, deps []exportDep) (string, error) {
	canonical, err := json.Marshal(exportChecksumDoc{Nodes: nodes, Deps: deps})
	if err != nil {
		return "", fmt.Errorf("marshal for legacy checksum: %w", err)
	}
	replacement := []byte(string(utf8.RuneError))
	if !bytes.Contains(canonical, replacement) {
		return "", nil
	}
	spelled := bytes.ReplaceAll(canonical, replacement, []byte(`\ufffd`))
	hash := sha256.Sum256(spelled)
	return fmt.Sprintf("%x", hash), nil
}

// RecomputeExportChecksum canonicalizes an export in place — sorts nodes
// and dependencies, fixes node_count — and stores a freshly computed
// checksum. It exists for the recovery path (MTIX-26.5): salvaged or
// hand-reconstructed exports carry stale checksums that would otherwise
// be rejected by import's integrity verification. Callers MUST surface
// loudly that integrity now attests to the reconstructed content, not
// the original.
func RecomputeExportChecksum(export *ExportData) error {
	if export == nil {
		return fmt.Errorf("nil export data: %w", model.ErrInvalidInput)
	}

	sort.Slice(export.Nodes, func(i, j int) bool { return export.Nodes[i].ID < export.Nodes[j].ID })
	sort.Slice(export.Dependencies, func(i, j int) bool {
		if export.Dependencies[i].FromID != export.Dependencies[j].FromID {
			return export.Dependencies[i].FromID < export.Dependencies[j].FromID
		}
		return export.Dependencies[i].ToID < export.Dependencies[j].ToID
	})
	export.NodeCount = len(export.Nodes)

	checksum, err := computeExportChecksum(export.Nodes, export.Dependencies)
	if err != nil {
		return fmt.Errorf("compute checksum: %w", err)
	}
	export.Checksum = checksum
	return nil
}

// VerifyExportChecksum validates an export's checksum matches its content.
// Used during import to verify data integrity per FR-7.8. It also accepts
// the checksum mtix 0.5.3 and earlier wrote for text holding invalid UTF-8
// (MTIX-107.39): the same decoded content under the spelling those
// versions hashed (legacyReplacementChecksum). Any edit to the content
// fails both.
func VerifyExportChecksum(export *ExportData) (bool, error) {
	if export == nil {
		return false, fmt.Errorf("nil export data: %w", model.ErrInvalidInput)
	}

	computed, err := computeExportChecksum(export.Nodes, export.Dependencies)
	if err != nil {
		return false, fmt.Errorf("compute checksum: %w", err)
	}
	if computed == export.Checksum {
		return true, nil
	}

	legacy, err := legacyReplacementChecksum(export.Nodes, export.Dependencies)
	if err != nil {
		return false, fmt.Errorf("compute legacy checksum: %w", err)
	}
	return legacy != "" && legacy == export.Checksum, nil
}
