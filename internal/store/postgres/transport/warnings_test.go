// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// TestWarningLog_Record_KeepsOnlyWarnings: the log keeps WARNING notices
// and ignores NOTICE and lower severities (MTIX-95.1).
func TestWarningLog_Record_KeepsOnlyWarnings(t *testing.T) {
	tests := []struct {
		name   string
		notice pgconn.Notice
		kept   bool
	}{
		{"warning", pgconn.Notice{SeverityUnlocalized: "WARNING", Message: "w"}, true},
		{"localized warning only", pgconn.Notice{Severity: "WARNING", Message: "w"}, true},
		{"notice", pgconn.Notice{SeverityUnlocalized: "NOTICE", Message: "n"}, false},
		{"localized severity differs", pgconn.Notice{Severity: "WARNUNG", SeverityUnlocalized: "WARNING", Message: "w"}, true},
		{"info", pgconn.Notice{SeverityUnlocalized: "INFO", Message: "i"}, false},
		{"debug", pgconn.Notice{SeverityUnlocalized: "DEBUG", Message: "d"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := transport.NewWarningLog()
			n := tt.notice
			log.Record(&n)
			got := log.Take()
			if tt.kept {
				require.Equal(t, []string{"w"}, got)
			} else {
				require.Empty(t, got)
			}
		})
	}
}

// TestWarningLog_Take_ClearsAndCaps: Take returns the warnings recorded
// since the last Take, and the log never grows past its cap.
func TestWarningLog_Take_ClearsAndCaps(t *testing.T) {
	log := transport.NewWarningLog()
	log.Record(nil)
	require.Empty(t, log.Take(), "a nil notice is ignored")

	for i := 0; i < 500; i++ {
		log.Record(&pgconn.Notice{SeverityUnlocalized: "WARNING", Message: fmt.Sprintf("w%d", i)})
	}
	got := log.Take()
	require.Len(t, got, transport.MaxLoggedWarnings)
	require.Equal(t, "w0", got[0], "the first warnings are kept")
	require.Empty(t, log.Take(), "Take clears the log")
}

// TestWarningLog_Record_IsSafeForConcurrentUse: pool connections may
// deliver notices from several goroutines.
func TestWarningLog_Record_IsSafeForConcurrentUse(t *testing.T) {
	log := transport.NewWarningLog()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				log.Record(&pgconn.Notice{SeverityUnlocalized: "WARNING", Message: "w"})
			}
		}()
	}
	wg.Wait()
	require.Len(t, log.Take(), 32)
}

// TestWarningLog_NilLog_IsSafe: a nil log records and returns nothing.
func TestWarningLog_NilLog_IsSafe(t *testing.T) {
	var log *transport.WarningLog
	log.Record(&pgconn.Notice{SeverityUnlocalized: "WARNING", Message: "w"})
	require.Empty(t, log.Take())
}
