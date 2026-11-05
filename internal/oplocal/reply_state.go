// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ReplyWindow is a reply the sender is waiting for (MTIX-112.20.3, X6): the
// sender keeps 60 s hub pulls until the awaited agent answers or ReplyBy.
type ReplyWindow struct {
	AwaitingFrom string    `json:"awaiting_from"`
	Sender       string    `json:"sender"`
	Node         string    `json:"node"`
	OpenedSeq    int64     `json:"opened_seq"`
	OpenedAt     time.Time `json:"opened_at"`
	ReplyBy      time.Time `json:"reply_by"`
}

// ReplyHold is a recipient-side hold: an event carrying a reply deadline keeps
// this host's 60 s pulls until Until (clamped, MTIX-112.20.3, C2).
type ReplyHold struct {
	Sender  string    `json:"sender"`
	EventID string    `json:"event_id"`
	Start   time.Time `json:"start"`
	Until   time.Time `json:"until"`
}

// ReplyState is the operator-local reply accounting of one board. It never
// syncs and never touches the board or tasks.json (additive, ADR-007 A6).
type ReplyState struct {
	Windows []ReplyWindow `json:"windows,omitempty"`
	Holds   []ReplyHold   `json:"holds,omitempty"`
	// Extensions are the hold start times per sender, pruned to the last hour.
	Extensions map[string][]time.Time `json:"extensions,omitempty"`
	// Cursor is the highest sync_events rowid the reply scan has examined.
	Cursor int64 `json:"cursor,omitempty"`
	// Day is the UTC date (2006-01-02) HeldSecs counts; AccountedTo is how far
	// remote-held time has been accrued.
	Day         string    `json:"day,omitempty"`
	HeldSecs    int64     `json:"held_secs,omitempty"`
	AccountedTo time.Time `json:"accounted_to,omitempty"`
}

func replyStateName(key string) (string, error) { return hubRecordName("replywin", key) }

// ReadReplyState returns the board's reply accounting; absent is the zero value.
func (s *State) ReadReplyState(key string) (ReplyState, error) {
	var st ReplyState
	name, err := replyStateName(key)
	if err != nil {
		return st, err
	}
	reports, err := s.ReadJSON(name, &st)
	if err != nil || len(reports) != 0 {
		return ReplyState{}, err
	}
	return st, nil
}

// UpdateReplyState runs change on the board's reply accounting under an
// exclusive lock and writes the result back, so concurrent CLI, MCP and host
// processes never lose each other's updates.
func (s *State) UpdateReplyState(ctx context.Context, key string, change func(*ReplyState) error) (err error) {
	name, err := replyStateName(key)
	if err != nil {
		return err
	}
	dir, err := s.directory(true)
	if err != nil {
		return err
	}
	defer closeWith(dir, &err)
	lock, err := openNamedLock(dir, "replywin.lock", true)
	if err != nil {
		return failure("reply state lock", err)
	}
	if err = awaitAlertLock(ctx, lock); err != nil {
		return errors.Join(err, closeFile(lock))
	}
	defer func() { err = errors.Join(err, unlockAlert(lock)) }()
	var st ReplyState
	reports, err := s.ReadJSON(name, &st)
	if err != nil {
		return err
	}
	if len(reports) != 0 {
		return invalid("reply state belongs to another host")
	}
	if changeErr := change(&st); changeErr != nil {
		return changeErr
	}
	if writeErr := s.WriteJSON(name, st); writeErr != nil {
		return fmt.Errorf("write reply state: %w", writeErr)
	}
	return nil
}
