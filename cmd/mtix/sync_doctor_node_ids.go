// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Doctor reports of task ids outside the node id grammar (MTIX-187). A pull
// quarantines an event that names such an id, and an import refuses a file
// that brings one in, but a store can already hold such a task from a sync
// before that check. Such a task stays readable and editable here, so the
// doctor warns instead of failing.

// nodeIDCheckName is the name of the doctor's node id grammar check.
const nodeIDCheckName = "valid node ids"

// maxListedInvalidIDs caps how many tasks the check's detail names.
const maxListedInvalidIDs = 5

// invalidNodeIDFix is the doctor's next step for local tasks whose id is
// outside the node id grammar.
const invalidNodeIDFix = "These tasks came in by sync before mtix checked pulled ids. They stay readable and editable here;" +
	" open one by its uid ('mtix show <uid>'). Other replicas quarantine every change to them, so recreate each task" +
	" you still need with 'mtix create' and stop using the old one. No mtix command renames a task in this version"

// invalidIDQuarantineAction is the doctor's next step for a pulled event
// quarantined for a task id outside the node id grammar.
const invalidIDQuarantineAction = ". For a reason with 'invalid node id': the event names a task id outside the node id grammar, so no" +
	" retry applies it. Report the event id, its node and its reason to whoever runs the hub; see the admin skill," +
	" 'Quarantined Pulled Events: invalid node id'"

// appendNodeIDCheck adds the doctor's "valid node ids" check (MTIX-187): it
// passes when every local task id is inside the node id grammar, and warns,
// naming the first tasks with their uids and the fix, when any is not.
// Local only.
func appendNodeIDCheck(ctx context.Context, r DoctorReport, st *sqlite.Store) DoctorReport {
	if st == nil {
		return appendCheck(r, nodeIDCheckName, false, "local store not initialized")
	}
	invalid, err := st.InvalidNodeIDs(ctx)
	if err != nil {
		return appendCheck(r, nodeIDCheckName, false, err.Error())
	}
	if len(invalid) == 0 {
		return appendCheck(r, nodeIDCheckName, true, "ok")
	}
	return appendDoctorCheck(r, DoctorCheck{
		Name: nodeIDCheckName, Pass: true, Warn: true,
		Detail: invalidNodeIDDetail(invalid), Fix: invalidNodeIDFix,
	})
}

// invalidNodeIDDetail names how many local tasks have an id outside the
// grammar and the first maxListedInvalidIDs of them, each with its uid.
func invalidNodeIDDetail(invalid []sqlite.InvalidNodeID) string {
	named := make([]string, 0, maxListedInvalidIDs)
	for i, n := range invalid {
		if i == maxListedInvalidIDs {
			break
		}
		uid := emptyDash(n.UID)
		if !app.jsonOutput && n.UID != "" {
			uid = format.EventID(n.UID)
		}
		entry := fmt.Sprintf("%q (uid %s)", n.ID, uid)
		if n.Deleted {
			entry += " deleted"
		}
		named = append(named, entry)
	}
	more := ""
	if len(invalid) > maxListedInvalidIDs {
		more = fmt.Sprintf(" and %d more", len(invalid)-maxListedInvalidIDs)
	}
	return fmt.Sprintf("%d local tasks have an id outside the node id grammar: %s%s",
		len(invalid), strings.Join(named, ", "), more)
}
