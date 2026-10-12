package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A distinct observed board identity must remain a failure when the hub changes too.
func TestAppendBoardIdentityChecks_DisagreementWithHubChange_RemainsFailure(t *testing.T) {
	for _, changed := range []bool{false, true} {
		name := "SameHub"
		if changed {
			name = "ChangedHub"
		}
		t.Run(name, func(t *testing.T) {
			initTestApp(t)
			state := identityOperatorState(t)
			current := "0c6f03d9-3633-4ab7-bcc9-b8da8d966023"
			other := "4549495e-f331-4ac3-86af-decd9cbcbda6"
			original := oplocal.HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "first"}
			observed := original
			if changed {
				observed.FirstEventHash = "second"
			}
			require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "board-id"), []byte(current+"\n"), 0600))
			require.NoError(t, state.WriteJSON("grants", map[string]any{"board_grants": []oplocal.Grant{{BoardID: current, Hub: original}}}))
			require.NoError(t, state.ObserveBoard(filepath.Dir(app.mtixDir), other, observed))
			before, _, err := state.BoardObservation(filepath.Dir(app.mtixDir))
			require.NoError(t, err)
			report := appendBoardIdentityChecks(DoctorReport{OverallPass: true})
			assert.False(t, report.OverallPass, "a hub change cannot downgrade a board-ID disagreement to WARN")
			failedIdentity := false
			for _, check := range report.Checks {
				if check.Name == "board.id" && !check.Pass {
					failedIdentity = true
					assert.Contains(t, check.Detail, "multiple board identities")
				}
			}
			assert.True(t, failedIdentity, "doctor must report the distinct recorded board ID")
			after, _, err := state.BoardObservation(filepath.Dir(app.mtixDir))
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}
