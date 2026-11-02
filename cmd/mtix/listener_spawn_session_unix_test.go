// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The detached child must lead its own session, so the command that started
// it can end without taking it down.
func TestListenerSysProcAttr_OwnSession(t *testing.T) {
	assert.True(t, listenerSysProcAttr().Setsid)
}
