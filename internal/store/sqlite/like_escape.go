// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import "strings"

// likeEscaper escapes the LIKE metacharacters (\, %, _) so an id prefix
// matches LITERALLY under `ESCAPE '\'`. Project prefixes may legally contain
// '_' (a LIKE single-char wildcard): the sync project_prefix grammar admits it
// even though FR-2.1a does not (see projectPrefixPattern in
// internal/model/sync_event.go). Without escaping, a pattern like
// 'DEP_ADD-1.%' would cross-match an unrelated same-length prefix such as
// 'DEPXADD-1.2' and act on another project's nodes (MTIX-33, MTIX-95.17).
// Apply this to the id PREFIX only — the trailing ".%" descendant wildcard
// stays unescaped.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// escapeLIKEPrefix returns s with LIKE metacharacters backslash-escaped.
//
// It is the one helper for every subtree query in this package: a pattern
// selecting the descendants of id is always escapeLIKEPrefix(id)+".%", bound
// as a parameter and paired with `ESCAPE '\'` in the SQL (MTIX-33 renumber,
// MTIX-95.17 cascade delete/cancel, undelete, tree, stats, list and search).
func escapeLIKEPrefix(s string) string { return likeEscaper.Replace(s) }
