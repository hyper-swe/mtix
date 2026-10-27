// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

// WakeAckStore supplies durable coverage and the local accounting source.
type WakeAckStore interface {
	WakeAckMark(context.Context, string) (int64, error)
	WakeBudgetSource(context.Context) (string, error)
}

// WakeAckPublisher publishes a newly effective acknowledgement across boards.
type WakeAckPublisher interface {
	Observe(context.Context, string, int64, int64) error
}

type wakeAckPublisher struct {
	store WakeAckStore
	state func() (*oplocal.State, string, error)
}

// NewWakeAckPublisher injects protected state and the registration's user identity.
func NewWakeAckPublisher(store WakeAckStore, state *oplocal.State, uid string) WakeAckPublisher {
	return &wakeAckPublisher{store: store, state: func() (*oplocal.State, string, error) { return state, uid, nil }}
}

func defaultWakeAckPublisher(st InboxAckStore) WakeAckPublisher {
	if _, ok := st.(inboxAtomicAcker); !ok {
		return nil
	}
	store, ok := st.(WakeAckStore)
	if !ok {
		return nil
	}
	return &wakeAckPublisher{store: store, state: processWakeAckState}
}

func processWakeAckState() (*oplocal.State, string, error) {
	env, err := oplocal.FromProcess()
	if err != nil {
		return nil, "", fmt.Errorf("acknowledgement operator environment: %w", err)
	}
	uid, err := wakeAckUserID()
	if err != nil {
		return nil, "", fmt.Errorf("resolve acknowledgement user identity: %w", err)
	}
	return oplocal.New(env), uid, nil
}

func (p *wakeAckPublisher) Observe(ctx context.Context, agent string, before, after int64) error {
	if after <= before || validateAgentID(agent) != nil {
		return nil // Idempotent acknowledgements and legacy non-wakeable ids have no guard effect.
	}
	state, uid, err := p.state()
	if err != nil {
		return fmt.Errorf("resolve acknowledgement operator state: %w", err)
	}
	if state == nil || p.store == nil {
		return fmt.Errorf("acknowledgement accounting dependencies are missing: %w", model.ErrInvalidInput)
	}
	host, err := state.ReadHostID()
	if err != nil {
		return fmt.Errorf("read acknowledgement host identity: %w", err)
	}
	if host == "" {
		return nil
	}
	key := model.WakeRecipient{AgentID: agent, HostID: host, UID: uid}
	_, present, budgetErr := state.ReadWakeBudget(key)
	if budgetErr != nil {
		return fmt.Errorf("read acknowledgement wake accounting: %w", budgetErr)
	}
	if !present {
		return nil // A normal acknowledgement never initializes operator accounting.
	}
	source, err := p.store.WakeBudgetSource(ctx)
	if err != nil {
		return fmt.Errorf("identify acknowledged board: %w", err)
	}
	return p.publish(ctx, state, key, source, after)
}

func (p *wakeAckPublisher) publish(ctx context.Context, state *oplocal.State, key model.WakeRecipient, source string, after int64) error {
	return state.UpdateWakeBudget(ctx, key, func(m *oplocal.WakeBudgetMirror, save func() error) error {
		current, err := p.store.WakeAckMark(ctx, key.AgentID)
		if err != nil {
			return fmt.Errorf("observe acknowledgement during publication: %w", err)
		}
		if after > current {
			return fmt.Errorf("acknowledgement was rewound during publication: %w", model.ErrConflict)
		}
		// The acknowledgement transaction proved this call made a new effect.
		// Ordinal equality after restore is not evidence of an idempotent call.
		if row, known := m.Sources[source]; known {
			row.AckMark = current
			m.Sources[source] = row
		}
		// Only admission establishes a wake-counter baseline. An ACK on an
		// unseen board still resets this recipient without inventing a zero floor.
		m.Unacked, m.NextWakeAt = 0, time.Time{}
		return save()
	})
}
