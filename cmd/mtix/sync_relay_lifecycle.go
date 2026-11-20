// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/relay/keyring"
	"github.com/hyper-swe/mtix/internal/relay/lifecycle"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

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
			printRelayInitAuth(cmd, noAuth, args[0], res.KeyEpoch)
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
			// The attach already succeeded: caching the identity is best-effort.
			cacheIdentityBestEffort(cmd.ErrOrStderr(), func() error { return cacheVerifiedRelay(args[0], res.Relay.RelayID) })
			fmt.Fprintf(cmd.OutOrStdout(), "attached to relay %s\n", res.Relay.RelayID)
			fmt.Fprintf(cmd.OutOrStdout(), "shared projects: %v\n", res.SharedProjects)
			fmt.Fprintf(cmd.OutOrStdout(), "next: mtix config set sync.relay.dir %s\n", args[0])
			return nil
		},
	}
}

// relayNoSharedProjectFix is the clone-first late-joiner recovery shown with
// RELAY_NO_SHARED_PROJECT. It never suggests a second relay or attach.
const relayNoSharedProjectFix = "if this project should join this existing relay, use the clone-first join:\n" +
	"  On an existing peer: `mtix sync relay tick`, then `mtix sync relay clone --export`.\n" +
	"  In a fresh empty directory, preserving any existing project: `mtix init <PREFIX>` (same prefix),\n" +
	"  then `mtix config set sync.relay.peer_id <new-peer-id>` (NEW id; never reuse a lost store's id).\n" +
	"  Ask the user to copy every relay key epoch file to .mtix/relay/keys/ out of band, never through the relay or by printing keys (directory 0700, files 0600).\n" +
	"  For a --no-auth relay, skip keys; every peer must run `mtix config set sync.relay.require_auth false`.\n" +
	"  Then `mtix config set sync.relay.dir <dir>`; stop any daemon in the new project before cloning.\n" +
	"  Run `mtix sync relay clone`, then `mtix sync relay tick`. Do not attach before or after clone.\n" +
	"  See the sync skill's Fresh late joiner section for the full sequence."

// relayAttachHint names the recovery for an attach refusal. An error
// that does not name the next command is a bug.
func relayAttachHint(err error) error {
	switch lifecycle.CodeOf(err) {
	case lifecycle.CodeModeMismatch:
		return fmt.Errorf("%w\n  fix: set sync.relay.require_auth to match the relay, or attach a relay in your mode", err)
	case lifecycle.CodeHistoryDiverged:
		return fmt.Errorf("%w\n  fix: these are different histories under one prefix; see the divergence resolution paths", err)
	case lifecycle.CodeNoSharedProject:
		return fmt.Errorf("%w\n  fix: %s", err, relayNoSharedProjectFix)
	}
	if keyring.CodeOf(err) == keyring.CodeKeyAbsent {
		return fmt.Errorf("%w\n  fix: copy the relay key into %s (mode 0600) out of band, then attach again", err, relayKeysDir())
	}
	return err
}

func newRelayRotateKeyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rotate-key",
		Args:  syncExactArgs(0),
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

// printRelayInitAuth explains the FR-21 authentication setup for every peer.
func printRelayInitAuth(cmd *cobra.Command, noAuth bool, dir string, epoch uint16) {
	if noAuth {
		fmt.Fprintf(cmd.OutOrStdout(),
			"WARNING: this relay is UNAUTHENTICATED. Anything that can write %s can write events.\n"+
				"  Every peer must set sync.relay.require_auth=false, and doctor will keep saying so.\n", dir)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(),
			"key epoch %d written to %s (mode 0600) — copy it to every peer out of band\n",
			epoch, relayKeysDir())
	}
}
