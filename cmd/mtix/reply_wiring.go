// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

// newReplyProcessor builds the one reply accounting both runtimes use
// (MTIX-112.20.3): the host dispatcher and the per-project daemon call
// Tick before they ask the scheduler and Scan after every hub pull, so a
// reply closes a sender window and a reply_by event addressed to a local
// agent starts a hold on either path. Never duplicate this wiring.
func newReplyProcessor(journal service.ReplyJournal, st *oplocal.State, src *hubContactSource, host service.RegistrationHost) *service.ReplyProcessor {
	return &service.ReplyProcessor{Journal: journal, State: st, Key: src.key(), Host: host, Settings: st, Clock: src.now}
}
