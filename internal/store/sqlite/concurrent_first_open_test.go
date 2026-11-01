// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

const (
	firstOpenPathEnv = "MTIX_TEST_FIRST_OPEN_PATH"
	firstOpenWorkers = 4
	firstOpenRounds  = 50
)

// TestNew_ConcurrentFirstOpenNoWALPrestep_AllSucceedWithOneIdentity opens the
// SAME brand-new file from several goroutines with no WAL pre-step (MTIX-147),
// 50 times over: every open must succeed, no open may report corruption, and
// exactly one store identity may be minted per file.
func TestNew_ConcurrentFirstOpenNoWALPrestep_AllSucceedWithOneIdentity(t *testing.T) {
	for round := range firstOpenRounds {
		path := filepath.Join(t.TempDir(), "store.db")
		start := make(chan struct{})
		var wg sync.WaitGroup
		ids := make([]string, firstOpenWorkers)
		errs := make([]error, firstOpenWorkers)
		for i := range firstOpenWorkers {
			wg.Go(func() {
				<-start
				s, err := sqlite.New(path, slog.New(slog.DiscardHandler))
				if err != nil {
					errs[i] = err
					return
				}
				ids[i], errs[i] = s.StoreInstanceID(t.Context())
				if cerr := s.Close(); errs[i] == nil {
					errs[i] = cerr
				}
			})
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			require.NoError(t, err, "round %d worker %d", round, i)
			require.NotEmpty(t, ids[i], "round %d worker %d", round, i)
			assert.Equal(t, ids[0], ids[i], "round %d: one identity per file", round)
		}
	}
}

// TestNew_ConcurrentFirstOpenAcrossProcesses_AllSucceed repeats the first-open
// race with separate OS processes (MTIX-147), the real-world form of it.
func TestNew_ConcurrentFirstOpenAcrossProcesses_AllSucceed(t *testing.T) {
	if p := os.Getenv(firstOpenPathEnv); p != "" {
		s, err := sqlite.New(p, slog.New(slog.DiscardHandler))
		require.NoError(t, err)
		id, err := s.StoreInstanceID(t.Context())
		require.NoError(t, err)
		require.NoError(t, s.Close())
		_, _ = os.Stdout.WriteString("IDENTITY=" + id + "\n")
		return
	}
	for round := range 10 {
		path := filepath.Join(t.TempDir(), "store.db")
		outs := make([][]byte, firstOpenWorkers)
		errs := make([]error, firstOpenWorkers)
		var wg sync.WaitGroup
		for i := range firstOpenWorkers {
			wg.Go(func() {
				cmd := exec.Command(os.Args[0], "-test.run=^TestNew_ConcurrentFirstOpenAcrossProcesses_AllSucceed$") //nolint:gosec // re-exec of this test binary
				cmd.Env = append(hostguard.Environ(), firstOpenPathEnv+"="+path)
				outs[i], errs[i] = cmd.CombinedOutput()
			})
		}
		wg.Wait()
		var first string
		for i := range firstOpenWorkers {
			require.NoError(t, errs[i], "round %d process %d: %s", round, i, outs[i])
			var id string
			for _, line := range strings.Split(string(outs[i]), "\n") {
				if v, ok := strings.CutPrefix(line, "IDENTITY="); ok {
					id = v
				}
			}
			require.NotEmpty(t, id, "round %d process %d: %s", round, i, outs[i])
			if first == "" {
				first = id
			}
			assert.Equal(t, first, id, "round %d: one identity per file", round)
		}
	}
}
