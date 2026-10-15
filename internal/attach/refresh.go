// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

// refreshRecordName is the operator-local record of glue awaiting refresh.
const refreshRecordName = "attach-refresh"

// RecordStore is the operator-local JSON record access attach needs;
// *oplocal.State implements it.
type RecordStore interface {
	ReadJSON(name string, v any) ([]oplocal.Report, error)
	WriteJSON(name string, v any) error
}

type refreshMarkers struct {
	Glue map[string]int `json:"glue"`
}

func readMarkers(st RecordStore) (refreshMarkers, error) {
	var m refreshMarkers
	reports, err := st.ReadJSON(refreshRecordName, &m)
	if err != nil {
		return refreshMarkers{}, fmt.Errorf("read attach refresh markers: %w", err)
	}
	if len(reports) > 0 || m.Glue == nil {
		// An ignored record (another host's, or invalid) holds no markers here.
		m.Glue = map[string]int{}
	}
	return m, nil
}

// MarkGlueForRefresh records that harness glue called with protocol seen, so
// the next non-sandboxed command or doctor --fix rewrites it (ADR-007 §6.5).
// The mark contains no authority; it only names release-defined bytes.
func MarkGlueForRefresh(st RecordStore, harness string, seen int) error {
	if !KnownHarness(harness) || seen < 0 {
		return fmt.Errorf("mark %q glue at protocol %d: %w", harness, seen, model.ErrInvalidInput)
	}
	m, err := readMarkers(st)
	if err != nil {
		return err
	}
	if prev, ok := m.Glue[harness]; ok && prev == seen {
		return nil
	}
	m.Glue[harness] = seen
	if err := st.WriteJSON(refreshRecordName, m); err != nil {
		return fmt.Errorf("mark %s glue for refresh: %w", harness, err)
	}
	return nil
}

// PendingGlueRefresh returns the harnesses whose glue awaits refresh, with
// the protocol each was last seen speaking.
func PendingGlueRefresh(st RecordStore) (map[string]int, error) {
	m, err := readMarkers(st)
	if err != nil {
		return nil, err
	}
	return m.Glue, nil
}

// ClearGlueRefresh removes the refresh mark for one harness.
func ClearGlueRefresh(st RecordStore, harness string) error {
	m, err := readMarkers(st)
	if err != nil {
		return err
	}
	if _, ok := m.Glue[harness]; !ok {
		return nil
	}
	delete(m.Glue, harness)
	if err := st.WriteJSON(refreshRecordName, m); err != nil {
		return fmt.Errorf("clear %s glue refresh mark: %w", harness, err)
	}
	return nil
}
