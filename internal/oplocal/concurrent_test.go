// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestState_ConcurrentInput(t *testing.T) {
	s := inputState(t)
	require.NoError(t, s.Ensure())
	start := make(chan struct{})
	errs := make([]error, 12)
	var workers sync.WaitGroup
	for i := range errs {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			errs[i] = s.Ensure()
		}()
	}
	close(start)
	workers.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
}
