// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment

import (
	"errors"
	"fmt"
)

// ErrAheadMismatch is what a WriterConfig.AdoptAhead hook returns when the
// records it was shown are not what its caller would have published next.
// Only this verdict is reported as RELAY_PEER_ID_CONFLICT; any other hook
// error is a failure to verify, not evidence of a second writer, and is
// returned with its own cause.
var ErrAheadMismatch = errors.New("adopt-ahead mismatch")

// MaxAheadRecords bounds how far past its durable cursor a medium may
// run before the writer stops asking its caller to vouch for it. One
// publish batch is the most a §6.2 crash can leave unacknowledged, so a
// larger gap is not that crash and is refused unread.
const MaxAheadRecords = 4096

// adoptOrRefuse decides a same-epoch disagreement between the caller's
// durable cursor and the medium (FR-21 §5.2 and §6.2).
//
// Refusal stays the default. The one disagreement that is expected — the
// medium holding records the cursor has not yet acknowledged, because
// the process died between the append and the cursor advance — is
// accepted only when the caller reads those exact records and confirms
// they are what it would have published next. Counts alone never
// suffice: records that merely have the right number are how a second
// writer under the same peer id would pass the check.
func (w *Writer) adoptOrRefuse(cfg WriterConfig, resumeRS uint64) error {
	conflict := fmt.Errorf("%w: cursor expects rs %d, the medium continues at rs %d",
		ErrPeerIDConflict, cfg.NextRS, resumeRS)
	if cfg.AdoptAhead == nil || resumeRS < cfg.NextRS || resumeRS-cfg.NextRS > MaxAheadRecords {
		return conflict
	}
	ahead, err := w.recordsFrom(cfg, cfg.NextRS)
	if err != nil {
		// A read or scan failure is an I/O or integrity fault, not a
		// disagreement between two writers.
		return fmt.Errorf("relay writer: read the records past the cursor: %w", err)
	}
	if uint64(len(ahead)) != resumeRS-cfg.NextRS {
		return fmt.Errorf("%w: the records past the cursor are not contiguous", conflict)
	}
	for i, rec := range ahead {
		if rec.RS != cfg.NextRS+uint64(i) {
			return fmt.Errorf("%w: the records past the cursor are not contiguous", conflict)
		}
	}
	if err := cfg.AdoptAhead(ahead); err != nil {
		if errors.Is(err, ErrAheadMismatch) {
			return fmt.Errorf("%w: the extra records are not this peer's next events: %w", conflict, err)
		}
		return fmt.Errorf("relay writer: verify the records past the cursor: %w", err)
	}
	return nil
}

// recordsFrom returns this peer's whole records from relay sequence rs
// onward, oldest first, reading back through its segments only as far
// as needed. Every segment but the newest is read as sealed with a torn
// tail tolerated, matching how recovery reads its own history.
func (w *Writer) recordsFrom(cfg WriterConfig, rs uint64) ([]Record, error) {
	segs, _, err := ListSegments(w.dir)
	if err != nil {
		return nil, err
	}
	var out []Record
	for i := len(segs) - 1; i >= 0; i-- {
		last := i == len(segs)-1
		res, err := ScanFile(segs[i].Path, ScanOptions{
			Sealed: !last, TolerateTornTail: !last, Key: cfg.Key, ExpectPeerID: cfg.PeerID,
		})
		if err != nil {
			return nil, fmt.Errorf("segment %s: %w", segs[i].Name, err)
		}
		if res.Header.PubEpoch != cfg.PubEpoch {
			break
		}
		var keep []Record
		for _, rec := range res.Records {
			if rec.RS >= rs {
				keep = append(keep, rec)
			}
		}
		out = append(keep, out...)
		if res.Header.FirstRS <= rs {
			break
		}
	}
	return out, nil
}
