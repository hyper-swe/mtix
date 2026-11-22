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

// RelayCloneOnSyncedListQuarantine is the read-only command that lists the
// records relay ingest skipped. The skip is persisted (MTIX-268), so an open
// relay row is visible on any later run, not only on the tick that skipped it.
const RelayCloneOnSyncedListQuarantine = "mtix sync quarantine list --json"

// RelayCloneOnSyncedStop is the STOP prose of step 1. A record the old project
// skipped at ingest (for example two peers that took the same task number) is
// stored in the relay quarantine; the export stamps its peer's stream past it,
// so a new project would lack it until the row is settled. Settling is a choice,
// not a repair: a recreate makes a NEW task that every peer receives, including
// the author, who then holds two copies; an accept keeps the record only in the
// old project's store.
const RelayCloneOnSyncedStop = "STOP, do not continue: keep the old project, do not run `mtix sync relay retire-peer`, " +
	"and settle every open relay record first: read each with `mtix sync quarantine list --json`, then choose per record. " +
	"`mtix sync quarantine resolve --handle <handle> --accept --reason <why>` keeps it unapplied (use it when the author peer still holds the task), " +
	"and the record then stays stored only in the old project, so keep that project. " +
	"`mtix sync quarantine resolve --handle <handle> --recreate --reason <why>` (a skipped task creation only) adds a NEW task here that syncs to every peer, " +
	"including the author, who then holds both copies: on the author peer close one with `mtix cancel <id> --reason \"duplicate of <other-id>\"`. " +
	"When none is open, start this recovery again from step 1"

// RelayCloneOnSyncedListCommand lists every task of every project with only
// its uid. The limit is far above any board; step 9 also checks that the
// number of rows read equals the "total" the command reports, so a truncated
// read can never pass.
const RelayCloneOnSyncedListCommand = "mtix list --json --all-projects --limit 100000 --fields uid"

// RelayCloneOnSyncedDaemonStopNote follows every daemon-stop mention in the
// recovery text and its doc copies. Stop ends the running instance and holds
// it stopped on every OS (MTIX-275: on macOS it unloads the launchd job, so
// KeepAlive cannot restart it), uninstall removes the service and succeeds
// when none is installed, and status fails for a stopped or missing service,
// so the order is stop, uninstall, status. A waiter's
// detached listener is listed by `mtix daemon status --listener` and ended by
// `mtix daemon stop --listener` (MTIX-276; cmd/mtix/listener_control.go).
// MTIX-271.1 deliberately requires operator quiescence of every addressed-send
// surface too: neither daemon shutdown nor peer retirement disables publishing.
const RelayCloneOnSyncedDaemonStopNote = "(for a daemon installed as an OS service, run `mtix daemon stop` first, which ends the running instance and keeps it stopped until `mtix daemon start` (on macOS it unloads the launchd job, so KeepAlive cannot restart it), then `mtix daemon uninstall`, which removes the service and succeeds when none is installed, then check that `mtix daemon status` fails, which it does for a stopped or missing service; stop a foreground `mtix daemon` or `mtix sync daemon` where it runs, with Ctrl-C or by closing its terminal; a waiter, `mtix inbox --wait`, starts a detached listener: end every waiter in that project first, then run `mtix daemon stop --listener`, which asks the listener to exit and waits until the daemon lock is free, and check that `mtix daemon status --listener` exits 10, which means the daemon lock is free and nothing runs (exit 1 is an error such as running outside a project and never means none; exit 12 means a daemon, such as a foreground `mtix daemon` or `mtix sync daemon`, still holds the lock: stop it where it runs, then check again; exit 8 means a process that could not be verified holds it); `mtix daemon status --listener` also lists the pid and the live waiters; Addressed sends publish even with every daemon stopped; retiring a peer does not disable these senders. In the project being stopped, do not run `mtix comment --to` or `mtix annotate --to`; stop the MCP server (`mtix mcp`) in its client and disable automatic restart; stop `mtix serve` where it runs (Ctrl-C in its terminal, or stop its supervisor and disable automatic restart). Pause agents, hooks and automation that can send or restart these processes. Wait for in-flight sends to finish before continuing: a send starts a push that can run for up to five minutes, so wait at least five minutes after the last send. Keep these senders stopped in the old project through retirement and never resume them there after retirement. Resume them only in the new project after the recovery checks pass, or in the old project if recovery is abandoned before retirement.)"

// RelayCloneOnSyncedDaemonRestartNote follows every daemon-restart mention.
const RelayCloneOnSyncedDaemonRestartNote = "(for a service removed with `mtix daemon uninstall`, run `mtix daemon install` again; start a foreground daemon again where it ran)"

// RelayCloneOnSyncedOldProjectSwitchNote is the addressed-send switch for the
// OLD project only (step 8, MTIX-271.1): it records the current value, turns
// the switch off, and says how an abandoned recovery restores the recorded
// value. The new project (step 7) keeps the default and never carries it.
const RelayCloneOnSyncedOldProjectSwitchNote = "in the old project only, record its current value with `mtix config get sync.push_on_addressed_send`, then run `mtix config set sync.push_on_addressed_send false`, which every addressed send rereads, so it covers the CLI, MCP and serve (a send made with it off still commits locally and is not published, so keep to the stops above); if recovery is abandoned before retirement, restore the recorded value there with `mtix config set sync.push_on_addressed_send <recorded value>`, never an assumed true"

// RelayCloneOnSyncedRestoreSwitchClause is the step 9 STOP clause that puts
// the old project's recorded switch value back.
const RelayCloneOnSyncedRestoreSwitchClause = "restore the old project's recorded `sync.push_on_addressed_send` value with `mtix config set sync.push_on_addressed_send <recorded value>`"

// RelayCloneOnSyncedStopHalfJoined is the STOP prose of step 9. Step 8 has
// already stopped the old daemon and the new project has published under its
// new id, so the prose says what to do with both. It deletes nothing.
const RelayCloneOnSyncedStopHalfJoined = "STOP, do not continue: restart the daemon in the old project with `mtix daemon install` " + RelayCloneOnSyncedDaemonRestartNote + ", " + RelayCloneOnSyncedRestoreSwitchClause + ", and keep using the old project, " +
	"leave the new project unused (delete nothing), do not run `mtix sync relay retire-peer`, and ask the project maintainer, " +
	"telling them the new project's peer id"

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
// at ingest is stored in the relay quarantine, and the export stamps its
// peer's stream past it, so the new project would lack it until it is settled.
// Step 1 STOPs on an open relay row and step 9 STOPs when the new project lacks any task (by uid) of the old
// project; nothing is retired before step 10.
func RelayCloneOnSyncedSteps() []RelayRejoinStep {
	return []RelayRejoinStep{
		{false, "in this project run `mtix sync relay tick` so this peer publishes the work it holds, then run `" +
			RelayCloneOnSyncedListQuarantine + "`: if any entry has source relay and relay.state open (the tick skipped a record, " +
			"for this tick or an earlier one), " + RelayCloneOnSyncedStop, "mtix sync relay tick"},
		{false, "in this project run `mtix sync relay clone --export` so the relay holds a snapshot that includes that work", "mtix sync relay clone --export"},
		{true, "in a new project run `mtix init <PREFIX>` with the same prefix as this project", "mtix init <PREFIX>"},
		{true, "set a NEW peer id there with `mtix config set sync.relay.peer_id <new-peer-id>` and never reuse this " +
			"project's id: a reused id inherits the old published stream, publishing stops with RELAY_PUBLISHER_DIVERGED " +
			"and this peer's own last work never arrives", "mtix config set sync.relay.peer_id <new-peer-id>"},
		{true, "copy the relay key epoch files from .mtix/relay/keys here into .mtix/relay/keys there, out of band and never " +
			"through the relay (directory mode 0700, files mode 0600; never print them)", ""},
		{true, "run `mtix config set sync.relay.dir <dir>` there", "mtix config set sync.relay.dir <dir>"},
		{true, "stop any daemon there with `mtix daemon stop`, then `mtix daemon uninstall` " + RelayCloneOnSyncedDaemonStopNote + ", then run `mtix sync relay clone` there (attach is not part of joining, before or after clone: it needs local history and " +
			"refuses an empty project with `RELAY_NO_SHARED_PROJECT`)", "mtix sync relay clone"},
		{true, "stop the daemon in the old project too with `mtix daemon stop`, then `mtix daemon uninstall` " + RelayCloneOnSyncedDaemonStopNote + ", so it stops publishing under the retired id; " + RelayCloneOnSyncedOldProjectSwitchNote + "; then run `mtix sync relay tick` in the new project; " +
			"it publishes under the new id and re-reads the old id's stream from the start (replaying it is harmless)", "mtix sync relay tick"},
		{true, "verify there with `" + RelayCloneOnSyncedListCommand + "`, and run the same command in the old project: in each project " +
			"the number of `uid` entries read must equal its `total` (otherwise the read was truncated: " + RelayCloneOnSyncedStopHalfJoined + "), " +
			"and compare by the `uid` field, never by id or title: if the new project lacks any uid the old project holds, " +
			RelayCloneOnSyncedStopHalfJoined, RelayCloneOnSyncedListCommand},
		{true, "only when the checks of steps 1 and 9 passed, run `mtix sync relay retire-peer <old-peer-id>` there so the retired id no longer holds the " +
			"prune window (`mtix config get sync.relay.peer_id` in the old project shows it; unset means the machine id: " +
			"read the `this peer` line of `mtix sync relay status` in the old project), then stop using the old project",
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
