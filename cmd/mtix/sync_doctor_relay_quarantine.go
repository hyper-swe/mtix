// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// relayQuarantineListed is how many open rows the row's detail names.
const relayQuarantineListed = 5

// appendRelayQuarantineCheck adds the doctor's "relay quarantined records" row
// (MTIX-268). It is local only and appears once relay ingest has ever skipped a
// record, so a board that never used a relay shows no extra row. It fails while
// any skipped record is unresolved and prints the exact resolve command for the
// first one; --json carries the same text in detail and fix.
func appendRelayQuarantineCheck(ctx context.Context, r DoctorReport, st *sqlite.Store) DoctorReport {
	if st == nil {
		return r
	}
	rows, err := st.ListRelayQuarantine(ctx)
	if err != nil {
		return appendCheck(r, relayQuarantineCheckName, false, err.Error())
	}
	if len(rows) == 0 {
		return r
	}
	check := relayQuarantineCheck(rows)
	return appendDoctorCheck(r, check)
}

// relayQuarantineCheck builds the row from the stored relay rows.
func relayQuarantineCheck(rows []sqlite.RelayQuarantineRow) DoctorCheck {
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = service.RelayQuarantineKeyString(r.RelayQuarantineKey)
	}
	handles := service.QuarantineHandles(keys)
	var open []string
	var first *sqlite.RelayQuarantineRow
	var firstHandle string
	for i := range rows {
		if rows[i].State != sqlite.RelayQuarantineOpen {
			continue
		}
		if first == nil {
			first, firstHandle = &rows[i], handles[i]
		}
		if len(open) < relayQuarantineListed {
			open = append(open, fmt.Sprintf("%s rs %d (%s %s)", rows[i].PeerID, rows[i].RS,
				oneLine(rows[i].OpType), oneLine(rows[i].NodeID)))
		}
	}
	if first == nil {
		return DoctorCheck{Name: relayQuarantineCheckName, Pass: true,
			Detail: fmt.Sprintf("%d skipped relay record(s) resolved, 0 open", len(rows))}
	}
	return DoctorCheck{
		Name: relayQuarantineCheckName, Pass: false,
		Detail: fmt.Sprintf("%d relay record(s) from authenticated peers were skipped at ingest and are not applied: %s. "+
			"Read them with: %s --json (the relay object holds the payload)", countOpenRelay(rows),
			strings.Join(open, "; "), quarantineInspectCmd),
		Fix: relayResolveFix(first, firstHandle),
	}
}

func countOpenRelay(rows []sqlite.RelayQuarantineRow) int {
	n := 0
	for _, r := range rows {
		if r.State == sqlite.RelayQuarantineOpen {
			n++
		}
	}
	return n
}

// relayResolveFix is the exact command for one open row. Neither verb is the
// default: --accept keeps the record unapplied (the author peer still holds the
// task) and --recreate, for a skipped create_node only, adds a copy that syncs to
// every peer including the author (MTIX-268).
func relayResolveFix(r *sqlite.RelayQuarantineRow, handle string) string {
	accept := fmt.Sprintf(`mtix sync quarantine resolve --handle %s --accept --reason "<why>"`, handle)
	if r.OpType != string(model.OpCreateNode) {
		return accept + " (keeps it unapplied, stored only in this project); repeat for each open row"
	}
	return fmt.Sprintf(`either %s (keeps it unapplied, stored only in this project; use when the author peer still holds the task) `+
		`or mtix sync quarantine resolve --handle %s --recreate --reason "<why>" (adds a copy of the task here that syncs to every peer, `+
		`so the author peer then holds both and must cancel one with mtix cancel <id> --reason "duplicate of <other-id>"); `+
		`repeat for each open row`, accept, handle)
}
