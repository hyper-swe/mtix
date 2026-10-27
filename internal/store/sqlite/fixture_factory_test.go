// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var fixtureImageCache struct {
	once  sync.Once
	image emptyStoreImage
}

func cachedFixtureImage(t *testing.T) emptyStoreImage {
	t.Helper()
	fixtureImageCache.once.Do(func() { fixtureImageCache.image = buildEmptyStoreImage(t) })
	// A fatal builder failure must never turn another caller into an empty-file fixture.
	require.NotEmpty(t, fixtureImageCache.image, "empty-store image builder did not complete")
	return fixtureImageCache.image
}

// FixtureStoreForTest opens a private empty image through real New, with a fresh
// creation identity. Tests of store creation or initialization must use New.
// Only immutable backup bytes are shared; files, pools and Store state are owned.
func FixtureStoreForTest(t *testing.T, path string) *Store {
	t.Helper()
	require.NoFileExists(t, path)
	require.NoError(t, os.WriteFile(path, []byte(cachedFixtureImage(t)), 0o600))
	s := openImageFixture(t, path)
	id, err := uuid.NewRandom()
	require.NoError(t, err)
	result, err := s.writeDB.ExecContext(t.Context(),
		"UPDATE meta SET value = ? WHERE key = 'store_instance_id'", id.String())
	require.NoError(t, err)
	n, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	return s
}

// TestFreshFixtureRoutes_RealTree_UseFreshConstructor runs the source-routing
// policy independently and proves the guard still rejects an image-backed route.
func TestFreshFixtureRoutes_RealTree_UseFreshConstructor(t *testing.T) {
	requireFreshFixtureRoutes(t)
	if os.Getenv("MTIX_TEST_FRESH_ROUTES_CHILD") == "1" {
		return
	}
	requireFreshFixtureRoutesRejectsImage(t)
}

func TestFixtureStore_MatchesFreshDefaultsAndPragmas(t *testing.T) {
	fresh := openImageFixture(t, filepath.Join(t.TempDir(), "fresh.db"))
	a := FixtureStoreForTest(t, filepath.Join(t.TempDir(), "a.db"))
	b := FixtureStoreForTest(t, filepath.Join(t.TempDir(), "b.db"))
	for _, s := range []*Store{a, b} {
		requireEmptyImageFixture(t, s)
		require.Equal(t, imageFixtureComparableState(t, fresh), imageFixtureComparableState(t, s))
		require.Equal(t, imageFixturePragmas(t, fresh.writeDB), imageFixturePragmas(t, s.writeDB))
		require.Equal(t, imageFixturePragmas(t, fresh.readDB), imageFixturePragmas(t, s.readDB))
		require.NotEqual(t, imageFixtureMeta(t, fresh, "store_instance_id"), imageFixtureMeta(t, s, "store_instance_id"))
	}
	require.NotEqual(t, imageFixtureMeta(t, a, "store_instance_id"), imageFixtureMeta(t, b, "store_instance_id"))
	requireImageFilesOwned(t, a, b)
}

func TestFixtureStore_ParallelCopiesOwnStateAndIdentity(t *testing.T) {
	t.Parallel()
	var ids sync.Map
	for i := range 8 {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			t.Parallel()
			s := FixtureStoreForTest(t, filepath.Join(t.TempDir(), "copy.db"))
			requireEmptyImageFixture(t, s)
			_, shared := ids.LoadOrStore(imageFixtureMeta(t, s, "store_instance_id"), true)
			require.False(t, shared, "independent fixtures must not share creation identity")
			at := time.Date(2030, 1, i+1, 0, 0, 0, 0, time.UTC)
			s.SetClock(func() time.Time { return at })
			calls := 0
			s.AddOnCommit(func() { calls++ })
			mutateImageFixture(t, s, t.Name(), "fixture-author", at)
			require.Equal(t, 2, calls)
			requireImageReopen(t, s)
		})
	}
}
