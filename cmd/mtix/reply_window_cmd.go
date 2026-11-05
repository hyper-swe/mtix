// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/mcp"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// seqSource reads the local journal's high-water mark and the agents
// registered on this host.
type seqSource interface {
	LatestSyncSeq(ctx context.Context) (int64, error)
	ListRegistrations(ctx context.Context, hostID, uid string) ([]sqlite.Registration, error)
}

// errAddresseeLocal marks a send whose addressee is registered on this host:
// the reply arrives through the local journal, so no window (and no hub
// contact) is needed.
var errAddresseeLocal = errors.New("addressee is registered on this host, no hub contact needed")

// addresseeOnThisHost reports whether the addressee is registered on this
// host. An unreadable host identity or registry means unknown, which keeps
// the window open (never a silently missing window).
func addresseeOnThisHost(ctx context.Context, journal seqSource, addressee string) bool {
	host, uid, err := registrationHostIdentity()
	if err != nil {
		return false
	}
	regs, err := journal.ListRegistrations(ctx, host, uid)
	if err != nil {
		return false
	}
	for _, r := range regs {
		if r.AgentID == addressee {
			return true
		}
	}
	return false
}

// openSenderWindow records the sender's reply window after an annotation sent
// with a reply deadline committed (MTIX-112.20.3, ADR-007 X6): the host
// keeps 60 s pulls until the addressee answers or the deadline. The window
// opens after the send, so only a later event can answer it. The record is
// operator-local; it never syncs and never touches the board.
func openSenderWindow(ctx context.Context, mtixDir string, journal seqSource, now func() time.Time, author, node string, res service.AnnotationResult) error {
	if res.ReplyBy.IsZero() {
		return nil
	}
	if addresseeOnThisHost(ctx, journal, res.Addressee) {
		return errAddresseeLocal
	}
	src, err := newHubContactSource(mtixDir)
	if err != nil {
		return err
	}
	seq, err := journal.LatestSyncSeq(ctx)
	if err != nil {
		return fmt.Errorf("reply window: %w", err)
	}
	w := oplocal.ReplyWindow{AwaitingFrom: res.Addressee, Sender: author, Node: node, OpenedSeq: seq, OpenedAt: now().UTC(), ReplyBy: res.ReplyBy}
	if err := service.OpenReplyWindow(ctx, src.state, src.key(), w); err != nil {
		return fmt.Errorf("reply window: %w", err)
	}
	return nil
}

// mcpReplyWindowHook adapts openSenderWindow to the MCP annotate tool.
func mcpReplyWindowHook() mcp.ReplyWindowHook {
	return func(ctx context.Context, node, author string, res service.AnnotationResult) string {
		if app.store == nil {
			return "no board store"
		}
		if err := openSenderWindow(ctx, app.mtixDir, app.store, utcNow, author, node, res); err != nil {
			if errors.Is(err, errAddresseeLocal) {
				return replyWindowLine(res, err)
			}
			return err.Error()
		}
		return ""
	}
}

// replyWindowLine is the one status line a send prints for its window.
func replyWindowLine(res service.AnnotationResult, err error) string {
	if errors.Is(err, errAddresseeLocal) {
		return "not needed (" + res.Addressee + " is registered on this host: no hub contact needed)"
	}
	if err != nil {
		return "not recorded (" + err.Error() + "): the hub is contacted on the normal schedule"
	}
	return "open until " + res.ReplyBy.Format(time.RFC3339) + " (60 s hub pulls until " + res.Addressee + " replies)"
}
