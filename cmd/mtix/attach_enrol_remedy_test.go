// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hyper-swe/mtix/internal/service"
)

func TestAttachErrorRemedy_PerCause_NamesTheRowThatPrintsTheCommand(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want string
	}{
		{"running", service.ErrEnrolInProgress, "wait for worktree enrollment"},
		{"interrupted", service.ErrEnrolInterrupted, "board.enrol-interrupted"},
		{"torn marker", errors.Join(service.ErrEnrolMarkerUnreadable, errors.New("decode")), "board.enrol-interrupted"},
		{"lock probe", errors.Join(service.ErrEnrolLockUnprobeable, errors.New("open")), "board.enrol-lock"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			class, fix := attachErrorRemedy(&boardEnrolGuardError{cause: tt.err}, "fallback")
			assert.Equal(t, attachAlertEnrolBlocked, class)
			assert.Contains(t, fix, tt.want)
			assert.Contains(t, fix, "mtix doctor")
		})
	}
	class, fix := attachErrorRemedy(errors.New("other"), "fallback")
	assert.Equal(t, attachAlertVerbFailure, class)
	assert.Equal(t, "fallback", fix)
}
