// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

type pausedWakeReplace struct {
	wakeReplaceFixture
	resume   func()
	done     <-chan struct{}
	err      *error
	oldSends []int64
}

func pauseWakeReplace(t *testing.T, phase string) pausedWakeReplace {
	t.Helper()
	f := newWakeReplaceFixture(t, 1, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(resume) }) }
	pause := func() {
		once.Do(func() {
			close(entered)
			select {
			case <-resume:
			case <-ctx.Done():
			}
		})
	}
	opts := f.options(always)
	if phase == "candidate" {
		opts.Wakeable = func(context.Context, string) (bool, string, error) { pause(); return true, "test", nil }
	} else {
		f.adapter.onDeliver = func(int64) { pause() }
	}
	scanner, err := service.NewWakeScanner(opts)
	require.NoError(t, err)
	var scanErr error
	go func() { defer close(done); scanErr = scanner.Scan(ctx) }()
	t.Cleanup(func() { release(); waitWakeReplace(ctx, t, done) })
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("old wake pass did not pause")
	}
	return pausedWakeReplace{wakeReplaceFixture: f, resume: release, done: done, err: &scanErr, oldSends: f.adapter.seqs()}
}

func waitWakeReplace(ctx context.Context, t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
		return
	default:
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Error("old wake pass did not finish")
	}
}

func TestWakeScan_ReplaceDuringPass_OldIdentityCannotConsumeFreshMail(t *testing.T) {
	for _, phase := range []string{"candidate", "completion"} {
		t.Run(phase, func(t *testing.T) {
			f := pauseWakeReplace(t, phase)
			fresh := f.replaceAndAppend(t, 2)
			f.resume()
			select {
			case <-f.done:
			case <-time.After(10 * time.Second):
				t.Fatal("old wake pass did not finish")
			}
			if phase == "completion" {
				assert.ErrorIs(t, *f.err, model.ErrConflict, "the old attempt token was removed by replace")
			} else {
				assert.NoError(t, *f.err)
			}
			mark, _ := f.mark()
			assert.Equal(t, f.survivorTail, mark, "the old pass cannot move the replacement registration's mark")
			assert.Equal(t, f.oldSends, f.adapter.seqs(), "an old candidate must not start a send after replace")
			f.adapter.onDeliver = nil
			require.NoError(t, f.scanner(always).Scan(t.Context()))
			require.NoError(t, f.scanner(always).Scan(t.Context()))
			want := append(append([]int64(nil), f.oldSends...), fresh...)
			assert.Equal(t, want, f.adapter.seqs(), "fresh events each wake once, including the lower reused sequence")
		})
	}
}
