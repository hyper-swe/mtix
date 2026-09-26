// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import "fmt"

// mirrorSource is the .mtix/tasks.json mirror as Recover reads it
// (MTIX-26.5): its path, its contents or why it is unusable, whether its
// checksum verified, and the positions of its nodes by id (MTIX-95.31.3).
type mirrorSource struct {
	path     string
	data     *ExportData // nil when the mirror is unusable
	err      error       // why data is nil
	verified bool        // the mirror's checksum verified
	byID     map[string][]int
}

// readMirrorSource reads the mirror at path for Recover (MTIX-26.5) and
// notes in res when it is unusable or its checksum does not verify; an
// unverified mirror is still used, as salvage of last resort.
func readMirrorSource(path string, res *RecoverResult) *mirrorSource {
	m := &mirrorSource{path: path}
	m.data, m.err = readMirror(path)
	if m.err != nil {
		res.Notes = append(res.Notes, fmt.Sprintf("mirror unusable: %v", m.err))
		return m
	}
	valid, err := VerifyExportChecksum(m.data)
	m.verified = err == nil && valid
	if !m.verified {
		res.Notes = append(res.Notes,
			"mirror checksum did not verify; its contents are still used as salvage of last resort")
	}
	m.byID = make(map[string][]int, len(m.data.Nodes))
	for i := range m.data.Nodes {
		m.byID[m.data.Nodes[i].ID] = append(m.byID[m.data.Nodes[i].ID], i)
	}
	return m
}

// copyOf returns the mirror's copy of the database node n when that copy is
// usable for restoring n's unreadable JSON columns (MTIX-95.31.3), or nil
// and the reason it is not. A usable copy is the mirror's only node with
// n's id, from a mirror whose schema_version carries the JSON columns
// (2.0.0 and later), whose uid equals n's when both carry one, and whose
// time values pass the checks every import applies (validateNodeTimes), so
// the recovered export stays importable.
func (m *mirrorSource) copyOf(n *exportNode) (*exportNode, string) {
	if m.data == nil {
		return nil, fmt.Sprintf("no usable mirror: %v", m.err)
	}
	if !carriesNodeColumns(m.data.SchemaVersion) {
		return nil, fmt.Sprintf("the mirror's schema_version %q does not carry this column", m.data.SchemaVersion)
	}
	positions := m.byID[n.ID]
	if len(positions) == 0 {
		return nil, "the mirror holds no copy of this node"
	}
	if len(positions) > 1 {
		return nil, fmt.Sprintf("the mirror holds %d copies of this node", len(positions))
	}
	c := &m.data.Nodes[positions[0]]
	if c.UID != "" && n.UID != "" && c.UID != n.UID {
		return nil, fmt.Sprintf("the mirror's copy under this id is a different task (uid %s, not %s)", c.UID, n.UID)
	}
	if err := validateNodeTimes(c); err != nil {
		return nil, fmt.Sprintf("the mirror's copy does not validate: %v", err)
	}
	return c, ""
}

// salvagedColumn is a JSON column salvageFromDB could not read, with the key
// it kept the node under: the id the primary-key walk yielded (MTIX-95.31.3).
// On a damaged index that key can differ from the id the row carries.
type salvagedColumn struct {
	key string
	col *unreadableColumnError
}

// restoreColumnsFromMirror handles every JSON column the database could not
// read (MTIX-95.31.3). When the mirror holds a usable copy of the node
// (mirrorSource.copyOf), the column is taken from that copy and the note
// names the mirror as its source; otherwise the node keeps the column
// empty, as before, and the note says the column was dropped and why the
// mirror was not used. Every column in cols belongs to the node nodes holds
// under its key: salvageFromDB lists only the columns of nodes it salvaged.
// When that key differs from the id the row carries (a damaged index), the
// mirror is not consulted, so no other node's copy is ever applied.
func restoreColumnsFromMirror(
	nodes map[string]exportNode, cols []salvagedColumn, mirror *mirrorSource, res *RecoverResult,
) {
	for _, sc := range cols {
		col := sc.col
		if sc.key != col.nodeID {
			res.Notes = append(res.Notes, fmt.Sprintf(
				"%v; the column is dropped, and the node is salvaged without it: "+
					"the database row read under id %s carries id %s, so the mirror is not used",
				col, sc.key, col.nodeID))
			continue
		}
		n := nodes[sc.key]
		src, why := mirror.copyOf(&n)
		if src != nil {
			if entries, ok := copyNodeColumn(&n, src, col.column); ok {
				nodes[sc.key] = n
				res.Notes = append(res.Notes, restoredColumnNote(col, mirror, entries))
				continue
			}
			why = "recover cannot restore this column"
		}
		res.Notes = append(res.Notes, fmt.Sprintf(
			"%v; the column is dropped, and the node is salvaged without it: %s", col, why))
	}
}

// restoredColumnNote is the recovery note for a column taken from the
// mirror (MTIX-95.31.3): it names the mirror file and the number of entries
// its copy held, and says so when the mirror's checksum did not verify.
func restoredColumnNote(col *unreadableColumnError, mirror *mirrorSource, entries int) string {
	note := fmt.Sprintf("%v; the column is restored from the mirror %s, whose copy of the node holds %s",
		col, mirror.path, entryCount(entries))
	if !mirror.verified {
		note += " (the mirror checksum did not verify)"
	}
	return note
}

// copyNodeColumn sets the named JSON column of dst to src's and returns the
// number of entries copied (MTIX-95.31.3). ok is false, and dst unchanged,
// for a column it does not know.
func copyNodeColumn(dst, src *exportNode, column string) (entries int, ok bool) {
	switch column {
	case columnAnnotations:
		dst.Annotations = src.Annotations
		return len(src.Annotations), true
	case columnActivity:
		dst.Activity = src.Activity
		return len(src.Activity), true
	case columnCodeRefs:
		dst.CodeRefs = src.CodeRefs
		return len(src.CodeRefs), true
	case columnCommitRefs:
		dst.CommitRefs = src.CommitRefs
		return len(src.CommitRefs), true
	}
	return 0, false
}

// unreadableColumns returns every unreadableColumnError in err, which may
// be one, joined (errors.Join, as scanExportNode returns them) or wrapped
// (MTIX-95.31.3). It returns nil for nil and for any other error.
func unreadableColumns(err error) []*unreadableColumnError {
	switch e := err.(type) {
	case *unreadableColumnError:
		return []*unreadableColumnError{e}
	case interface{ Unwrap() []error }:
		var cols []*unreadableColumnError
		for _, inner := range e.Unwrap() {
			cols = append(cols, unreadableColumns(inner)...)
		}
		return cols
	case interface{ Unwrap() error }:
		return unreadableColumns(e.Unwrap())
	}
	return nil
}

// entryCount renders n as "1 entry" or "n entries".
func entryCount(n int) string {
	if n == 1 {
		return "1 entry"
	}
	return fmt.Sprintf("%d entries", n)
}
