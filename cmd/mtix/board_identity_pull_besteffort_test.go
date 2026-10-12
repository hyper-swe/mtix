// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// The pull path treats identity caching as best-effort after data is applied.

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPullAndCacheIdentity_CacheFailure_PullSucceedsWithWarning(t *testing.T) {
	initTestApp(t)
	events := offlineEvents(t)
	hub := &fakeLateHub{events: events, pullEvents: events, hubNows: []time.Time{sweepHubT1}}
	var stderr bytes.Buffer
	res, err := pullAndCacheIdentity(t.Context(), testIngest(&stderr), hub, transport.PullCursor{}, 100,
		func() error { return errors.New("operator state refused") })
	require.NoError(t, err)
	assert.Equal(t, 3, res.pulled, "the pull's events stay applied")
	assert.Contains(t, stderr.String(), "WARN")
	assert.Contains(t, stderr.String(), "operator state refused")
}

func TestPullAndCacheIdentity_PullFailure_SkipsCache(t *testing.T) {
	initTestApp(t)
	hub := &fakeLateHub{pullErr: errors.New("hub down")}
	called := false
	_, err := pullAndCacheIdentity(t.Context(), testIngest(nil), hub, transport.PullCursor{}, 100,
		func() error { called = true; return nil })
	require.Error(t, err)
	assert.False(t, called, "a failed pull records no identity")
}
