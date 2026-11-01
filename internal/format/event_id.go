// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import "strconv"

// EventID renders a sync event id for human-facing output (text, errors and
// logs) as a double-quoted, printable-ASCII string with strconv.QuoteToASCII
// semantics, so control characters, NUL, bidirectional marks and non-ASCII
// text can never reach a terminal or a log line. An ordinary UUID renders
// recognizably, inside quotes.
//
// The rendering is presentation only: strconv.Unquote of the result returns
// the exact original bytes, and the stored id is never modified (MTIX-187.4
// ruling from MTIX-187.2.6.2.1: receivers keep accepting any non-empty id).
// Implements MTIX-187.4.1.
func EventID(id string) string { return strconv.QuoteToASCII(id) }
