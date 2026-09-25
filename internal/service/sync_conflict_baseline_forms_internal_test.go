// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.11 (FR-15.2h) that reach inside SyncService: an
// older baseline form that cannot be hashed. Every form mtix builds encodes
// the same values as the current form, so no stored data reaches this
// path; the forms hasConflict tries are therefore set here (olderForms).
// Written red-first.
package service

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// unknownForm is a baseline form with a flag exportHash does not know.
const unknownForm baselineForm = 1 << 7

// TestExportHash_UnknownForm_ReturnsInvalidInput verifies a form with a flag
// exportHash does not know is refused, never hashed as another form.
func TestExportHash_UnknownForm_ReturnsInvalidInput(t *testing.T) {
	for _, form := range []baselineForm{unknownForm, form100 | unknownForm} {
		_, err := exportHash(&sqlite.ExportData{}, form)
		assert.ErrorIs(t, err, model.ErrInvalidInput, "form %d", uint8(form))
	}
}

// TestHasConflict_OlderFormCannotBeHashed_ReturnsWrappedError verifies a
// failure to hash the store in an older form is returned with its context,
// never read as a conflict or as a match, and the baseline is left as it is.
func TestHasConflict_OlderFormCannotBeHashed_ReturnsWrappedError(t *testing.T) {
	mtixDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(mtixDir, "data"), 0o755))
	baselinePath := filepath.Join(mtixDir, "data", "sync-db.sha256")
	require.NoError(t, os.WriteFile(baselinePath, []byte("a baseline no form matches"), 0o644))
	s := &SyncService{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), olderForms: []baselineForm{unknownForm}}

	conflict, err := s.hasConflict(mtixDir, &sqlite.ExportData{})

	require.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Contains(t, err.Error(), "compare the conflict baseline with older forms: ")
	assert.False(t, conflict)
	baseline, readErr := os.ReadFile(baselinePath)
	require.NoError(t, readErr)
	assert.Equal(t, "a baseline no form matches", string(baseline))
}
