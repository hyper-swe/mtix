// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package bootstrap carries the FR-21 §6.7 clone path: the snapshot a
// peer joining after pruning — or recovering from a condemned segment —
// imports before it starts tailing.
//
// Snapshots live INSIDE the relay (D-R13). A sneakernet courier should
// carry one directory and the joiner should need nothing else; a
// snapshot parked beside the relay is a second thing to remember and
// the one people forget.
//
// Two rules shape everything here. The size cap is a LOUD REFUSAL and
// never a truncation: a truncated snapshot is a store copy missing rows
// nobody can name, and a joiner that imported one would carry a
// silently incomplete history forever. And a snapshot is a full
// plaintext store copy, so it is pruned once consumed and flagged when
// stale — §8.4's privacy posture applies here at double strength.
//
// The package touches the store only through the narrow interface
// below, and imports through the shipped reconcile path so §14.6's uid
// rules apply to a bootstrap exactly as they do to any other import.
package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// DirName is the snapshot directory inside the relay.
const DirName = "bootstrap"

// FileExt is a snapshot's extension. The grammar is strict for the same
// reason the segment grammar is: anything else in the directory is left
// alone rather than parsed or removed.
const FileExt = ".mrsnap"

// DefaultMaxBytes caps a snapshot. A relay may sit on removable media
// with far less room than a store, and discovering that halfway through
// a courier run is worse than refusing up front.
const DefaultMaxBytes int64 = 256 << 20

// snapshotFileMode is a snapshot's permission. It is a full plaintext
// store copy, so it is owner-only.
const snapshotFileMode os.FileMode = 0o600

// peerIDPattern is the FR-21 §5.2 identity grammar.
var peerIDPattern = regexp.MustCompile(`^[0-9a-f]{16}(-[a-z0-9_-]{1,32})?$`)

// snapshotNamePattern matches a snapshot file name.
var snapshotNamePattern = regexp.MustCompile(`^snap-[0-9]{14}-[0-9a-f]{16}(-[a-z0-9_-]{1,32})?\.mrsnap$`)

// Refusals.
var (
	// ErrSnapshotTooLarge refuses a snapshot past the cap. It is a
	// refusal and never a truncation.
	ErrSnapshotTooLarge = errors.New("relay bootstrap snapshot exceeds the size cap")

	// ErrSnapshotUnusable marks a file that cannot be read as a
	// snapshot.
	ErrSnapshotUnusable = errors.New("relay bootstrap snapshot is unusable")
)

// Exporter is the store slice a snapshot export needs.
type Exporter interface {
	Export(ctx context.Context, project, mtixVersion string) (*sqlite.ExportData, error)
}

// Importer is the store slice a snapshot import needs. It is the
// shipped reconcile path, so §14.6's uid rules apply unchanged.
type Importer interface {
	ImportReconcile(ctx context.Context, data *sqlite.ExportData, opts sqlite.ImportReconcileOptions) (
		*sqlite.ImportReconcileReport, *sqlite.ImportResult, error)
}

// Snapshot is what lands in the relay: the existing export format plus
// the stamped per-peer relay sequences a joiner tails from.
type Snapshot struct {
	// ExportedBy is the peer that produced it.
	ExportedBy string `json:"exported_by"`

	// CreatedAt is when, supplied by the caller rather than read from a
	// clock so a snapshot is reproducible.
	CreatedAt time.Time `json:"created_at"`

	// Positions is each peer's relay sequence as of the export. A
	// joiner begins tailing from these rather than from the start,
	// which is the whole point of bootstrapping after a prune.
	Positions map[string]uint64 `json:"positions"`

	// Export is the store copy, in the shipped export format.
	Export *sqlite.ExportData `json:"export"`
}

// ExportRequest configures producing a snapshot.
type ExportRequest struct {
	Store      Exporter
	RelayDir   string
	Project    string
	ExportedBy string
	CreatedAt  time.Time

	// Positions is the stamped rs vector.
	Positions map[string]uint64

	// MtixVersion is stamped into the export; optional.
	MtixVersion string

	// MaxBytes caps the snapshot; zero takes the default.
	MaxBytes int64
}

// ExportResult reports what was written.
type ExportResult struct {
	Path  string
	Bytes int64
}

// ExportSnapshot writes a bootstrap snapshot into the relay.
//
// The whole snapshot is rendered in memory and measured BEFORE anything
// reaches the medium, so the cap can refuse rather than truncate. A
// partial snapshot is the one outcome this must never produce.
func ExportSnapshot(ctx context.Context, req ExportRequest) (ExportResult, error) {
	if err := req.validate(); err != nil {
		return ExportResult{}, err
	}
	data, err := req.Store.Export(ctx, req.Project, req.MtixVersion)
	if err != nil {
		return ExportResult{}, fmt.Errorf("export for relay bootstrap: %w", err)
	}

	snap := Snapshot{
		ExportedBy: req.ExportedBy,
		CreatedAt:  req.CreatedAt.UTC(),
		Positions:  req.Positions,
		Export:     data,
	}
	body, err := json.MarshalIndent(&snap, "", "  ")
	if err != nil {
		return ExportResult{}, fmt.Errorf("encode relay bootstrap snapshot: %w", err)
	}
	body = append(body, '\n')

	maxBytes := req.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if int64(len(body)) > maxBytes {
		return ExportResult{}, fmt.Errorf("%w: %d bytes against a cap of %d; raise the cap or prune the project first",
			ErrSnapshotTooLarge, len(body), maxBytes)
	}

	dir := filepath.Join(req.RelayDir, DirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ExportResult{}, fmt.Errorf("create %s: %w", dir, err)
	}
	path := filepath.Join(dir, snapshotName(req.CreatedAt.UTC(), req.ExportedBy))
	if err := os.WriteFile(path, body, snapshotFileMode); err != nil {
		return ExportResult{}, fmt.Errorf("write %s: %w", path, err)
	}
	return ExportResult{Path: path, Bytes: int64(len(body))}, nil
}

// validate refuses a request that could not produce an importable
// snapshot.
func (r ExportRequest) validate() error {
	if r.Store == nil {
		return errors.New("relay bootstrap: store is required")
	}
	if !peerIDPattern.MatchString(r.ExportedBy) {
		return fmt.Errorf("relay bootstrap: exporting peer %q is malformed", r.ExportedBy)
	}
	if r.CreatedAt.IsZero() {
		return errors.New("relay bootstrap: creation time is required")
	}
	for peer := range r.Positions {
		if !peerIDPattern.MatchString(peer) {
			return fmt.Errorf("relay bootstrap: stamped position names peer %q", peer)
		}
	}
	return nil
}

// snapshotName renders a snapshot's file name: sortable by time, and
// carrying the peer that produced it so a courier can tell two apart.
func snapshotName(at time.Time, peer string) string {
	return fmt.Sprintf("snap-%s-%s%s", at.Format("20060102150405"), peer, FileExt)
}

// ImportRequest configures consuming a snapshot.
type ImportRequest struct {
	Store Importer
	Path  string

	// Options are passed to the shipped reconcile path unchanged, so a
	// bootstrap gets the same uid treatment as any other import.
	Options sqlite.ImportReconcileOptions
}

// ImportResult reports what a joiner took on.
type ImportResult struct {
	// Positions are the stamped relay sequences to begin tailing from.
	Positions map[string]uint64

	// NodesImported is the snapshot's node count.
	NodesImported int

	// Report is the reconcile path's own report, including any
	// idempotent no-ops it absorbed.
	Report *sqlite.ImportReconcileReport
}

// ImportSnapshot applies a snapshot and returns where to start tailing.
func ImportSnapshot(ctx context.Context, req ImportRequest) (ImportResult, error) {
	if req.Store == nil {
		return ImportResult{}, errors.New("relay bootstrap: store is required")
	}
	snap, err := readSnapshot(req.Path)
	if err != nil {
		return ImportResult{}, err
	}
	report, _, err := req.Store.ImportReconcile(ctx, snap.Export, req.Options)
	if err != nil {
		return ImportResult{}, fmt.Errorf("import relay bootstrap snapshot: %w", err)
	}
	return ImportResult{
		Positions:     snap.Positions,
		NodesImported: snap.Export.NodeCount,
		Report:        report,
	}, nil
}

// readSnapshot loads and validates one snapshot file.
func readSnapshot(path string) (*Snapshot, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("lstat %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %s is a symlink", ErrSnapshotUnusable, path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrSnapshotUnusable, path)
	}
	body, err := os.ReadFile(path) // #nosec G304 -- path is the caller's chosen snapshot
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var snap Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrSnapshotUnusable, path, err)
	}
	if !peerIDPattern.MatchString(snap.ExportedBy) {
		return nil, fmt.Errorf("%w: %s names exporting peer %q", ErrSnapshotUnusable, path, snap.ExportedBy)
	}
	if snap.Export == nil {
		return nil, fmt.Errorf("%w: %s carries no export", ErrSnapshotUnusable, path)
	}
	return &snap, nil
}

// PruneRequest configures snapshot cleanup.
type PruneRequest struct {
	// Acked is each peer's relay sequence as the pruner understands it.
	// A snapshot goes once every peer it stamped has passed the stamp.
	Acked map[string]uint64

	// Now is the clock, injected.
	Now time.Time
}

// PruneSnapshots removes snapshots every stamped peer has caught up to,
// returning the names removed.
//
// A snapshot is a full plaintext store copy, so removing a consumed one
// is a privacy measure rather than housekeeping. One that anybody might
// still need stays — the cost of keeping it is disk, and the cost of
// removing it early is a joiner that cannot join.
func PruneSnapshots(relayDir string, req PruneRequest) ([]string, error) {
	files, err := listSnapshots(relayDir)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, f := range files {
		snap, err := readSnapshot(f.path)
		if err != nil {
			// A damaged snapshot is left alone: it is not this
			// function's job to decide the medium is wrong, and a
			// courier may still be mid-copy.
			continue
		}
		if !consumed(snap.Positions, req.Acked) {
			continue
		}
		if err := os.Remove(f.path); err != nil {
			return removed, fmt.Errorf("remove %s: %w", f.path, err)
		}
		removed = append(removed, f.name)
	}
	sort.Strings(removed)
	return removed, nil
}

// consumed reports whether every peer the snapshot stamped has read
// past that stamp.
func consumed(stamped, acked map[string]uint64) bool {
	for peer, rs := range stamped {
		if acked[peer] < rs {
			return false
		}
	}
	return true
}

// StaleRequest configures the staleness report.
type StaleRequest struct {
	Now           time.Time
	RetentionDays int
}

// Stale is one snapshot older than retention.
type Stale struct {
	Name    string
	Path    string
	AgeDays int
}

// StaleSnapshots reports snapshots older than the retention window.
//
// It reports and never removes. A stale snapshot may still be the only
// route a courier has, so the decision is an operator's — but leaving a
// full plaintext store copy on shared media indefinitely is exactly the
// §8.4 hazard worth naming out loud.
func StaleSnapshots(relayDir string, req StaleRequest) ([]Stale, error) {
	retentionDays := req.RetentionDays
	if retentionDays <= 0 {
		retentionDays = 7
	}
	files, err := listSnapshots(relayDir)
	if err != nil {
		return nil, err
	}
	var out []Stale
	for _, f := range files {
		age := int(req.Now.Sub(f.modTime).Hours() / 24)
		if age < retentionDays {
			continue
		}
		out = append(out, Stale{Name: f.name, Path: f.path, AgeDays: age})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// snapshotFile is one entry in the bootstrap directory.
type snapshotFile struct {
	name    string
	path    string
	modTime time.Time
}

// listSnapshots enumerates the bootstrap directory under the same
// Lstat discipline the rest of the relay uses. An absent directory is
// simply no snapshots.
func listSnapshots(relayDir string) ([]snapshotFile, error) {
	dir := filepath.Join(relayDir, DirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var out []snapshotFile
	for _, e := range entries {
		if !snapshotNamePattern.MatchString(e.Name()) || !e.Type().IsRegular() {
			// Anything that is not a snapshot is left entirely alone —
			// never parsed, never removed.
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", filepath.Join(dir, e.Name()), err)
		}
		out = append(out, snapshotFile{
			name: e.Name(), path: filepath.Join(dir, e.Name()), modTime: info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// SnapshotNames lists the snapshot file names in a relay, for status.
func SnapshotNames(relayDir string) ([]string, error) {
	files, err := listSnapshots(relayDir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.name)
	}
	return names, nil
}
