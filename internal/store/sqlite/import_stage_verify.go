// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"unicode/utf8"

	"github.com/hyper-swe/mtix/internal/model"
)

// StagedChecksums are the digests a staged export hashes to: the same values
// computeExportChecksumH, legacyReplacementChecksumH and computeIDsChecksum
// give for the fully decoded export (MTIX-2.3.2.1, FR-7.8, FR-7.8a).
type StagedChecksums struct {
	Checksum string
	// Legacy is the pre-0.5.4 digest of text holding U+FFFD, "" when none.
	Legacy string
	// IDs is the ids_checksum digest, "" when no value is invalid UTF-8.
	IDs string
}

// StagedRecompute is what RecomputeExportChecksum stores for the same export
// decoded whole: node_count, checksum and ids_checksum over the canonical
// (sorted) order.
type StagedRecompute struct {
	NodeCount int
	Checksum  string
	IDs       string
}

// Staged row reads, in the two orders the checksum is taken in. Both read one
// kind's rows; the canonical one follows RecomputeExportChecksum's sorts.
const (
	// Read one kind's staged rows in file order (the order the file spelled
	// them), as VerifyExportChecksum hashes a decoded file.
	stagedRowsInFileOrderSQL = `SELECT body FROM stage_rows WHERE kind = ? ORDER BY ord`
	// Read one kind's staged rows in canonical order: nodes by id, dependencies
	// by from then to, history by task uid, time then id (sortHistory), all
	// bytewise (BINARY collation), ties in file order.
	stagedRowsInCanonicalOrderSQL = `SELECT body FROM stage_rows WHERE kind = ? ORDER BY k1, k2, k3, ord`
)

// checksumStream hashes the checksum document {"nodes":..,"deps":..,
// "history":..} piece by piece: canonical is the reader-view digest, legacy
// the escaped-U+FFFD spelling.
type checksumStream struct {
	canonical, legacy hash.Hash
	sawReplacement    bool
}

func (c *checksumStream) write(s string) {
	_, _ = c.canonical.Write([]byte(s))
	_, _ = c.legacy.Write([]byte(s))
}

// writeElement hashes one array element as computeExportChecksumH does: the
// canonical form is the reader view encoded, decoded and encoded again.
func (c *checksumStream) writeElement(first []byte, decode func([]byte) ([]byte, error)) error {
	second, err := decode(first)
	if err != nil {
		return err
	}
	_, _ = c.canonical.Write(second)
	replacement := []byte(string(utf8.RuneError))
	if bytes.Contains(first, replacement) {
		c.sawReplacement = true
		first = bytes.ReplaceAll(first, replacement, []byte("\\ufffd"))
	}
	_, _ = c.legacy.Write(first)
	return nil
}

// Checksums computes the digests of the staged export, reading the staged
// rows back in file order, one at a time (FR-7.8).
func (st *StagedExport) Checksums(ctx context.Context) (StagedChecksums, error) {
	return st.checksums(ctx, stagedRowsInFileOrderSQL)
}

// Recompute computes what RecomputeExportChecksum stores for this export
// (FR-7.8, MTIX-26.5): the rows read back in canonical sorted order, so the
// recovery and rewrite paths can use the staged rows instead of a decoded
// board. Elements whose sort keys are equal keep file order, where
// RecomputeExportChecksum's unstable sort leaves them unspecified.
func (st *StagedExport) Recompute(ctx context.Context) (StagedRecompute, error) {
	got, err := st.checksums(ctx, stagedRowsInCanonicalOrderSQL)
	if err != nil {
		return StagedRecompute{}, fmt.Errorf("recompute checksum: %w", err)
	}
	return StagedRecompute{NodeCount: st.NodeCount(), Checksum: got.Checksum, IDs: got.IDs}, nil
}

// checksums hashes the staged rows read with query (one of the two constants
// above).
func (st *StagedExport) checksums(ctx context.Context, query string) (StagedChecksums, error) {
	cs := &checksumStream{canonical: sha256.New(), legacy: sha256.New()}
	cs.write(`{"nodes":`)
	if err := st.hashArray(ctx, cs, stageNodes, query, nodeFirstEncoding, reencode[exportNode]); err != nil {
		return StagedChecksums{}, err
	}
	cs.write(`,"deps":`)
	if err := st.hashArray(ctx, cs, stageDeps, query, depFirstEncoding, reencode[exportDep]); err != nil {
		return StagedChecksums{}, err
	}
	if st.counts[stageHistory] > 0 {
		cs.write(`,"history":`)
		if err := st.hashArray(ctx, cs, stageHistory, query, historyFirstEncoding, reencode[exportHistoryEntry]); err != nil {
			return StagedChecksums{}, err
		}
	}
	cs.write(`}`)
	out := StagedChecksums{Checksum: fmt.Sprintf("%x", cs.canonical.Sum(nil))}
	if cs.sawReplacement {
		out.Legacy = fmt.Sprintf("%x", cs.legacy.Sum(nil))
	}
	ids, err := st.idsChecksum(ctx)
	if err != nil {
		return StagedChecksums{}, err
	}
	out.IDs = ids
	return out, nil
}

// hashArray streams one staged array into cs as a JSON array (null when the
// file had none), reading its rows with query.
func (st *StagedExport) hashArray(ctx context.Context, cs *checksumStream, kind stageKind, query string,
	first func([]byte) ([]byte, error), again func([]byte) ([]byte, error)) error {
	if st.presence[kind] == arrayAbsent {
		cs.write("null")
		return nil
	}
	cs.write("[")
	rows, err := st.db.QueryContext(ctx, query, int(kind))
	if err != nil {
		return fmt.Errorf("read staged %s rows: %w", kind, err)
	}
	defer func() { _ = rows.Close() }()
	for i := 0; rows.Next(); i++ {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return fmt.Errorf("scan staged %s row %d: %w", kind, i, err)
		}
		if i > 0 {
			cs.write(",")
		}
		enc, err := first(body)
		if err == nil {
			err = cs.writeElement(enc, again)
		}
		if err != nil {
			return fmt.Errorf("hash staged %s row %d: %w", kind, i, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read staged %s rows: %w", kind, err)
	}
	cs.write("]")
	return nil
}

func nodeFirstEncoding(body []byte) ([]byte, error) {
	var n exportNode
	if err := json.Unmarshal(body, &n); err != nil {
		return nil, fmt.Errorf("decode staged node: %w", err)
	}
	n.readerView = true
	n.History = readerViewHistory(n.History)
	return json.Marshal(n)
}

func depFirstEncoding(body []byte) ([]byte, error) {
	var d exportDep
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("decode staged dependency: %w", err)
	}
	return json.Marshal(d)
}

func historyFirstEncoding(body []byte) ([]byte, error) {
	var h exportHistoryEntry
	if err := json.Unmarshal(body, &h); err != nil {
		return nil, fmt.Errorf("decode staged history entry: %w", err)
	}
	h.readerView = true
	return json.Marshal(h)
}

// reencode decodes the first encoding and encodes it again (MTIX-107.39).
func reencode[T any](first []byte) ([]byte, error) {
	var v T
	if err := json.Unmarshal(first, &v); err != nil {
		return nil, fmt.Errorf("decode for checksum: %w", err)
	}
	second, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("re-encode for checksum: %w", err)
	}
	return second, nil
}

// idsChecksum hashes the sorted non-UTF-8 id tuples as computeIDsChecksum
// does, reading them back in order from the staging database.
func (st *StagedExport) idsChecksum(ctx context.Context) (string, error) {
	// Read every staged non-UTF-8 id tuple in the order computeIDsChecksum
	// sorts them (kind, key, hex), bytewise.
	rows, err := st.db.QueryContext(ctx,
		`SELECT kind, key, hex FROM stage_ids ORDER BY kind, key, hex`)
	if err != nil {
		return "", fmt.Errorf("read staged ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	h := sha256.New()
	n := 0
	for rows.Next() {
		var t idTuple
		if err := rows.Scan(&t.Kind, &t.Key, &t.Hex); err != nil {
			return "", fmt.Errorf("scan staged id: %w", err)
		}
		enc, err := json.Marshal(t)
		if err != nil {
			return "", fmt.Errorf("encode ids for checksum: %w", err)
		}
		sep := ","
		if n == 0 {
			sep = "["
		}
		_, _ = h.Write(append([]byte(sep), enc...))
		n++
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("read staged ids: %w", err)
	}
	if n == 0 {
		return "", nil
	}
	_, _ = h.Write([]byte("]"))
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// Verify applies the checks Import makes on the decoded export, on the staged
// rows (FR-7.8, FR-7.8a): node_count matches, and the checksum and
// ids_checksum verify (ValidateExport's first two checks;
// VerifyExportChecksum's rule for the legacy digest).
func (st *StagedExport) Verify(ctx context.Context) error {
	if st.Header.NodeCount != st.NodeCount() {
		return fmt.Errorf("node count mismatch: declared %d, actual %d: %w",
			st.Header.NodeCount, st.NodeCount(), model.ErrInvalidInput)
	}
	got, err := st.Checksums(ctx)
	if err != nil {
		return fmt.Errorf("verify checksum: %w", err)
	}
	ok := got.Checksum == st.Header.Checksum ||
		(got.Legacy != "" && got.Legacy == st.Header.Checksum)
	if !ok || got.IDs != st.Header.IDsChecksum {
		return fmt.Errorf("checksum verification failed: %w", model.ErrInvalidInput)
	}
	return nil
}
