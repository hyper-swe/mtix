// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

func TestAddressedSendChild_ProductionWrapperUsesFiveMinuteBound(t *testing.T) {
	t.Setenv(transport.EnvDSN, fakeEnrolledDSN)
	initTestApp(t)
	_ = seedOneNode(t)
	var observed context.Context
	push := func(ctx context.Context, _ io.Writer, _ transport.Options) error {
		observed = ctx
		return nil
	}
	ctx := context.WithValue(context.Background(), addressedPushBodyKey{}, push)
	cmd := newSyncPushCmd()
	cmd.SetArgs([]string{"--on-addressed-send", "--insecure-tls"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	start := time.Now()
	err := cmd.ExecuteContext(ctx)
	require.NotNil(t, observed, "the production command must invoke the scoped push body")
	require.NoError(t, err)
	deadline, ok := observed.Deadline()
	require.True(t, ok)
	assert.WithinDuration(t, start.Add(5*time.Minute), deadline, time.Second,
		"the production wrapper must pass the documented five minute bound")
	assert.ErrorIs(t, observed.Err(), context.Canceled, "completion must release the deadline timer")
}
