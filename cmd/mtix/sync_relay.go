// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/hyper-swe/mtix/internal/relay/bootstrap"
	"github.com/hyper-swe/mtix/internal/relay/keyring"
	"github.com/hyper-swe/mtix/internal/relay/lifecycle"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
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
		Short: "Manage the file-based sync relay (FR-21)",
		Long: "Operate a sync transport that carries events through a shared\n" +
			"directory — a mounted folder, a network filer, removable media —\n" +
			"for peers that cannot reach a database.",
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
	if app.configSvc == nil {
		return ""
	}
	dir, err := app.configSvc.Get("sync.relay.dir")
	if err != nil {
		return ""
	}
	return dir
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
	})
}

func newRelayInitCmd() *cobra.Command {
	var noAuth bool
	cmd := &cobra.Command{
		Use:   "init <dir>",
		Short: "Create a relay in a shared directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			peer, err := relayPeerID()
			if err != nil {
				return err
			}
			prefix, hash, err := app.store.GetOrComputeLocalFirstEventHash(ctx)
			if err != nil {
				return fmt.Errorf("read local project history: %w", err)
			}
			if prefix == "" {
				return fmt.Errorf("this store has no project history yet; create a node before initializing a relay")
			}
			relayID, err := clock.NewEventID()
			if err != nil {
				return fmt.Errorf("mint relay id: %w", err)
			}
			res, err := lifecycle.Init(lifecycle.InitRequest{
				RelayDir:      args[0],
				KeysDir:       relayKeysDir(),
				RelayID:       relayID,
				CreatedAt:     time.Now().UTC(),
				CreatedBy:     peer,
				Projects:      []metadata.Project{{Prefix: prefix, FirstEventHash: hash}},
				Authenticated: !noAuth,
				Rand:          rand.Reader,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "relay initialized at %s for project %s\n", args[0], prefix)
			if noAuth {
				fmt.Fprintf(cmd.OutOrStdout(),
					"WARNING: this relay is UNAUTHENTICATED. Anything that can write %s can write events.\n"+
						"  Every peer must attach with sync.relay.require_auth=false, and doctor will keep saying so.\n", args[0])
			} else {
				fmt.Fprintf(cmd.OutOrStdout(),
					"key epoch %d written to %s (mode 0600) — copy it to every peer out of band\n",
					res.KeyEpoch, relayKeysDir())
			}
			fmt.Fprintf(cmd.OutOrStdout(), "next: mtix config set sync.relay.dir %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&noAuth, "no-auth", false,
		"Create an unauthenticated relay (§8.3 opt-out; records carry no MAC)")
	return cmd
}

func newRelayAttachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach <dir>",
		Short: "Join an existing relay",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			prefix, hash, err := app.store.GetOrComputeLocalFirstEventHash(ctx)
			if err != nil {
				return fmt.Errorf("read local project history: %w", err)
			}
			res, err := lifecycle.Attach(lifecycle.AttachRequest{
				RelayDir:      args[0],
				KeysDir:       relayKeysDir(),
				Local:         []metadata.Project{{Prefix: prefix, FirstEventHash: hash}},
				Authenticated: relayRequireAuth(),
			})
			if err != nil {
				return relayAttachHint(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "attached to relay %s\n", res.Relay.RelayID)
			fmt.Fprintf(cmd.OutOrStdout(), "shared projects: %v\n", res.SharedProjects)
			fmt.Fprintf(cmd.OutOrStdout(), "next: mtix config set sync.relay.dir %s\n", args[0])
			return nil
		},
	}
}

// relayAttachHint names the recovery for an attach refusal. An error
// that does not name the next command is a bug.
func relayAttachHint(err error) error {
	switch lifecycle.CodeOf(err) {
	case lifecycle.CodeModeMismatch:
		return fmt.Errorf("%w\n  fix: set sync.relay.require_auth to match the relay, or attach a relay in your mode", err)
	case lifecycle.CodeHistoryDiverged:
		return fmt.Errorf("%w\n  fix: these are different histories under one prefix; see the divergence resolution paths", err)
	case lifecycle.CodeNoSharedProject:
		return fmt.Errorf("%w\n  fix: this relay carries other projects; init your own relay instead", err)
	}
	if keyring.CodeOf(err) == keyring.CodeKeyAbsent {
		return fmt.Errorf("%w\n  fix: copy the relay key into %s (mode 0600) out of band, then attach again", err, relayKeysDir())
	}
	return err
}

func newRelayTickCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tick",
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

// relayStallRecovery names the command that clears each stall.
func relayStallRecovery(code string) string {
	switch code {
	case "RELAY_SEGMENT_CORRUPT":
		return "ask that peer to run `mtix sync relay republish --from <rs>`, or re-bootstrap with `mtix sync relay clone`"
	case "RELAY_GAP":
		return "ask that peer to run `mtix sync relay republish --from <rs>`, or re-bootstrap with `mtix sync relay clone`"
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

func newRelayRotateKeyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rotate-key",
		Short: "Install the next key epoch and record its boundary",
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := requireRelay()
			if err != nil {
				return err
			}
			peer, err := relayPeerID()
			if err != nil {
				return err
			}
			pos, err := app.store.RelayPushCursor(cmd.Context())
			if err != nil {
				return err
			}
			res, err := lifecycle.RotateKey(lifecycle.RotateRequest{
				RelayDir:     dir,
				KeysDir:      relayKeysDir(),
				FromRSByPeer: map[string]uint64{peer: pos.NextRS},
				Rand:         rand.Reader,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"key epoch %d installed; this peer publishes under it from rs %d\n",
				res.KeyEpoch, pos.NextRS)
			fmt.Fprintf(cmd.OutOrStdout(),
				"copy epoch %d to every peer out of band. Keep the OLD epoch installed until\n"+
					"pruning has passed the boundary — a reader that loses it reads valid history as forged.\n",
				res.KeyEpoch)
			return nil
		},
	}
}

func newRelayResetPeerCmd() *cobra.Command {
	var baseRS uint64
	var floor int64
	cmd := &cobra.Command{
		Use:   "reset-peer",
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

func newRelayRetirePeerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "retire-peer <peer-id>",
		Short: "Drop a vanished peer from the prune quorum",
		Long: "A peer that never acks holds every writer's retention window open\n" +
			"forever. Retiring it releases the window. Check `mtix sync doctor`\n" +
			"first: a peer silent for a fortnight may be a laptop on holiday, and\n" +
			"retiring one that returns prunes history it still needs.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := requireRelay()
			if err != nil {
				return err
			}
			if err := lifecycle.RetirePeer(dir, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"%s retired; it no longer holds the prune window.\n"+
					"If it returns, `mtix sync relay clone` re-admits it.\n", args[0])
			return nil
		},
	}
}

func newRelayCloneCmd() *cobra.Command {
	var export bool
	cmd := &cobra.Command{
		Use:   "clone",
		Short: "Export or import a bootstrap snapshot",
		Long: "A peer joining after pruning cannot tail from the start. Any current\n" +
			"peer exports a snapshot into the relay; the joiner imports it and\n" +
			"begins tailing from the stamped positions.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := requireRelay()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if export {
				return runRelayCloneExport(ctx, cmd, dir)
			}
			return runRelayCloneImport(ctx, cmd, dir)
		},
	}
	cmd.Flags().BoolVar(&export, "export", false, "Produce a snapshot instead of consuming one")
	return cmd
}

func newRelayRepublishCmd() *cobra.Command {
	var fromRS uint64
	cmd := &cobra.Command{
		Use:   "republish",
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
	positions, err := relayPeerPositions(ctx, dir)
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
		Positions:  positions,
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

// runRelayCloneImport consumes the newest snapshot in the relay.
func runRelayCloneImport(ctx context.Context, cmd *cobra.Command, dir string) error {
	names, err := bootstrap.SnapshotNames(dir)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("no bootstrap snapshot in %s; ask a current peer to run `mtix sync relay clone --export`",
			filepath.Join(dir, bootstrap.DirName))
	}
	path := filepath.Join(dir, bootstrap.DirName, names[len(names)-1])
	res, err := bootstrap.ImportSnapshot(ctx, bootstrap.ImportRequest{Store: app.store, Path: path})
	if err != nil {
		return err
	}
	for peer, rs := range res.Positions {
		if advErr := app.store.AdvanceRelayIngestCursor(ctx, peer, sqlite.RelayIngestPosition{RS: rs}); advErr != nil {
			return advErr
		}
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

// relayPeerPositions reads this store's view of every peer's position.
func relayPeerPositions(ctx context.Context, dir string) (map[string]uint64, error) {
	peers, _, err := relayPeerDirs(dir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]uint64, len(peers))
	for _, peer := range peers {
		pos, err := app.store.RelayIngestCursor(ctx, peer)
		if err != nil {
			return nil, err
		}
		out[peer] = pos.RS
	}
	return out, nil
}
