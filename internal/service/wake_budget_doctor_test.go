// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

type wakeBudgetReaderFault struct {
	source             string
	rows               []model.WakeBudgetCounter
	sourceErr, rowsErr error
}

func (r wakeBudgetReaderFault) WakeBudgetSource(context.Context) (string, error) {
	return r.source, r.sourceErr
}
func (r wakeBudgetReaderFault) WakeBudgetCounters(context.Context) ([]model.WakeBudgetCounter, error) {
	return r.rows, r.rowsErr
}

func TestWakeBudgetDoctor_FailuresAreVisibleAndNonrepairable(t *testing.T) {
	cause := errors.New("accounting storage unavailable")
	for _, name := range []string{"nil store", "nil state", "source error", "counter query", "conflicting row", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			state, key, source := wakePublisherFixture(t)
			reader := wakeBudgetReaderFault{source: source, rows: []model.WakeBudgetCounter{{Recipient: key, Source: source, Total: 5}}}
			var backend WakeBudgetReader = reader
			ctx := t.Context()
			switch name {
			case "nil store":
				backend = nil
			case "nil state":
				state = nil
			case "source error":
				reader.sourceErr = cause
				backend = reader
			case "counter query":
				reader.rowsErr = cause
				backend = reader
			case "conflicting row":
				reader.rows[0].Source = "another-source"
				backend = reader
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			check := WakeBudgetDoctorCheck(backend, state)
			row := check.Run(ctx)
			require.Equal(t, DoctorFail, row.Status)
			require.Contains(t, row.Fix, "Preserve")
			require.Nil(t, check.Fixer)
		})
	}
}

func TestWakeBudgetDoctor_ReportsPendingAndRetainedRewind(t *testing.T) {
	for _, rewind := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending charged", true: "retained rewind"}[rewind], func(t *testing.T) {
			state, key, source := wakePublisherFixture(t)
			require.NoError(t, state.UpdateWakeBudget(t.Context(), key, func(m *oplocal.WakeBudgetMirror, save func() error) error {
				if rewind {
					m.Rewinds, m.LastRewindAt = 1, time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
				} else {
					r := m.Sources[source]
					r.Total, r.Pending = 6, "pending-fence"
					m.Sources[source] = r
					m.Credits[0].Count = 6
				}
				return save()
			}))
			reader := wakeBudgetReaderFault{source: source, rows: []model.WakeBudgetCounter{{Recipient: key, Source: source, Total: 5}}}
			row := WakeBudgetDoctorCheck(reader, state).Run(t.Context())
			if rewind {
				require.Equal(t, DoctorWarn, row.Status)
				require.Equal(t, 1, row.Data["recorded_rewind_recipients"])
			} else {
				require.Equal(t, DoctorInfo, row.Status)
				require.Equal(t, 1, row.Data["pending_acceptances"])
			}
		})
	}
}
