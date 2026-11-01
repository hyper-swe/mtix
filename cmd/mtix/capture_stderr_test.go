// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A checked write to stderr after the capture must succeed: the helper has to
// put os.Stderr back when it returns, not only when the test ends.
func TestCaptureStderr_WriteAfterCapture_Succeeds(t *testing.T) {
	orig := os.Stderr
	out := captureStderr(t, func() { _, _ = fmt.Fprint(os.Stderr, "inside") })
	assert.Equal(t, "inside", out)
	assert.Same(t, orig, os.Stderr, "os.Stderr is restored on return")
	_, err := fmt.Fprint(os.Stderr, "")
	require.NoError(t, err, "a write after the capture must not hit a closed pipe")
}

// Nested captures unwind in order: after the inner one returns, writes go to
// the outer capture again.
func TestCaptureStderr_Nested_UnwindsInOrder(t *testing.T) {
	orig := os.Stderr
	var inner string
	outer := captureStderr(t, func() {
		_, err := fmt.Fprint(os.Stderr, "a")
		require.NoError(t, err)
		inner = captureStderr(t, func() { _, _ = fmt.Fprint(os.Stderr, "b") })
		_, err = fmt.Fprint(os.Stderr, "c")
		require.NoError(t, err, "the outer capture is active again after the inner one returns")
	})
	assert.Equal(t, "b", inner)
	assert.Equal(t, "ac", outer)
	assert.Same(t, orig, os.Stderr)
}

// A panic inside fn must not leave os.Stderr pointing at the capture pipe.
func TestCaptureStderr_PanicInFn_RestoresStderr(t *testing.T) {
	orig := os.Stderr
	require.PanicsWithValue(t, "boom", func() {
		captureStderr(t, func() { panic("boom") })
	})
	assert.Same(t, orig, os.Stderr, "os.Stderr is restored even when fn panics")
}
