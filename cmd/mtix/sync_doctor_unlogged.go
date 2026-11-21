// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// unloggedCheckName is the name of the doctor's check of the tasks whose
// edits push holds because the sync log has no creation of them
// (MTIX-112.17.16.37).
const unloggedCheckName = "tasks peers do not hold"

// unloggedTask is one task named by the check: its subject and how many
// held events depend on it.
type unloggedTask struct {
	subject sqlite.UnloggedSubject
	events  int
}

// checkUnloggedHolds is the doctor's "tasks peers do not hold" check
// (MTIX-112.17.16.37, founder decision DESK-1.371). It passes when no push
// hold depends on a task the sync log has no creation for, or when every
// such hold is retired (the task and what depends on it are deleted here,
// or gone from the board: peers never held it and nothing is left to send;
// the held events stay as a record and are never sent; after a pull brings
// the creation of the same task, a delete made here before it, and the
// task's other held events while it stays deleted, stay retired). Otherwise
// it fails: it names each task
// with how many of its events are held and gives, per task, the exact
// commands that re-create it as a new task, which the log then creates and
// push sends; and it counts the held events whose task the log now creates
// (a pull brought the creation), which the next push sends (review r1 F1).
// Local only.
func checkUnloggedHolds(ctx context.Context, st *sqlite.Store) DoctorCheck {
	ids, err := st.HeldPushEventIDsWithReason(ctx, holdUnloggedPrefix)
	if err != nil {
		return DoctorCheck{Name: unloggedCheckName, Detail: err.Error()}
	}
	if len(ids) == 0 {
		return DoctorCheck{Name: unloggedCheckName, Pass: true, Detail: "ok"}
	}
	states, err := readUnloggedHoldStates(ctx, st, ids)
	if err != nil {
		return DoctorCheck{Name: unloggedCheckName, Detail: err.Error()}
	}
	g := groupUnlogged(ids, states)
	if len(g.tasks) == 0 && g.released == 0 {
		return DoctorCheck{Name: unloggedCheckName, Pass: true, Detail: fmt.Sprintf(
			"ok; %d held events of deleted tasks no peer holds stay held as a record: a delete made here "+
				"before the sync log held the task's creation is never sent, and neither is any change of a "+
				"task mtix gc purged before then; the other changes of such a task are not sent while it stays "+
				"deleted here, and are sent if it is undeleted; a teammate's copy of the task is not deleted by them",
			g.retired)}
	}
	detail, fix := unloggedReport(g)
	return DoctorCheck{Name: unloggedCheckName, Detail: detail, Fix: fix}
}

// unloggedReport is the detail and the fix of a failing check: each
// unlogged task (up to heldPushListed) with its held event count and its
// re-create fix, then the held events the next push sends.
func unloggedReport(g unloggedGroups) (detail, fix string) {
	names := make([]string, 0, len(g.tasks))
	fixes := make([]string, 0, len(g.tasks)+1)
	for i, t := range g.tasks {
		if i == heldPushListed {
			names = append(names, fmt.Sprintf("and %d more", len(g.tasks)-i))
			break
		}
		names = append(names, fmt.Sprintf("%s (%d held events)", oneLine(t.subject.NodeID), t.events))
		fixes = append(fixes, unloggedFix(t.subject))
	}
	var parts []string
	if len(g.tasks) > 0 {
		parts = append(parts, fmt.Sprintf("push holds the edits of %d tasks the sync log has no creation for, "+
			"so no peer holds them and they would be quarantined there: %s", len(g.tasks), strings.Join(names, ", ")))
	}
	if g.released > 0 {
		parts = append(parts, fmt.Sprintf("%d held events whose task the sync log now has the creation of "+
			"are not pushed yet; the next mtix sync push sends them", g.released))
		fixes = append(fixes, "run mtix sync push to send the held events whose task the sync log now creates")
	}
	return strings.Join(parts, "; "), strings.Join(fixes, "; ")
}

// unloggedGroups is the held events of unlogged tasks, grouped: per
// unlogged task, in the order the tasks first appear; how many are retired
// (the task is deleted here, or gone from the board); and how many the next
// push releases (the task now has a logged creation).
type unloggedGroups struct {
	tasks             []unloggedTask
	retired, released int
}

// unloggedKey identifies one unlogged task among the held events.
type unloggedKey struct{ uid, nodeID string }

// groupUnlogged groups the held events ids by what states says of each. An
// event that depends on no unlogged task any more is retired only when its
// task is gone from the board; otherwise the next push releases it.
func groupUnlogged(ids []string, states unloggedHoldStates) unloggedGroups {
	var g unloggedGroups
	index := map[unloggedKey]int{}
	for _, id := range ids {
		s, ok := states.subjects[id]
		switch {
		case !ok && states.gone[id], ok && s.Retired:
			g.retired++
			continue
		case !ok:
			g.released++
			continue
		}
		key := unloggedKey{uid: s.UID, nodeID: s.NodeID}
		i, seen := index[key]
		if !seen {
			i = len(g.tasks)
			index[key] = i
			g.tasks = append(g.tasks, unloggedTask{subject: s})
		}
		g.tasks[i].events++
	}
	return g
}

// unloggedFix is the fix for one unlogged task: read it, create it again as
// a new task (under the same parent) with its fields, its children and its
// comments, then delete it with its subtree. The held events stay as a
// record; once the task is deleted the check passes.
func unloggedFix(s sqlite.UnloggedSubject) string {
	task := oneLine(s.NodeID)
	under := ""
	if s.ParentID != "" {
		under = " --under " + oneLine(s.ParentID)
	}
	return fmt.Sprintf("for %s: run mtix show %s --json to read its fields, re-create it as a new task with "+
		"mtix create \"<its title>\"%s --description ... --prompt ... --acceptance ... (then its child tasks under "+
		"the new task the same way, and its comments with mtix comment), then run mtix delete %s --cascade; "+
		"the held events stay as a record and are never sent", task, task, under, task)
}
