// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// SyncStatus is the JSON-serializable shape returned by `mtix sync
// status --json`. Lives here (not in a separate package) because no
// other consumer needs it; if 15.8 MCP integration adds machine
// access to status, the type can be lifted then.
type SyncStatus struct {
	Pending       int    `json:"pending"`
	Pushed        int    `json:"pushed"`
	Conflicted    int    `json:"conflicted"`
	Applied       int    `json:"applied"`
	Lamport       int64  `json:"lamport"`
	LastPulled    int64  `json:"last_pulled_clock"`
	MachineHash   string `json:"machine_hash,omitempty"`
	ProjectPrefix string `json:"project_prefix,omitempty"`
	OpenConflicts int    `json:"open_conflicts"`
	// LastSweepAt is meta.sync.last_sweep_at: the hub's clock (RFC 3339,
	// UTC) at the start of the last late-event sweep of sync pull
	// (MTIX-95.5). Empty when no sweep has completed on this store.
	LastSweepAt string `json:"last_sweep_at"`
	// FullSweepInProgress is true while no sweep has completed and the
	// full hub comparison is part-way done: it has saved listing progress
	// (meta.sync.sweep_after_id) or staged ids not yet applied. The next
	// pull resumes it (MTIX-95.5).
	FullSweepInProgress bool `json:"full_sweep_in_progress"`
	// SweepPendingEvents counts the late events a sweep has listed and
	// staged but not yet applied (sync_sweep_pending, MTIX-95.5).
	SweepPendingEvents int `json:"sweep_pending_events"`
	// HighConflict is the FR-18.12 banner trigger: true when
	// open_conflicts > 50 so the human-readable output adds a
	// guidance banner.
	HighConflict bool `json:"high_conflict"`
	// QuarantinedEvents counts the pulled events held in the local
	// quarantine, not applied (sync_quarantine, MTIX-95.11). Every pull
	// retries them.
	QuarantinedEvents int `json:"quarantined_events"`
}

// newSyncStatusCmd creates `mtix sync status` per FR-18 / MTIX-15.7.3.
// Pure local read; no PG round-trip. Use `mtix sync doctor` for hub
// reachability checks.
func newSyncStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show local sync state (counts + sentinels)",
		Long: `Show the local sync queue counts plus meta sentinels. Pure local
read — does not touch the hub. Use 'mtix sync doctor' to verify hub
reachability and schema currency.

When unresolved conflicts exceed 50, surfaces the FR-18.12 banner
pointing at 'mtix sync conflicts list --batch'.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSyncStatus(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	return cmd
}

func runSyncStatus(ctx context.Context, stdout, stderr io.Writer) error {
	if app.mtixDir == "" {
		return fmt.Errorf("mtix sync status: not in an mtix project (run 'mtix init' first)")
	}
	if app.store == nil {
		return fmt.Errorf("mtix sync status: local store not initialized")
	}

	st, err := readSyncStatus(ctx, app.store)
	if err != nil {
		return wrapSyncErr(stderr, "read status", err)
	}

	if app.jsonOutput {
		return printStatusJSON(stdout, st)
	}
	return printStatusTable(stdout, st)
}

// readSyncStatus aggregates counts + sentinels into one struct via
// readDB-only queries (no write tx), including the time of the last
// late-event sweep (meta.sync.last_sweep_at, MTIX-95.5).
func readSyncStatus(ctx context.Context, store *sqlite.Store) (SyncStatus, error) {
	var st SyncStatus

	for _, row := range []struct {
		status string
		dst    *int
	}{
		{"pending", &st.Pending},
		{"pushed", &st.Pushed},
		{"conflicted", &st.Conflicted},
		{"applied", &st.Applied},
	} {
		if err := store.QueryRow(ctx,
			`SELECT COUNT(*) FROM sync_events WHERE sync_status = ?`, row.status,
		).Scan(row.dst); err != nil {
			return st, fmt.Errorf("count %s: %w", row.status, err)
		}
	}

	if err := store.QueryRow(ctx,
		`SELECT COUNT(*) FROM sync_conflicts`,
	).Scan(&st.OpenConflicts); err != nil {
		return st, fmt.Errorf("count conflicts: %w", err)
	}
	// Late events a sweep has staged but not yet applied (MTIX-95.5).
	if err := store.QueryRow(ctx,
		`SELECT COUNT(*) FROM sync_sweep_pending`,
	).Scan(&st.SweepPendingEvents); err != nil {
		return st, fmt.Errorf("count staged late events: %w", err)
	}
	st.HighConflict = st.OpenConflicts > 50
	if err := readQuarantineStatus(ctx, store, &st); err != nil {
		return st, err
	}

	if err := readStatusSentinels(ctx, store, &st); err != nil {
		return st, err
	}
	return st, nil
}

// readStatusSentinels fills st from the meta.sync.* sentinels, including
// the late-event sweep's last sweep time and whether a full hub comparison
// is part-way done (MTIX-95.5).
func readStatusSentinels(ctx context.Context, store *sqlite.Store, st *SyncStatus) error {
	var fullSweepAfterID string
	for _, kv := range []struct {
		key string
		dst interface{}
	}{
		{"meta.sync.lamport", &st.Lamport},
		{"meta.sync.last_pulled_clock", &st.LastPulled},
		{"meta.sync.machine_hash", &st.MachineHash},
		{"meta.sync.project_prefix", &st.ProjectPrefix},
		{"meta.sync.last_sweep_at", &st.LastSweepAt},
		{"meta.sync.sweep_after_id", &fullSweepAfterID},
	} {
		var raw string
		if err := store.QueryRow(ctx,
			`SELECT value FROM meta WHERE key = ?`, kv.key,
		).Scan(&raw); err != nil {
			return fmt.Errorf("read %s: %w", kv.key, err)
		}
		switch dst := kv.dst.(type) {
		case *int64:
			v, parseErr := strconv.ParseInt(raw, 10, 64)
			if parseErr != nil {
				return fmt.Errorf("parse %s %q: %w", kv.key, raw, parseErr)
			}
			*dst = v
		case *string:
			*dst = raw
		}
	}
	st.FullSweepInProgress = (fullSweepAfterID != "" || st.SweepPendingEvents > 0) && st.LastSweepAt == ""
	return nil
}

func printStatusJSON(w io.Writer, st SyncStatus) error {
	body, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(body))
	return err
}

func printStatusTable(w io.Writer, st SyncStatus) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	rows := [][2]string{
		{"project_prefix", emptyDash(st.ProjectPrefix)},
		{"machine_hash", emptyDash(st.MachineHash)},
		{"", ""},
		{"pending", strconv.Itoa(st.Pending)},
		{"pushed", strconv.Itoa(st.Pushed)},
		{"conflicted", strconv.Itoa(st.Conflicted)},
		{"applied", strconv.Itoa(st.Applied)},
		{"open conflicts", strconv.Itoa(st.OpenConflicts)},
		{"quarantined events", strconv.Itoa(st.QuarantinedEvents)},
		{"", ""},
		{"local lamport", strconv.FormatInt(st.Lamport, 10)},
		{"last pulled clock", strconv.FormatInt(st.LastPulled, 10)},
		{"last sweep", lastSweepLabel(st)},
	}
	for _, r := range rows {
		if r[0] == "" {
			fmt.Fprintln(tw)
			continue
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\n", r[0], r[1]); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if st.HighConflict {
		fmt.Fprintf(w,
			"\nWARN: %d unresolved conflicts. Run 'mtix sync conflicts list --batch <node-id>' for batch resolution.\n",
			st.OpenConflicts)
	}
	return nil
}

// lastSweepLabel renders meta.sync.last_sweep_at for the status table:
// the recorded hub time, or "never" before the first late-event sweep has
// completed, noting a full hub comparison that is part-way done (MTIX-95.5).
func lastSweepLabel(st SyncStatus) string {
	switch {
	case st.LastSweepAt != "":
		return st.LastSweepAt
	case st.FullSweepInProgress:
		return "never (full hub comparison in progress)"
	default:
		return "never"
	}
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
