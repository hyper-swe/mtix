// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package tick

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/hyper-swe/mtix/internal/relay/ingest"
	"github.com/hyper-swe/mtix/internal/relay/keyring"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/hyper-swe/mtix/internal/relay/peerid"
	"github.com/hyper-swe/mtix/internal/relay/publisher"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

// PeersDirName is the relay subdirectory holding one directory per peer.
const PeersDirName = "peers"

// SegmentsDirName is a peer's own segment directory.
const SegmentsDirName = "segments"

// StackStore is the union of what a pass needs from the store.
type StackStore interface {
	publisher.Journal
	ingest.Store
}

// StackConfig opens a relay for one peer.
type StackConfig struct {
	Store StackStore

	// RelayDir is the relay root: the directory holding relay.json,
	// peers/ and bootstrap/.
	RelayDir string

	// KeysDir holds this peer's epoch keys. Authenticated relays only.
	KeysDir string

	// PeerID is this peer's identity under peers/.
	PeerID string

	// MaxSegmentBytes and BatchLimit tune publishing; zero takes the
	// package defaults.
	MaxSegmentBytes int64
	BatchLimit      int

	Logger *slog.Logger

	// LockDir is the project's .mtix directory, where the per-store
	// publish lock lives (FR-21 §6.2a item 7). Every process that
	// publishes for one store must name the same directory; empty falls
	// back to an in-process lock, which does not cover other processes.
	LockDir string

	// Now stamps quarantined records at ingest (MTIX-268); nil takes the wall clock.
	Now func() time.Time
}

// Stack is one peer's view of a relay: what it publishes with and what
// it reads with.
//
// It exists so the CLI verb and the daemon phase construct a relay the
// same way. Two call sites building this by hand would drift, and the
// first thing to drift would be which mode the reader trusts.
type Stack struct {
	// Publisher frames this peer's journal onto the medium.
	Publisher *publisher.Publisher

	// Ingestor applies every other peer's stream.
	Ingestor *ingest.Ingestor

	// Relay is the verified identity record.
	Relay *metadata.Relay

	// PeersDir is where the peer directories live, for status.
	PeersDir string
}

// Open builds a peer's relay stack from what is on the medium.
//
// The authentication mode comes from relay.json rather than from local
// configuration, and the key epoch from the frame the reader is looking
// at — so a peer cannot silently read a relay in a weaker mode than the
// relay was created in. An authenticated relay whose key is absent is a
// refusal here, before a single record is read (§6.5's fail-closed
// discipline, applied every pass rather than only at attach).
func Open(cfg StackConfig) (*Stack, error) {
	if cfg.Store == nil {
		return nil, errors.New("relay stack: store is required")
	}
	if !peerid.Valid(cfg.PeerID) {
		return nil, fmt.Errorf("relay stack: peer id %q is malformed", cfg.PeerID)
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}

	doc, err := metadata.Read(cfg.RelayDir)
	if err != nil {
		return nil, err
	}

	var ring *keyring.Ring
	var key []byte
	var epoch uint16
	if doc.Authenticated {
		var ok bool
		if epoch, ok = doc.CurrentKeyEpoch(); !ok {
			return nil, fmt.Errorf("%w: relay records no key epoch", metadata.ErrRelayCorrupt)
		}
		if ring, err = keyring.Load(cfg.KeysDir); err != nil {
			return nil, err
		}
		if key, err = ring.For(epoch); err != nil {
			return nil, err
		}
	}

	// Lstat each component before creating anything: a link planted at
	// peers/, peers/<id>/ or segments/ is RELAY_SYMLINK, never followed.
	segDir, err := segment.EnsureDir(cfg.RelayDir, PeersDirName, cfg.PeerID, SegmentsDirName)
	if err != nil {
		return nil, fmt.Errorf("relay stack: %w", err)
	}

	pub, err := publisher.New(publisher.Config{
		Journal:         cfg.Store,
		SegmentsDir:     segDir,
		PeerID:          cfg.PeerID,
		Key:             key,
		Unauthenticated: !doc.Authenticated,
		KeyEpoch:        epoch,
		MaxSegmentBytes: cfg.MaxSegmentBytes,
		BatchLimit:      cfg.BatchLimit,
		Logger:          log,
		Lock:            publishLock(cfg.LockDir),
	})
	if err != nil {
		return nil, err
	}

	peersDir := filepath.Join(cfg.RelayDir, PeersDirName)
	// The reader selects its key by the epoch each FRAME declares, not
	// by the relay's current one, so a rotation stays readable on both
	// sides of its boundary (D-R10).
	var keys ingest.KeySelector
	if doc.Authenticated {
		keys = ring
	}
	in, err := ingest.New(ingest.Config{
		Store:           cfg.Store,
		PeersDir:        peersDir,
		SelfPeerID:      cfg.PeerID,
		Keys:            keys,
		Unauthenticated: !doc.Authenticated,
		Logger:          log,
		Now:             cfg.Now,
	})
	if err != nil {
		return nil, err
	}
	return &Stack{Publisher: pub, Ingestor: in, Relay: doc, PeersDir: peersDir}, nil
}

// Pass runs one publish-then-ingest pass over this stack.
func (s *Stack) Pass(ctx context.Context) (Result, error) {
	return Run(ctx, s.Publisher, s.Ingestor)
}

// PublishLockName is the per-store publish lock file under .mtix/data.
const PublishLockName = "relay-publish.lock"

// publishLock builds the cross-process publish lock for lockDir, or nil
// (the in-process lock) when no directory is configured.
func publishLock(lockDir string) publisher.PublishLock {
	if lockDir == "" {
		return nil
	}
	return func() (func() error, error) {
		l, err := pushlock.AcquireNamed(lockDir, PublishLockName)
		if errors.Is(err, pushlock.ErrLockHeld) {
			return nil, publisher.ErrPublishBusy
		}
		if err != nil {
			return nil, err
		}
		return l.Release, nil
	}
}
