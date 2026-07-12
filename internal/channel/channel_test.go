// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package channel

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// fakeReader scripts successive InboxWait returns.
type fakeReader struct {
	mu      sync.Mutex
	batches [][]sqlite.InboxEvent
	err     error
}

func (f *fakeReader) InboxWait(_ context.Context, _ string, _ time.Duration) ([]sqlite.InboxEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if len(f.batches) == 0 {
		return nil, nil
	}
	b := f.batches[0]
	f.batches = f.batches[1:]
	return b, nil
}

// TestInboxSource_YieldsEachEventOnce: the inbox resurfaces unacked events on
// every read (MTIX-55, by design); the source high-water filter turns that
// into push-each-once so a live session is not spammed with repeats.
func TestInboxSource_YieldsEachEventOnce(t *testing.T) {
	r := &fakeReader{batches: [][]sqlite.InboxEvent{
		{{Seq: 1, NodeID: "P-1", Author: "a", Body: "one"}},
		// second read resurfaces 1 (unacked) alongside the new 2
		{{Seq: 1, NodeID: "P-1", Author: "a", Body: "one"}, {Seq: 2, NodeID: "P-2", Author: "b", Body: "two"}},
		{{Seq: 1, NodeID: "P-1", Author: "a", Body: "one"}, {Seq: 2, NodeID: "P-2", Author: "b", Body: "two"}},
	}}
	src := NewInboxSource(r, "worker")
	ctx := context.Background()

	got1, err := src.Next(ctx, time.Second)
	require.NoError(t, err)
	require.Len(t, got1, 1)
	assert.Equal(t, int64(1), got1[0].Seq)

	got2, err := src.Next(ctx, time.Second)
	require.NoError(t, err)
	require.Len(t, got2, 1, "the resurfaced seq 1 is filtered; only the new event yields")
	assert.Equal(t, Event{Seq: 2, Node: "P-2", From: "b", Body: "two"}, got2[0])

	got3, err := src.Next(ctx, time.Second)
	require.NoError(t, err)
	assert.Empty(t, got3, "nothing new")
}

// recordingAdapter captures pushes; can fail on demand.
type recordingAdapter struct {
	mu     sync.Mutex
	pushed []Event
	fail   bool
}

func (a *recordingAdapter) Name() string { return "recording" }
func (a *recordingAdapter) Push(e Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail {
		return errors.New("session gone")
	}
	a.pushed = append(a.pushed, e)
	return nil
}
func (a *recordingAdapter) events() []Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Event(nil), a.pushed...)
}

// stubSource yields one batch then blocks until ctx cancel.
type stubSource struct {
	once  sync.Once
	batch []Event
}

func (s *stubSource) Next(ctx context.Context, _ time.Duration) ([]Event, error) {
	var out []Event
	s.once.Do(func() { out = s.batch })
	if out != nil {
		return out, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestPump_PushesInOrderAndStopsOnCancel: the pump forwards each event in
// order and exits promptly when the session context ends.
func TestPump_PushesInOrderAndStopsOnCancel(t *testing.T) {
	src := &stubSource{batch: []Event{
		{Seq: 1, Node: "P-1", From: "a", Body: "one"},
		{Seq: 2, Node: "P-2", From: "b", Body: "two"},
	}}
	ad := &recordingAdapter{}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() { Pump(ctx, src, ad, slog.Default()); close(done) }()

	require.Eventually(t, func() bool { return len(ad.events()) == 2 },
		2*time.Second, 10*time.Millisecond)
	assert.Equal(t, int64(1), ad.events()[0].Seq)
	assert.Equal(t, int64(2), ad.events()[1].Seq)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pump did not stop on ctx cancel")
	}
}

// TestPump_AdapterFailureIsNotFatal: a failed push is logged and dropped —
// the event stays in the durable inbox; the pump keeps serving later events.
func TestPump_AdapterFailureIsNotFatal(t *testing.T) {
	src := &stubSource{batch: []Event{{Seq: 1, Node: "P-1", From: "a", Body: "one"}}}
	ad := &recordingAdapter{fail: true}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	Pump(ctx, src, ad, slog.Default()) // must return via ctx, not panic/exit early
	assert.Empty(t, ad.events())
}
