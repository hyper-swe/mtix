package service_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func activityProjectionFixture(t *testing.T) (*service.NodeService, *sqlite.Store, string) {
	t.Helper()
	svc, st, _ := newTestNodeService(t)
	node, err := svc.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "activity projection", Creator: "writer"})
	require.NoError(t, err)
	return svc, st, node.ID
}

func setActivityProjection(t *testing.T, st *sqlite.Store, id string, activity []model.ActivityEntry, annotations []model.Annotation) {
	t.Helper()
	a, err := json.Marshal(activity)
	require.NoError(t, err)
	b, err := json.Marshal(annotations)
	require.NoError(t, err)
	tx, err := st.WriteDB().BeginTx(t.Context(), nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Error(err)
		}
	})
	_, err = tx.ExecContext(t.Context(), `UPDATE nodes SET activity = ?, annotations = ? WHERE id = ?`, string(a), string(b), id)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
}

func TestNodeServiceGetActivity_MergesPlainAndAddressedWithoutWriting(t *testing.T) {
	svc, st, id := activityProjectionFixture(t)
	stamp := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, ann := range []model.Annotation{
		{ID: "plain", Author: "writer", Text: "plain body", CreatedAt: stamp},
		{ID: "addressed", Author: "other", Text: "addressed body", Addressee: "reader", CreatedAt: stamp.Add(time.Second)},
	} {
		require.NoError(t, st.AppendAnnotation(t.Context(), id, ann))
	}
	storageBefore := activityStorageState(t, st, id)
	before, err := st.GetNode(t.Context(), id)
	require.NoError(t, err)
	rows, err := svc.GetActivity(t.Context(), id, 0, 0)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	require.Equal(t, []string{"addressed", "plain"}, []string{rows[0].ID, rows[1].ID})
	raw, err := json.Marshal(rows)
	require.NoError(t, err)
	var wire []map[string]any
	require.NoError(t, json.Unmarshal(raw, &wire))
	require.Equal(t, "reader", wire[0]["addressee"])
	require.Equal(t, "other", wire[0]["author"])
	require.Equal(t, "addressed body", wire[0]["text"])
	require.NotContains(t, wire[1], "addressee")
	after, err := st.GetNode(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, storageBefore, activityStorageState(t, st, id))
}

func TestNodeServiceGetActivity_StableMergeBeforePagination(t *testing.T) {
	svc, st, id := activityProjectionFixture(t)
	stamp := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	var annotations []model.Annotation
	for i := 0; i < 63; i++ {
		annotations = append(annotations, model.Annotation{ID: fmt.Sprintf("ann-%03d", i), Author: "writer", Text: "same body", CreatedAt: stamp.Add(time.Duration(i/3) * time.Second)})
	}
	activity := []model.ActivityEntry{
		{ID: "legacy", Type: model.ActivityTypeStatusChange, Author: "writer", CreatedAt: stamp.Add(100 * time.Second)},
		{ID: "ann-010", Type: model.ActivityTypeComment, Author: "writer", Text: "same body", CreatedAt: annotations[10].CreatedAt},
	}
	setActivityProjection(t, st, id, activity, annotations)
	all, err := svc.GetActivity(t.Context(), id, 0, 0)
	require.NoError(t, err)
	require.Len(t, all, 64)
	require.Equal(t, "legacy", all[0].ID)
	for i := 1; i < len(all); i++ {
		prior, next := all[i-1], all[i]
		require.False(t, prior.CreatedAt.Before(next.CreatedAt))
		if prior.CreatedAt.Equal(next.CreatedAt) {
			require.Greater(t, prior.ID, next.ID)
		}
	}
	var combined []model.ActivityEntry
	for offset := 0; offset < len(all); offset += 20 {
		page, err := svc.GetActivity(t.Context(), id, 20, offset)
		require.NoError(t, err)
		combined = append(combined, page...)
	}
	require.Equal(t, all, combined)
	last, err := svc.GetActivity(t.Context(), id, 20, len(all))
	require.NoError(t, err)
	require.Empty(t, last)
}

func TestNodeServiceGetActivity_CanonicalAnnotationWinsIDOverlap(t *testing.T) {
	svc, st, id := activityProjectionFixture(t)
	stamp := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	setActivityProjection(t, st, id, []model.ActivityEntry{{ID: "same", Type: model.ActivityTypeComment, Author: "legacy", Text: "legacy body", CreatedAt: stamp}}, []model.Annotation{{ID: "same", Author: "writer", Addressee: "reader", Text: "current body", CreatedAt: stamp}})
	rows, err := svc.GetActivity(t.Context(), id, 50, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "writer", rows[0].Author)
	require.Equal(t, "current body", rows[0].Text)
	raw, err := json.Marshal(rows)
	require.NoError(t, err)
	var wire []map[string]any
	require.NoError(t, json.Unmarshal(raw, &wire))
	require.Equal(t, "reader", wire[0]["addressee"])
}

// Include the activity column (absent from model.Node) and all connection writes.
func activityStorageState(t *testing.T, st *sqlite.Store, id string) []any {
	t.Helper()
	var activity, annotations, updated string
	require.NoError(t, st.ReadDB().QueryRowContext(t.Context(), `SELECT activity, annotations, updated_at FROM nodes WHERE id = ?`, id).Scan(&activity, &annotations, &updated))
	var changes int
	require.NoError(t, st.WriteDB().QueryRowContext(t.Context(), `SELECT total_changes()`).Scan(&changes))
	return []any{activity, annotations, updated, changes}
}

func TestNodeServiceGetActivity_MissingDeletedAndCancelled_PreserveErrors(t *testing.T) {
	for _, kind := range []string{"missing", "deleted", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			svc, _, id := activityProjectionFixture(t)
			ctx := t.Context()
			want := model.ErrNotFound
			switch kind {
			case "missing":
				id = "TEST-999"
			case "deleted":
				require.NoError(t, svc.DeleteNode(ctx, id, false, "writer"))
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			}
			rows, err := svc.GetActivity(ctx, id, 50, 0)
			require.ErrorIs(t, err, want)
			require.Nil(t, rows)
		})
	}
}

func TestNodeServiceGetActivity_InvalidAnnotations_PropagatesReadError(t *testing.T) {
	svc, st, id := activityProjectionFixture(t)
	tx, err := st.WriteDB().BeginTx(t.Context(), nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Error(err)
		}
	})
	_, err = tx.ExecContext(t.Context(), `UPDATE nodes SET annotations = ? WHERE id = ?`, "{", id)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	rows, err := svc.GetActivity(t.Context(), id, 50, 0)
	require.Nil(t, rows)
	var syntax *json.SyntaxError
	require.ErrorAs(t, err, &syntax)
	var operation interface{ Operation() string }
	require.ErrorAs(t, err, &operation)
	require.Contains(t, operation.Operation(), "get activity")
}
