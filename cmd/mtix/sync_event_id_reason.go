// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"

	"github.com/hyper-swe/mtix/internal/format"
)

// heldDependencyID returns the event id a stored dependent-hold reason names
// ("depends on held create of <id> (<node>)..."), cut out the way the push
// hold rule reads it back. ok is false for any other reason. Both heldFor and
// displayHoldReason use it, so display and rule agree on where the id ends
// (MTIX-187.4.2).
func heldDependencyID(reason string) (id string, ok bool) {
	rest, found := strings.CutPrefix(reason, holdDependsPrefix)
	if !found {
		return "", false
	}
	id, _, _ = strings.Cut(rest, " ")
	return id, true
}

// quarantinedCreatePrefix starts the stored reason of a pulled event held
// behind a quarantined creation (refuseBehindQuarantinedCreate); the creation
// id follows, as received. It is the text of sqlite.ErrBehindQuarantinedCreate
// and a space, which a test pins.
const quarantinedCreatePrefix = "depends on a quarantined create "

// displayHoldReason is a stored hold or quarantine reason prepared for a
// human-facing line. The stored reason stays as it is (the push hold rule
// parses it back, and --json shows it as stored); only the printed copy
// changes. When the reason names a held creation or a quarantined creation,
// that id is shown with format.EventID. Whatever follows a held-creation id is
// folded to one line and printed as printable ASCII, so an id that holds a
// space cannot carry its remainder to the terminal. Any other reason is
// returned folded the same way (MTIX-187.4.2).
func displayHoldReason(reason string) string {
	if id, found := strings.CutPrefix(reason, quarantinedCreatePrefix); found {
		return quarantinedCreatePrefix + format.EventID(id)
	}
	id, ok := heldDependencyID(reason)
	if !ok {
		return oneLine(reason)
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(reason, holdDependsPrefix), id)
	tail := asciiOneLine(rest)
	if tail != "" {
		tail = " " + tail
	}
	return holdDependsPrefix + format.EventID(id) + tail
}

// asciiOneLine folds s to one line and escapes whatever is not printable
// ASCII (strconv.QuoteToASCII without its surrounding quotes).
func asciiOneLine(s string) string {
	q := format.EventID(oneLine(s))
	return q[1 : len(q)-1]
}
