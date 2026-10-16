// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

// testGlueWriter stands in for a harness glue writer; production glue writers
// arrive with their adapters, so this type lives only in tests.
type testGlueWriter struct {
	harness string
	writes  int
	state   ArtifactState
	err     error
}

func (w *testGlueWriter) Name() string { return "glue." + w.harness }
func (w *testGlueWriter) Check(context.Context) (ArtifactStatus, error) {
	return ArtifactStatus{State: w.state}, nil
}
func (w *testGlueWriter) Write(context.Context) error {
	if w.err != nil {
		return w.err
	}
	w.writes++
	w.state = ArtifactCurrent
	return nil
}

func TestMarkGlueForRefresh_RecordsSeenProtocolPerHarness(t *testing.T) {
	st := newMemoryStore(t.TempDir())
	require.NoError(t, MarkGlueForRefresh(st, "claude-code", 1))
	require.NoError(t, MarkGlueForRefresh(st, "pi", 4))
	pending, err := PendingGlueRefresh(st)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"claude-code": 1, "pi": 4}, pending)
	require.NoError(t, ClearGlueRefresh(st, "claude-code"))
	pending, err = PendingGlueRefresh(st)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"pi": 4}, pending)
}

func TestMarkGlueForRefresh_InvalidInput_Refused(t *testing.T) {
	st := newMemoryStore(t.TempDir())
	assert.Error(t, MarkGlueForRefresh(st, "unknown-harness", 1))
	assert.Error(t, MarkGlueForRefresh(st, "codex", -1))
}

func TestPendingGlueRefresh_IgnoredRecord_ReadsAsEmpty(t *testing.T) {
	st := newMemoryStore(t.TempDir())
	require.NoError(t, MarkGlueForRefresh(st, "codex", 1))
	st.reports = []oplocal.Report{{Name: "attach-refresh.json", Reason: "record belongs to another host"}}
	pending, err := PendingGlueRefresh(st)
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestRegistryRefresh_MarkedGlueWithWriter_RewrittenAndCleared(t *testing.T) {
	st := newMemoryStore(t.TempDir())
	glue := &testGlueWriter{harness: "claude-code", state: ArtifactStale}
	reg, err := NewRegistry(2, glue)
	require.NoError(t, err)
	require.NoError(t, MarkGlueForRefresh(st, "claude-code", 1))

	report, err := reg.Refresh(context.Background(), st)
	require.NoError(t, err)
	assert.Equal(t, []string{"glue.claude-code"}, report.Rewritten)
	assert.Empty(t, report.Pending)
	assert.Equal(t, 1, glue.writes, "the marker refresh and the stale check write once")
	pending, err := PendingGlueRefresh(st)
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestRegistryRefresh_UnrefreshableMarkers_StayPending(t *testing.T) {
	tests := []struct {
		name    string
		current int
		seen    int
		writers []Writer
	}{
		{"no writer for the harness in this build", 2, 1, nil},
		{"glue newer than this binary is never downgraded", 2, 3, []Writer{&testGlueWriter{harness: "codex", state: ArtifactCurrent}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newMemoryStore(t.TempDir())
			reg, err := NewRegistry(tt.current, tt.writers...)
			require.NoError(t, err)
			require.NoError(t, MarkGlueForRefresh(st, "codex", tt.seen))
			report, err := reg.Refresh(context.Background(), st)
			require.NoError(t, err)
			assert.Equal(t, []string{"glue.codex"}, report.Pending)
			assert.Empty(t, report.Rewritten)
			pending, err := PendingGlueRefresh(st)
			require.NoError(t, err)
			assert.Equal(t, map[string]int{"codex": tt.seen}, pending)
		})
	}
}

func TestRegistryRefresh_WriterFails_MarkerKeptAndErrorReturned(t *testing.T) {
	st := newMemoryStore(t.TempDir())
	boom := errors.New("glue file locked")
	reg, err := NewRegistry(2, &testGlueWriter{harness: "pi", state: ArtifactStale, err: boom})
	require.NoError(t, err)
	require.NoError(t, MarkGlueForRefresh(st, "pi", 1))
	_, err = reg.Refresh(context.Background(), st)
	require.ErrorIs(t, err, boom)
	pending, err := PendingGlueRefresh(st)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"pi": 1}, pending)
}

func TestNewRegistry_InvalidWriters_Refused(t *testing.T) {
	_, err := NewRegistry(1, &testGlueWriter{harness: "pi"}, &testGlueWriter{harness: "pi"})
	assert.Error(t, err, "duplicate names")
	_, err = NewRegistry(1, nil)
	assert.Error(t, err, "nil writer")
	_, err = NewRegistry(0)
	assert.Error(t, err, "invalid protocol")
}

func TestRegistryWriter_ByName_FindsRegistered(t *testing.T) {
	glue := &testGlueWriter{harness: "codex"}
	reg, err := NewRegistry(1, glue)
	require.NoError(t, err)
	assert.Same(t, glue, reg.Writer("glue.codex"))
	assert.Nil(t, reg.Writer("glue.pi"))
}
