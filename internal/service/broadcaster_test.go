// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// TestBroadcaster_BroadcastEvent_AllSubscribersReceive verifies fan-out.
func TestBroadcaster_BroadcastEvent_AllSubscribersReceive(t *testing.T) {
	hub := service.NewHub(slog.Default())

	// Subscribe 3 listeners with no filter.
	ch1 := hub.Subscribe(service.SubscriptionFilter{})
	ch2 := hub.Subscribe(service.SubscriptionFilter{})
	ch3 := hub.Subscribe(service.SubscriptionFilter{})

	event := service.Event{
		Type:      service.EventNodeCreated,
		NodeID:    "PROJ-1",
		Timestamp: time.Now(),
	}

	err := hub.Broadcast(context.Background(), event)
	require.NoError(t, err)

	// All subscribers should receive the event.
	select {
	case e := <-ch1:
		assert.Equal(t, service.EventNodeCreated, e.Type)
	case <-time.After(time.Second):
		t.Fatal("ch1 timed out")
	}

	select {
	case e := <-ch2:
		assert.Equal(t, service.EventNodeCreated, e.Type)
	case <-time.After(time.Second):
		t.Fatal("ch2 timed out")
	}

	select {
	case e := <-ch3:
		assert.Equal(t, service.EventNodeCreated, e.Type)
	case <-time.After(time.Second):
		t.Fatal("ch3 timed out")
	}
}

// TestBroadcaster_SubscriptionFilter_UnderPrefix verifies prefix filtering.
func TestBroadcaster_SubscriptionFilter_UnderPrefix(t *testing.T) {
	hub := service.NewHub(slog.Default())

	// Subscribe only to PROJ-1 subtree.
	ch := hub.Subscribe(service.SubscriptionFilter{Under: "PROJ-1"})

	// Event from PROJ-1 subtree — should match.
	err := hub.Broadcast(context.Background(), service.Event{
		Type:   service.EventNodeCreated,
		NodeID: "PROJ-1.2",
	})
	require.NoError(t, err)

	// Event from PROJ-2 — should NOT match.
	err = hub.Broadcast(context.Background(), service.Event{
		Type:   service.EventNodeUpdated,
		NodeID: "PROJ-2.1",
	})
	require.NoError(t, err)

	// Should receive only the first event.
	select {
	case e := <-ch:
		assert.Equal(t, "PROJ-1.2", e.NodeID)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}

	// Should NOT receive the second event.
	select {
	case e := <-ch:
		t.Fatalf("unexpected event: %v", e)
	case <-time.After(100 * time.Millisecond):
		// Expected — no event.
	}
}

// TestBroadcaster_SubscriptionFilter_EventTypeWhitelist verifies type filtering.
func TestBroadcaster_SubscriptionFilter_EventTypeWhitelist(t *testing.T) {
	hub := service.NewHub(slog.Default())

	// Subscribe only to created events.
	ch := hub.Subscribe(service.SubscriptionFilter{
		Events: []service.EventType{service.EventNodeCreated},
	})

	// Send both created and updated.
	_ = hub.Broadcast(context.Background(), service.Event{
		Type:   service.EventNodeCreated,
		NodeID: "PROJ-1",
	})
	_ = hub.Broadcast(context.Background(), service.Event{
		Type:   service.EventNodeUpdated,
		NodeID: "PROJ-1",
	})

	// Should only receive created.
	select {
	case e := <-ch:
		assert.Equal(t, service.EventNodeCreated, e.Type)
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}

	select {
	case e := <-ch:
		t.Fatalf("unexpected event: %v", e)
	case <-time.After(100 * time.Millisecond):
		// Expected.
	}
}

// TestBroadcaster_SlowSubscriber_Dropped verifies slow subscriber handling.
func TestBroadcaster_SlowSubscriber_Dropped(t *testing.T) {
	hub := service.NewHub(slog.Default())

	// Subscribe and don't read from the channel.
	ch := hub.Subscribe(service.SubscriptionFilter{})

	// Fill the buffer (256 events).
	for i := 0; i < 257; i++ {
		_ = hub.Broadcast(context.Background(), service.Event{
			Type:   service.EventNodeCreated,
			NodeID: "PROJ-1",
		})
	}

	// The subscriber should have been dropped.
	assert.Equal(t, 0, hub.SubscriberCount(), "slow subscriber should be removed")

	// Channel should be closed.
	_, ok := <-ch
	// Once buffer is drained, channel is closed.
	// Let's drain the buffer first.
	for ok {
		_, ok = <-ch
	}
	assert.False(t, ok, "channel should be closed after drop")
}

// TestBroadcaster_Unsubscribe_StopsReceiving verifies unsubscribe.
func TestBroadcaster_Unsubscribe_StopsReceiving(t *testing.T) {
	hub := service.NewHub(slog.Default())

	ch := hub.Subscribe(service.SubscriptionFilter{})
	assert.Equal(t, 1, hub.SubscriberCount())

	hub.Unsubscribe(ch)
	assert.Equal(t, 0, hub.SubscriberCount())

	// Broadcasting after unsubscribe should succeed silently.
	err := hub.Broadcast(context.Background(), service.Event{
		Type:   service.EventNodeCreated,
		NodeID: "PROJ-1",
	})
	assert.NoError(t, err)
}

// TestBroadcaster_ConcurrentBroadcast_ThreadSafe verifies thread safety.
func TestBroadcaster_ConcurrentBroadcast_ThreadSafe(t *testing.T) {
	hub := service.NewHub(slog.Default())

	// Subscribe some listeners.
	channels := make([]<-chan service.Event, 5)
	for i := range channels {
		channels[i] = hub.Subscribe(service.SubscriptionFilter{})
	}

	// Broadcast from multiple goroutines concurrently.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = hub.Broadcast(context.Background(), service.Event{
					Type:   service.EventNodeCreated,
					NodeID: "PROJ-1",
				})
			}
		}(i)
	}
	wg.Wait()

	// Drain all channels to verify no deadlock.
	for _, ch := range channels {
		count := 0
		for {
			select {
			case <-ch:
				count++
			case <-time.After(100 * time.Millisecond):
				goto done
			}
		}
	done:
		assert.Greater(t, count, 0, "subscriber should have received some events")
	}
}

// TestBroadcaster_All11EventTypes_Valid verifies all event type constants exist.
func TestBroadcaster_All11EventTypes_Valid(t *testing.T) {
	allTypes := []service.EventType{
		service.EventNodeCreated,
		service.EventNodeUpdated,
		service.EventNodeDeleted,
		service.EventProgressChanged,
		service.EventNodeClaimed,
		service.EventNodeUnclaimed,
		service.EventNodeCancelled,
		service.EventNodesInvalidated,
		service.EventDependencyAdded,
		service.EventDependencyRemoved,
		service.EventStatusChanged,
	}

	for _, et := range allTypes {
		assert.NotEmpty(t, string(et), "event type should not be empty")
	}

	assert.Len(t, allTypes, 11, "should have exactly 11 event types")
}
