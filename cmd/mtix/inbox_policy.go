// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/hyper-swe/mtix/internal/channel"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

// inboxHeldNotice is the only text a held count adds: on stderr, never in the
// payload a script or a model reads from stdout (ADR-007 A12).
const inboxHeldNotice = "%d held: ask your operator to run `mtix doctor` and follow its inbox.held fix\n"

// errIncludeHeldRefused is returned when held bodies are requested from inside
// an agent harness session (ADR-007 A12).
var errIncludeHeldRefused = errors.New("inbox: --include-held is refused inside a harness session; ask your operator to run it in a terminal")

// newPolicyInboxService builds the inbox service every agent-facing read goes
// through, with the board's wake policy applied (ADR-007 A12). On a board
// whose permission state cannot be read, a hub-connected board fails closed
// (everything held) and a board never connected applies no policy.
func newPolicyInboxService() *service.InboxService {
	svc := service.NewInboxService(app.store)
	perms, board, deps, err := configuredPermissionState()
	if err != nil {
		return svc.WithInboxPolicy(unreadablePolicy())
	}
	return svc.WithInboxPolicy(service.NewBoardInboxPolicy(service.BoardInboxPolicyDeps{
		Perms: perms, Store: app.store, Board: board, State: deps.State, HostID: deps.HostID, UID: deps.UID, Clock: time.Now,
	}))
}

func unreadablePolicy() service.InboxPolicy {
	if app.store == nil {
		return service.InboxPolicy{}
	}
	status, err := service.NewHubMarkService(app.store).Inspect(context.Background(), lenientHubEvidence())
	if err == nil && !status.Connected {
		return service.InboxPolicy{}
	}
	return service.ConnectedInboxPolicy("operator state unreadable")
}

// writeInboxHeldNotice prints the held notice when something was held.
func writeInboxHeldNotice(w io.Writer, held service.InboxHeld) {
	if held.Count > 0 {
		fmt.Fprintf(w, inboxHeldNotice, held.Count)
	}
}

// checkIncludeHeld refuses --include-held under a harness session marker.
func checkIncludeHeld(includeHeld bool, env []string) error {
	if includeHeld && len(oplocal.HarnessSessionMarkers(env)) > 0 {
		return errIncludeHeldRefused
	}
	return nil
}

// newChannelInboxSource is the channel push source over the policy-applying
// inbox, so a held event is never pushed into a session (ADR-007 A12).
func newChannelInboxSource(agent string) *channel.InboxSource {
	return channel.NewInboxSource(service.NewPolicyInboxReader(newPolicyInboxService()), agent)
}
