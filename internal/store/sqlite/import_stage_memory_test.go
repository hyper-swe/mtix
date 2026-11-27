// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MTIX-2.3.2.1: staging and verification stay within a fixed heap ceiling
// whatever the size of the board. The ceiling is stageHeapCeiling above the
// heap in use before the call; the generated board is far larger than it.
const (
	stageHeapCeiling = 8 << 20 // bytes over the starting heap
	stageBoardNodes  = 8000
	stageNodeBody    = 2048 // bytes of description per node
)

// writeLargeBoard writes a valid export of stageBoardNodes nodes to path
// without ever holding more than one node, and returns the file size. Its
// checksum is hashed independently of the code under test: the canonical
// document is {"nodes":[...],"deps":[]} over the reader view of each node.
func writeLargeBoard(t *testing.T, path string) (int64, string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	w := bufio.NewWriterSize(f, 1<<20)
	h := sha256.New()
	both := func(s string) { _, _ = w.WriteString(s); _, _ = h.Write([]byte(s)) }
	_, _ = fmt.Fprintf(w, `{"version":1,"project":"BIG","node_count":%d,"nodes":[`, stageBoardNodes)
	_, _ = h.Write([]byte(`{"nodes":[`))
	filler := strings.Repeat("lorem ipsum dolor sit amet ", stageNodeBody/27+1)[:stageNodeBody]
	for i := 0; i < stageBoardNodes; i++ {
		n := stageNode(fmt.Sprintf("BIG-%06d", i), fmt.Sprintf("node %d", i))
		n.Description = filler
		plain, err := json.Marshal(n)
		require.NoError(t, err)
		n.readerView = true
		view, err := json.Marshal(n)
		require.NoError(t, err)
		if i > 0 {
			both(",")
		}
		_, _ = w.Write(plain)
		_, _ = h.Write(view)
	}
	both("]")
	_, _ = h.Write([]byte(`,"deps":[]}`))
	sum := fmt.Sprintf("%x", h.Sum(nil))
	_, _ = w.WriteString(`,"dependencies":[],"checksum":"` + sum + `"}`)
	require.NoError(t, w.Flush())
	require.NoError(t, f.Close())
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.Size(), sum
}

// peakHeapOver runs fn and returns the highest heap in use it reached above
// the heap in use when it began.
func peakHeapOver(fn func()) uint64 {
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak uint64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peak {
				peak = m.HeapAlloc
			}
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	fn()
	close(stop)
	wg.Wait()
	if peak < base.HeapAlloc {
		return 0
	}
	return peak - base.HeapAlloc
}

func TestStageExport_LargeBoardStaysUnderHeapCeiling(t *testing.T) {
	if testing.Short() {
		t.Skip("generates a large board")
	}
	defer debug.SetGCPercent(debug.SetGCPercent(20))
	path := filepath.Join(t.TempDir(), "big.json")
	size, sum := writeLargeBoard(t, path)
	require.Greater(t, size, int64(3*stageHeapCeiling/2), "the board must dwarf the ceiling")

	var verifyErr error
	var staged int
	peak := peakHeapOver(func() {
		f, err := os.Open(path)
		require.NoError(t, err)
		defer func() { _ = f.Close() }()
		st, err := StageExport(context.Background(), f, t.TempDir())
		require.NoError(t, err)
		defer func() { require.NoError(t, st.Close()) }()
		staged = st.NodeCount()
		verifyErr = st.Verify(context.Background())
		assert.Equal(t, sum, st.Header.Checksum)
	})
	require.NoError(t, verifyErr)
	assert.Equal(t, stageBoardNodes, staged)
	t.Logf("board %d MiB, %d nodes: peak heap over start %d KiB (ceiling %d MiB)",
		size>>20, staged, peak>>10, stageHeapCeiling>>20)
	assert.Less(t, peak, uint64(stageHeapCeiling), "staging + verification must not grow with the board")
}

// The control: the current decoder holds every node, so the same board blows
// the ceiling. It proves the ceiling above is a real bound, not slack.
func TestStageExport_CurrentDecoderExceedsTheCeiling(t *testing.T) {
	if testing.Short() {
		t.Skip("generates a large board")
	}
	defer debug.SetGCPercent(debug.SetGCPercent(20))
	path := filepath.Join(t.TempDir(), "big.json")
	writeLargeBoard(t, path)
	peak := peakHeapOver(func() {
		f, err := os.Open(path)
		require.NoError(t, err)
		defer func() { _ = f.Close() }()
		data, err := DecodeExportData(f)
		require.NoError(t, err)
		ok, err := VerifyExportChecksum(data)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Logf("current decoder peak heap over start: %d MiB", peak>>20)
	assert.Greater(t, peak, uint64(stageHeapCeiling), "the whole-board decoder must breach the ceiling")
}
