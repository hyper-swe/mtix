// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// HubMarkStore is the board-local storage the hub-connected policy needs.
type HubMarkStore interface {
	HubMark(ctx context.Context) (sqlite.HubMark, error)
	MarkHubConnected(ctx context.Context, source string) (bool, error)
	InferHubMark(ctx context.Context) (bool, error)
	AppliedEventsAboveFloor(ctx context.Context) (bool, error)
	ResetHubMark(ctx context.Context) (int64, error)
}

// HubEvidence is what the caller observed outside the store. EnvDSNConnected
// is true only after a sync or relay command actually connected with an
// environment DSN for this board; a DSN that is merely exported is false.
// EnvDSNExported is true while an environment DSN is exported: it is only
// transient evidence for the replace guards (SyncService.noteHubEvidence)
// and never becomes the sticky mark (MTIX-107.31.8).
type HubEvidence struct {
	ConfigKeys      bool
	SecretsDSN      bool
	EnvDSNConnected bool
	EnvDSNExported  bool
	// DSNRefused is true while a DSN is set up that the transport refuses to
	// use (a DSN key in a tracked config file, a secrets file with the wrong
	// mode). Like EnvDSNExported it is transient evidence for the replace
	// guards and never becomes the mark (MTIX-107.31.8).
	DSNRefused bool
}

// HubStatus is the answer to "is this board connected to a hub or relay".
// Flipped is true when this call set the mark: the signal the wake policy
// and alerts act on (they live in other tickets).
type HubStatus struct {
	Connected bool
	Source    string
	Flipped   bool
}

// HubMarkService decides, from sticky evidence, whether a board is
// connected to a hub or relay (ADR-007 §3.5, A9; MTIX-112.17.3).
type HubMarkService struct {
	store HubMarkStore
}

// NewHubMarkService builds the service over a board store.
func NewHubMarkService(store HubMarkStore) *HubMarkService {
	return &HubMarkService{store: store}
}

// HubConnected reports whether the board is hub-connected and persists the
// mark when evidence is found. The mark never reverts on config edits.
func (s *HubMarkService) HubConnected(ctx context.Context, ev HubEvidence) (HubStatus, error) {
	mark, err := s.store.HubMark(ctx)
	if err != nil {
		return HubStatus{}, fmt.Errorf("hub connected: %w", err)
	}
	if mark.Set {
		return HubStatus{Connected: true, Source: mark.Source}, nil
	}
	// A 0.5.x applier writes applied_events without the mark; rows above the
	// reset floor are evidence, and the mark is set when inferred.
	flipped, err := s.store.InferHubMark(ctx)
	if err != nil {
		return HubStatus{}, fmt.Errorf("hub connected: infer: %w", err)
	}
	if flipped {
		return HubStatus{Connected: true, Source: "applied_events", Flipped: true}, nil
	}
	source := evidenceSource(ev)
	if source == "" {
		return HubStatus{}, nil
	}
	flipped, err = s.store.MarkHubConnected(ctx, source)
	if err != nil {
		return HubStatus{}, fmt.Errorf("hub connected: mark %s: %w", source, err)
	}
	return HubStatus{Connected: true, Source: source, Flipped: flipped}, nil
}

// Inspect answers like HubConnected without writing anything (doctor).
func (s *HubMarkService) Inspect(ctx context.Context, ev HubEvidence) (HubStatus, error) {
	mark, err := s.store.HubMark(ctx)
	if err != nil {
		return HubStatus{}, fmt.Errorf("inspect hub mark: %w", err)
	}
	if mark.Set {
		return HubStatus{Connected: true, Source: mark.Source}, nil
	}
	has, err := s.store.AppliedEventsAboveFloor(ctx)
	if err != nil {
		return HubStatus{}, fmt.Errorf("inspect hub mark: %w", err)
	}
	if has {
		return HubStatus{Connected: true, Source: "applied_events"}, nil
	}
	source := evidenceSource(ev)
	return HubStatus{Connected: source != "", Source: source}, nil
}

// RecordEnvConnection marks the board after a sync or relay command
// actually connected with an environment DSN. It reports whether it flipped.
func (s *HubMarkService) RecordEnvConnection(ctx context.Context) (bool, error) {
	flipped, err := s.store.MarkHubConnected(ctx, "env_dsn")
	if err != nil {
		return false, fmt.Errorf("record env connection: %w", err)
	}
	return flipped, nil
}

// ResetHubMark clears the mark and records the applied_events floor so the
// next pass does not infer it again. Callers enforce the TTY rule.
func (s *HubMarkService) ResetHubMark(ctx context.Context) (int64, error) {
	floor, err := s.store.ResetHubMark(ctx)
	if err != nil {
		return 0, fmt.Errorf("reset hub mark: %w", err)
	}
	return floor, nil
}

// evidenceSource names the first piece of outside evidence, or "".
func evidenceSource(ev HubEvidence) string {
	switch {
	case ev.ConfigKeys:
		return "config"
	case ev.SecretsDSN:
		return "secrets"
	case ev.EnvDSNConnected:
		return "env_dsn"
	}
	return ""
}
