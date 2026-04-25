// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// SchemaVersionV1 is the current export schema version per FR-15.2g.
// Auto-import rejects files with a higher major version.
const SchemaVersionV1 = "1.0.0"

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

// exportNode is the JSON representation of a node in the export.
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
		return nil, fmt.Errorf("export nodes: %w", err)
	}

	deps, err := s.exportDependencies(ctx)
	if err != nil {
		return nil, fmt.Errorf("export dependencies: %w", err)
	}

	agents, err := s.exportAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("export agents: %w", err)
	}

	sessions, err := s.exportSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("export sessions: %w", err)
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

// exportNodes reads all nodes (including soft-deleted) for export.
func (s *Store) exportNodes(ctx context.Context) ([]exportNode, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT id, COALESCE(parent_id,''), depth, seq, project,
		        title, COALESCE(description,''), COALESCE(prompt,''),
		        COALESCE(acceptance,''), COALESCE(node_type,'auto'),
		        COALESCE(issue_type,''), priority, COALESCE(labels,'[]'),
		        status, progress, COALESCE(assignee,''), COALESCE(creator,''),
		        COALESCE(agent_state,''), weight, COALESCE(content_hash,''),
		        created_at, updated_at, COALESCE(closed_at,''),
		        COALESCE(defer_until,''), COALESCE(deleted_at,'')
		 FROM nodes ORDER BY id`)
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
		var n exportNode
		if err := rows.Scan(
			&n.ID, &n.ParentID, &n.Depth, &n.Seq, &n.Project,
			&n.Title, &n.Description, &n.Prompt, &n.Acceptance, &n.NodeType,
			&n.IssueType, &n.Priority, &n.Labels, &n.Status, &n.Progress,
			&n.Assignee, &n.Creator, &n.AgentState, &n.Weight, &n.ContentHash,
			&n.CreatedAt, &n.UpdatedAt, &n.ClosedAt, &n.DeferUntil, &n.DeletedAt,
		); err != nil {
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

// computeExportChecksum computes SHA-256 checksum of canonical JSON per FR-7.8.
// The checksum covers sorted nodes and dependencies for reproducibility.
func computeExportChecksum(nodes []exportNode, deps []exportDep) (string, error) {
	// Marshal to canonical JSON (sorted by primary key).
	canonical := struct {
		Nodes []exportNode `json:"nodes"`
		Deps  []exportDep  `json:"deps"`
	}{Nodes: nodes, Deps: deps}

	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal for checksum: %w", err)
	}

	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash), nil
}

// VerifyExportChecksum validates an export's checksum matches its content.
// Used during import to verify data integrity per FR-7.8.
func VerifyExportChecksum(export *ExportData) (bool, error) {
	if export == nil {
		return false, fmt.Errorf("nil export data: %w", model.ErrInvalidInput)
	}

	computed, err := computeExportChecksum(export.Nodes, export.Dependencies)
	if err != nil {
		return false, fmt.Errorf("compute checksum: %w", err)
	}

	return computed == export.Checksum, nil
}
