// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/hyper-swe/mtix/internal/relay/bootstrap"
	"github.com/hyper-swe/mtix/internal/relay/lifecycle"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/relay/tick"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
	"github.com/spf13/cobra"
)

// relayKeysSubdir is where this peer keeps its epoch keys, under the
// local .mtix directory rather than on the shared medium: the medium
// carries what the fleet authenticates, never what it authenticates
// with.
const relayKeysSubdir = "relay/keys"

// newSyncRelayCmd builds the FR-21 relay command group.
//
// Every verb here is a thin wrapper over the shipped relay packages.
// None of them adds semantics — the engine decided what a prune, a
// rotation or a repair means, and this layer only names them for an
// operator and reports what happened.
func newSyncRelayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "relay",
		Short: "EXPERIMENTAL: manage the file-based sync relay (FR-21)",
		Long: "EXPERIMENTAL and opt-in. Operate a sync transport that carries events\n" +
			"through a shared directory — a mounted folder, a network filer,\n" +
			"removable media — for peers that cannot reach a database.\n\n" +
			"In this release the relay is experimental, meaning supported for trial use:\n" +
			"its behavior and on-disk format may change in 0.6.0, and it is not yet\n" +
			"covered by the release gate. It is opt-in: nothing changes unless\n" +
			"sync.relay.dir is set.",
	}
	cmd.AddCommand(
		newRelayInitCmd(),
		newRelayAttachCmd(),
		newRelayStatusCmd(),
		newRelayTickCmd(),
		newRelayRotateKeyCmd(),
		newRelayResetPeerCmd(),
		newRelayRetirePeerCmd(),
		newRelayCloneCmd(),
		newRelayRepublishCmd(),
	)
	return cmd
}

// relayDir resolves the configured relay directory, or "" when none is
// configured. An unconfigured relay is not an error anywhere: a store
// that never wanted this transport should not have to say so.
func relayDir() string {
	dir, err := relayDirChecked()
	if err != nil {
		return ""
	}
	return dir
}

// relayDirChecked is relayDir with the read error kept, for the replace
// guards, which fail closed when the evidence cannot be read (MTIX-107.31.8).
func relayDirChecked() (string, error) {
	if app.configSvc == nil {
		return "", nil
	}
	dir, err := app.configSvc.Get("sync.relay.dir")
	if err != nil {
		return "", fmt.Errorf("read sync.relay.dir: %w", err)
	}
	return dir, nil
}

// relayConfigInt reads a numeric relay setting, falling back to its
// default when unset or unreadable.
func relayConfigInt(key string, fallback int) int {
	if app.configSvc == nil {
		return fallback
	}
	raw, err := app.configSvc.Get(key)
	if err != nil || raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

// relayRequireAuth reports whether this peer insists on an
// authenticated relay. It defaults on: the medium is a less trusted
// channel than a hub, so running without MACs is an explicit opt-out.
func relayRequireAuth() bool {
	if app.configSvc == nil {
		return true
	}
	raw, err := app.configSvc.Get("sync.relay.require_auth")
	if err != nil {
		return true
	}
	return raw != "false"
}

// relayPeerID returns this peer's identity under peers/.
//
// An operator-assigned id wins when configured. That is what a peer
// whose workspace is rebuilt somewhere else between sessions needs: a
// machine-derived id would change with the machine, and the peer would
// rejoin as a new member each time — leaving the abandoned one holding
// everyone's retention window open until someone retired it (FR-21
// §7.1).
//
// Unset, the id derives from the machine, which is right whenever the
// machine is the thing that persists.
func relayPeerID() (string, error) {
	if app.configSvc != nil {
		if configured, err := app.configSvc.Get("sync.relay.peer_id"); err == nil && configured != "" {
			// Already validated at `config set`; re-checked here because
			// a config file can also be edited by hand.
			if err := segment.ValidatePeerID(configured); err != nil {
				return "", fmt.Errorf("sync.relay.peer_id %q is malformed: %w", configured, err)
			}
			return configured, nil
		}
	}
	hash, err := clock.MachineHash()
	if err != nil {
		return "", fmt.Errorf("resolve relay peer id: %w", err)
	}
	return hash, nil
}

// relayKeysDir is where this peer's epoch keys live locally.
func relayKeysDir() string {
	return filepath.Join(app.mtixDir, relayKeysSubdir)
}

// requireRelay resolves the relay directory or explains how to set one.
func requireRelay() (string, error) {
	dir := relayDir()
	if dir == "" {
		return "", fmt.Errorf("no relay configured; run `mtix config set sync.relay.dir <path>`")
	}
	return dir, nil
}

// openRelayStack builds this peer's publisher and ingestor.
func openRelayStack() (*tick.Stack, error) {
	dir, err := requireRelay()
	if err != nil {
		return nil, err
	}
	peer, err := relayPeerID()
	if err != nil {
		return nil, err
	}
	return tick.Open(tick.StackConfig{
		Store:           app.store,
		RelayDir:        dir,
		KeysDir:         relayKeysDir(),
		PeerID:          peer,
		MaxSegmentBytes: int64(relayConfigInt("sync.relay.max_segment_bytes", 4<<20)),
		Logger:          app.logger,
		Now:             func() time.Time { return time.Now().UTC() },
	})
}

func newRelayTickCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tick",
		Args:  syncExactArgs(0),
		Short: "Run exactly one relay pass: publish, then ingest",
		Long: "One pass and exit — for peers that cannot host a daemon:\n" +
			"a turn-driven agent seat, a cron-locked appliance, a courier laptop.\n" +
			"Such a peer converges on its next tick; it is a deployment, not a\n" +
			"degraded daemon.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			stack, err := openRelayStack()
			if err != nil {
				return err
			}
			res, err := stack.Pass(cmd.Context())
			printRelayPass(cmd, res)
			if err != nil {
				// A pass reports what it could not do and exits zero:
				// relay problems are transport-degraded, and a courier
				// script must not treat a stalled peer as a failed run.
				fmt.Fprintf(cmd.ErrOrStderr(), "relay tick: %s\n", err)
			}
			if app.hooksDisp != nil {
				app.hooksDisp.Dispatch(cmd.Context())
			}
			return nil
		},
	}
}

// printRelayPass renders one pass for an operator.
func printRelayPass(cmd *cobra.Command, res tick.Result) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "published %d, applied %d\n", res.Published, res.Ingest.Applied)
	if res.Ingest.Quarantined > 0 {
		fmt.Fprintf(out, "quarantined %d record(s) from authenticated peers:\n", res.Ingest.Quarantined)
		for _, q := range res.Ingest.Quarantines {
			fmt.Fprintf(out, "  %s rs %d: %s\n", q.PeerID, q.RS, q.Reason)
		}
	}
	if res.Ingest.AuthFailures > 0 {
		fmt.Fprintf(out, "%s: %d authentication failure(s) — check who else can write the relay\n",
			"RELAY_AUTH_FAIL", res.Ingest.AuthFailures)
	}
	for _, s := range res.Ingest.Stalls {
		fmt.Fprintf(out, "stalled on %s", s.PeerID)
		if s.Code != "" {
			fmt.Fprintf(out, " (%s)", s.Code)
		}
		fmt.Fprintf(out, ": %s\n  fix: %s\n", s.Reason, relayStallRecovery(s.Code))
	}
}

// relayGapRecovery is the recovery for a segment missing from a peer's stream
// (MTIX-265). `republish` and `reset-peer` cannot clear it: they append after
// the hole, and the reader stops at the hole before reaching them.
const relayGapRecovery = "restore the missing segment file from a backup of the relay medium (the next poll resumes by itself); " +
	"otherwise ask the user, then rejoin as a new peer in a new project from a snapshot exported by a project already past the hole " +
	"(`mtix sync relay status --json` there shows the peer's ingested_rs at or beyond the last missing sequence) or by the peer that published the stream, if its own store still holds the events it published " +
	"(the mtix-sync skill's refused-clone row has the steps). " +
	"A snapshot from the stalled project stalls again; if no such project exists only restoring the file recovers it. " +
	"`mtix sync relay republish` does not clear a gap"

// relayStallRecovery names the command that clears each stall.
func relayStallRecovery(code string) string {
	switch code {
	case "RELAY_SEGMENT_CORRUPT":
		return "ask that peer to run `mtix sync relay republish --from <rs>`, or re-bootstrap with `mtix sync relay clone`"
	case "RELAY_GAP":
		return relayGapRecovery
	case "RELAY_KEY_ABSENT":
		return "install the missing key epoch under the relay keys directory (mode 0600)"
	case "RELAY_SYMLINK", "RELAY_FOREIGN_ENTRY":
		return "remove the offending entry from the relay directory; mtix never follows or deletes it"
	default:
		return "check that the relay directory is reachable, then retry"
	}
}

func newRelayStatusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Args:  syncExactArgs(0),
		Short: "Show relay peers, positions and outstanding problems",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rep, err := collectRelayStatus(cmd.Context())
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(rep)
			}
			printRelayStatus(cmd, rep)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit machine-readable output")
	return cmd
}

func newRelayResetPeerCmd() *cobra.Command {
	var baseRS uint64
	var floor int64
	cmd := &cobra.Command{
		Use:   "reset-peer",
		Args:  syncExactArgs(0),
		Short: "Declare a new publisher epoch after restoring this store from backup",
		Long: "Use after a restore, when publishing has refused with\n" +
			"RELAY_PUBLISHER_DIVERGED. Bumps this peer's publisher epoch and\n" +
			"republishes from a safe floor, so post-restore work reaches every\n" +
			"reader instead of being silently discarded.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			stack, err := openRelayStack()
			if err != nil {
				return err
			}
			if resetErr := stack.Publisher.ResetPeer(cmd.Context(), floor, baseRS); resetErr != nil {
				return resetErr
			}
			n, err := stack.Publisher.PublishPending(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"publisher epoch bumped; republished %d event(s) from journal floor %d at rs %d\n",
				n, floor, baseRS)
			return nil
		},
	}
	cmd.Flags().Uint64Var(&baseRS, "base-rs", 1, "Relay sequence the new epoch starts at")
	cmd.Flags().Int64Var(&floor, "floor", 0, "Journal position to republish from")
	return cmd
}

func newRelayCloneCmd() *cobra.Command {
	var export bool
	var f importFlags
	cmd := &cobra.Command{
		Use:   "clone",
		Args:  syncExactArgs(0),
		Short: "Export or import a bootstrap snapshot",
		Long: "A peer joining after pruning cannot tail from the start. Any current\n" +
			"peer exports a snapshot into the relay; the joiner imports it and\n" +
			"begins tailing from the stamped positions. Import is for a project that\n" +
			"holds no tasks or does not sync: a project that holds tasks and already\n" +
			"syncs is refused (nothing is written), and rejoins as a NEW peer: a new\n" +
			"project with a new sync.relay.peer_id, then clone there (see the\n" +
			"mtix-sync skill).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := requireRelay()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if export {
				return runRelayCloneExport(ctx, cmd, dir)
			}
			return runRelayCloneImport(ctx, cmd, dir, f)
		},
	}
	cmd.Flags().BoolVar(&export, "export", false, "Produce a snapshot instead of consuming one")
	cmd.Flags().StringVar(&f.prefer, "prefer", "",
		"Import only (a project that does not sync): for every task whose status, assignee, agent state, wake time or deletion state differs from "+
			"the snapshot's, take the snapshot's values (theirs) or keep yours (ours); without a choice such an import "+
			"is reported and writes nothing")
	cmd.Flags().StringSliceVar(&f.theirs, "theirs", nil,
		"Import only (a project that does not sync): comma-separated task ids whose differing workflow values take the snapshot's")
	cmd.Flags().StringSliceVar(&f.ours, "ours", nil,
		"Import only (a project that does not sync): comma-separated task ids whose differing workflow values keep yours")
	return cmd
}

func newRelayRepublishCmd() *cobra.Command {
	var fromRS uint64
	cmd := &cobra.Command{
		Use:   "republish",
		Args:  syncExactArgs(0),
		Short: "Re-emit this peer's events from an earlier relay sequence",
		Long: "Operator gap repair. Re-emits into FRESH segments — no existing file\n" +
			"is touched — and the duplicates are absorbed on arrival, so it is\n" +
			"safe to run more than once.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if fromRS == 0 {
				return fmt.Errorf("--from is required and must name a relay sequence")
			}
			stack, err := openRelayStack()
			if err != nil {
				return err
			}
			n, err := stack.Publisher.RepublishFrom(cmd.Context(), fromRS)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "re-emitted %d event(s) from rs %d into fresh segments\n", n, fromRS)
			return nil
		},
	}
	cmd.Flags().Uint64Var(&fromRS, "from", 0, "Relay sequence to repair from")
	return cmd
}

// runRelayCloneExport writes a bootstrap snapshot into the relay.
func runRelayCloneExport(ctx context.Context, cmd *cobra.Command, dir string) error {
	peer, err := relayPeerID()
	if err != nil {
		return err
	}
	stamp, err := relayStampPositions(ctx, dir, peer)
	if err != nil {
		return err
	}
	prefix, _, err := app.store.GetOrComputeLocalFirstEventHash(ctx)
	if err != nil {
		return err
	}
	res, err := bootstrap.ExportSnapshot(ctx, bootstrap.ExportRequest{
		Store:      app.store,
		RelayDir:   dir,
		Project:    prefix,
		ExportedBy: peer,
		CreatedAt:  time.Now().UTC(),
		Positions:  stamp.Positions,
		Epochs:     stamp.Epochs,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "snapshot written: %s (%d bytes)\n", res.Path, res.Bytes)
	fmt.Fprintf(cmd.OutOrStdout(),
		"NOTE: a snapshot is a full plaintext copy of this store. It is removed once every\n"+
			"stamped peer has read past it; doctor flags one left behind.\n")
	return nil
}

// runRelayCloneImport consumes the newest snapshot in the relay. It refuses a
// store that holds tasks and already syncs (MTIX-112.17.16.43).
// A snapshot task whose status, assignee, agent state, wake time or deletion
// state differs from the local one refuses the import until --prefer,
// --theirs or --ours settles it (MTIX-95.31.13).
func runRelayCloneImport(ctx context.Context, cmd *cobra.Command, dir string, f importFlags) error {
	f.mode = "merge"
	if flagErr := checkWorkflowFlags(f); flagErr != nil {
		return flagErr
	}
	names, err := bootstrap.SnapshotNames(dir)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("no bootstrap snapshot in %s; ask a current peer to run `mtix sync relay clone --export`",
			filepath.Join(dir, bootstrap.DirName))
	}
	if guardErr := refuseSyncedRelayClone(ctx); guardErr != nil { // MTIX-112.17.16.43: before any write or cursor advance
		return guardErr
	}
	path := filepath.Join(dir, bootstrap.DirName, names[len(names)-1])
	opts := sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, Workflow: workflowResolution(f),
		HoldPush: holdPushForAdoption} // MTIX-95.31.16: an adopted uid moves pending events under the push lock
	res, err := bootstrap.ImportSnapshot(ctx, bootstrap.ImportRequest{Store: app.store, Path: path, Options: opts})
	if res.Report != nil {
		fmt.Fprint(cmd.ErrOrStderr(), res.Report.String())
	}
	if err != nil {
		return err
	}
	// The cursor is seeded as it always was (epoch 0, so the store advances it
	// on the first poll) with the stamped epoch beside it, atomically; the stamp
	// feeds the continuity check only (MTIX-265).
	if seedErr := bootstrap.SeedCloneCursors(ctx, app.store, res); seedErr != nil {
		return seedErr
	}
	peer, err := relayPeerID()
	if err == nil {
		// Rejoining clears any retirement, so this peer counts in the
		// prune quorum again (§6.7).
		if rejoinErr := lifecycle.RejoinPeer(dir, peer); rejoinErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "relay clone: clear retirement: %s\n", rejoinErr)
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "imported %d node(s); tailing from the stamped positions\n", res.NodesImported)
	return nil
}

// relayStampPositions is what a snapshot stamps: this store's position in every
// peer's stream and the publisher epoch it was read in, with this peer's own
// stream at its published head, which the snapshot holds (MTIX-265).
func relayStampPositions(ctx context.Context, dir, self string) (bootstrap.Stamp, error) {
	peers, _, err := relayPeerDirs(dir)
	if err != nil {
		return bootstrap.Stamp{}, err
	}
	return bootstrap.StampPositions(ctx, app.store, self, peers)
}
