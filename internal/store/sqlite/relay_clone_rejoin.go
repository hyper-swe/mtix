// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"fmt"
	"strings"
)

// RelayRejoinStep is one step of the recovery a refused relay clone names
// (MTIX-112.17.16.43). The refusal text, the tests and the agent docs all
// read the same list, so the text cannot drift from the steps that are run.
type RelayRejoinStep struct {
	// InNewProject is false for a step run in the project that was refused.
	InNewProject bool
	// Text is the instruction as the refusal prints it.
	Text string
	// Command is the mtix command the step runs, with <placeholders> for the
	// values the operator chooses. It is empty for the key copy, which is a
	// file operation and never an mtix command.
	Command string
}

// RelayCloneOnSyncedQuarantineLine is the exact text a relay tick prints when
// it skipped records from authenticated peers; the recovery looks for it.
const RelayCloneOnSyncedQuarantineLine = "quarantined N record(s) from authenticated peers:"

// RelayCloneOnSyncedStop is the STOP prose of the recovery. It names no
// command that could lose data: a record the old project skipped at ingest
// (for example two peers that took the same task number) is kept in no store
// until MTIX-268 persists the relay-ingest quarantine, so no manual step can
// safely bring it back, and a recreate would duplicate the task on its author.
const RelayCloneOnSyncedStop = "STOP, do not continue: keep the old project, do not run `mtix sync relay retire-peer`, " +
	"and ask the project maintainer. Records the old project skipped are kept in no store, and the " +
	"durable fix is MTIX-268"

// RelayCloneOnSyncedLogSearch is the command that searches the supervised
// daemon's captured log for a record skipped at ingest. It exists only where
// a daemon ran the tick; an interactive tick prints to the terminal and
// leaves no trace.
const RelayCloneOnSyncedLogSearch = "grep -s 'relay ingest quarantined a record' .mtix/logs/daemon.err.log"

// RelayCloneOnSyncedListCommand lists every task of every project with only
// its uid. The limit is far above any board; step 9 also checks that the
// number of rows read equals the "total" the command reports, so a truncated
// read can never pass.
const RelayCloneOnSyncedListCommand = "mtix list --json --all-projects --limit 100000 --fields uid"

// RelayCloneOnSyncedStopHalfJoined is the STOP prose of step 9. Step 8 has
// already stopped the old daemon and the new project has published under its
// new id, so the prose says what to do with both. It deletes nothing.
const RelayCloneOnSyncedStopHalfJoined = "STOP, do not continue: restart the daemon in the old project and keep using the old project, " +
	"leave the new project unused (delete nothing), do not run `mtix sync relay retire-peer`, and ask the project maintainer, " +
	"telling them the new project's peer id; the durable fix is MTIX-268"

// RelayCloneOnSyncedSteps lists how a peer that already syncs rejoins: as a
// NEW peer in a new project. A new project that kept the old peer id would
// inherit the old publisher stream, whose events its journal lacks, so it
// would stop publishing with RELAY_PUBLISHER_DIVERGED, and ingest skips a
// peer's own id, so the old project's last published work would never
// arrive. A new id starts a fresh publisher stream and re-reads the old id's
// stream from the start (replaying it is harmless: the last writer is kept).
// Story 3 of MTIX-112.17.16.43 words the recovery as keeping the peer id;
// that cannot work, so the recovery keeps the relay key and takes a NEW id,
// and the story is judged by its intent (a working, narrowed recovery).
//
// The recovery fails closed on collisions: a record the old project skipped
// at ingest is kept in no store and the export stamps its peer's stream past
// it, so the new project would lack it. Step 1 STOPs on the quarantined line
// and step 9 STOPs when the new project lacks any task (by uid) of the old
// project; nothing is retired before step 10.
func RelayCloneOnSyncedSteps() []RelayRejoinStep {
	return []RelayRejoinStep{
		{false, "in this project run `mtix sync relay tick` so this peer publishes the work it holds, and read its output: if it has a " +
			"`" + RelayCloneOnSyncedQuarantineLine + "` line, " + RelayCloneOnSyncedStop + ". That line appears only on the tick that skipped " +
			"the record and is persisted nowhere (MTIX-268). Also run `" + RelayCloneOnSyncedLogSearch + "` here: if it prints anything, " +
			"STOP the same way. A skip made by an earlier interactive tick leaves no trace and cannot be detected until MTIX-268 lands; " +
			"step 9 does not catch it", "mtix sync relay tick"},
		{false, "in this project run `mtix sync relay clone --export` so the relay holds a snapshot that includes that work", "mtix sync relay clone --export"},
		{true, "in a new project run `mtix init <PREFIX>` with the same prefix as this project", "mtix init <PREFIX>"},
		{true, "set a NEW peer id there with `mtix config set sync.relay.peer_id <new-peer-id>` and never reuse this " +
			"project's id: a reused id inherits the old published stream, publishing stops with RELAY_PUBLISHER_DIVERGED " +
			"and this peer's own last work never arrives", "mtix config set sync.relay.peer_id <new-peer-id>"},
		{true, "copy the relay key epoch files from .mtix/relay/keys here into .mtix/relay/keys there, out of band and never " +
			"through the relay (directory mode 0700, files mode 0600; never print them)", ""},
		{true, "run `mtix config set sync.relay.dir <dir>` there", "mtix config set sync.relay.dir <dir>"},
		{true, "stop any daemon there, then run `mtix sync relay clone` there before any `mtix sync relay attach`, which needs local history and " +
			"refuses an empty project", "mtix sync relay clone"},
		{true, "stop the daemon in the old project too, so it stops publishing under the retired id, then run `mtix sync relay tick` " +
			"there; it publishes under the new id and re-reads the old id's stream from the start (replaying it is harmless)", "mtix sync relay tick"},
		{true, "verify there with `" + RelayCloneOnSyncedListCommand + "`, and run the same command in the old project: in each project " +
			"the number of `uid` entries read must equal its `total` (otherwise the read was truncated: " + RelayCloneOnSyncedStopHalfJoined + "), " +
			"and compare by the `uid` field, never by id or title: if the new project lacks any uid the old project holds, " +
			RelayCloneOnSyncedStopHalfJoined, RelayCloneOnSyncedListCommand},
		{true, "only when the checks of steps 1 and 9 passed, run `mtix sync relay retire-peer <old-peer-id>` there so the retired id no longer holds the " +
			"prune window (mtix config get sync.relay.peer_id here shows it; unset means the machine id), then stop using the old project",
			"mtix sync relay retire-peer <old-peer-id>"},
	}
}

// RelayCloneOnSyncedWays renders RelayCloneOnSyncedSteps as the numbered
// recovery text the refusal prints.
func RelayCloneOnSyncedWays() string {
	var b strings.Builder
	b.WriteString("To rejoin as a new peer (the old project is retired only by the last step, and only if every check passes): ")
	for i, step := range RelayCloneOnSyncedSteps() {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "(%d) %s", i+1, step.Text)
	}
	return b.String()
}
