// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Protected record shape and canonical outcome metadata are never caller input.
package oplocal

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPermissions_PendingEnvelopeDataMustDecodeAsTransaction(t *testing.T) {
	for name, data := range map[string]any{"array": []string{"synthetic"}, "string": "synthetic-invalid-transaction"} {
		t.Run(name, func(t *testing.T) {
			s := alertFixture(t)
			require.NoError(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare, nil))
			require.NoError(t, s.WriteJSON("permissions-pending", data))
			requirePermissionReadersRefuseUnchanged(t, s)
		})
	}
}

func TestPermissions_RetainedOutcomeMustMatchConfirmationMode(t *testing.T) {
	for _, outcome := range []string{"invalid", "denied_requires_operator"} {
		t.Run(outcome, func(t *testing.T) {
			s := alertFixture(t)
			require.NoError(t, s.ApplyPermissions(t.Context(), permissionFixtureBatch(), nil))
			rows, err := s.Confirmations(t.Context())
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, "yes", rows[0].Mode)
			rows[0].Outcome = outcome
			raw, err := json.Marshal(rows[0])
			require.NoError(t, err)
			writePermissionFixture(t, s, "confirmations.jsonl", append(raw, '\n'))
			before := alertStateSnapshot(t, s)
			_, err = s.Confirmations(t.Context())
			require.Error(t, err)
			require.Error(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare, nil))
			require.Equal(t, before, alertStateSnapshot(t, s))
		})
	}
}

func TestPermissions_InvalidScopeOrCallerIdentityCannotPrepareOrPublish(t *testing.T) {
	for _, kind := range []string{"legacy_scope", "caller_identity"} {
		t.Run(kind, func(t *testing.T) {
			s := alertFixture(t)
			require.NoError(t, s.ApplyPermissions(t.Context(), permissionFixtureBatch(), nil))
			before := alertStateSnapshot(t, s)
			called := false
			var err error
			if kind == "legacy_scope" {
				err = s.ApplyPermissionsScoped(t.Context(), "019a5cc9-8000-7000-8000-000000000002", permissionFixtureBatch(),
					func(context.Context, PermissionBatch) error { called = true; return nil })
			} else {
				intent := replayIntent()
				intent.ID = "invalid"
				err = s.ApplyPermissionIntent(t.Context(), intent, func(ctx context.Context, b PermissionBatch) (PermissionBatch, error) {
					called = true
					return replayPrepare(ctx, b)
				}, nil)
			}
			require.Error(t, err)
			require.False(t, called)
			require.Equal(t, before, alertStateSnapshot(t, s))
		})
	}
}
