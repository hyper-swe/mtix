// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hyper-swe/mtix/internal/service"
)

func TestAttachErrorRemedy_PerCause_ExactFix(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want string
	}{
		{"running", service.ErrEnrolInProgress, "wait for worktree enrollment to finish; then run mtix doctor"},
		{"interrupted", service.ErrEnrolInterrupted, "mtix doctor"},
		{"torn marker", errors.Join(service.ErrEnrolMarkerUnreadable, errors.New("decode")),
			"run mtix doctor; its board.enrol-interrupted row prints the exact command for the unreadable enrollment marker"},
		{"lock probe", errors.Join(service.ErrEnrolLockUnprobeable, errors.New("open")),
			"run mtix doctor; its board.enrol-lock row prints the exact command for the unprobeable enrollment lock"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			class, fix := attachErrorRemedy(&boardEnrolGuardError{cause: tt.err}, "fallback")
			assert.Equal(t, attachAlertEnrolBlocked, class)
			assert.Equal(t, tt.want, fix)
		})
	}
	class, fix := attachErrorRemedy(errors.New("other"), "fallback")
	assert.Equal(t, attachAlertVerbFailure, class)
	assert.Equal(t, "fallback", fix)
}
