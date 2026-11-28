// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package publisher_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/relay/publisher"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// gateJournal pauses the first publisher inside its cursor-read..advance
// window and counts every cursor read, so a second publisher entering
// that window is observable.
type gateJournal struct {
	*fakeJournal
	cursorReads atomic.Int32
	inWindow    atomic.Int32
	maxWindow   atomic.Int32
	gate        chan struct{} // closed to release the first reader
	entered     chan struct{}
	gated       atomic.Bool
}

func (g *gateJournal) RelayPushCursor(ctx context.Context) (sqlite.RelayPushPosition, error) {
	g.cursorReads.Add(1)
	n := g.inWindow.Add(1)
	for {
		m := g.maxWindow.Load()
		if n <= m || g.maxWindow.CompareAndSwap(m, n) {
			break
		}
	}
	return g.fakeJournal.RelayPushCursor(ctx)
}

func (g *gateJournal) ReadRelayJournalSince(ctx context.Context, seq int64, limit int) ([]sqlite.RelayJournalEvent, error) {
	if g.gated.CompareAndSwap(false, true) {
		close(g.entered)
		<-g.gate
	}
	return g.fakeJournal.ReadRelayJournalSince(ctx, seq, limit)
}

func (g *gateJournal) AdvanceRelayPushCursor(ctx context.Context, seq int64, rs uint64) error {
	defer g.inWindow.Add(-1)
	return g.fakeJournal.AdvanceRelayPushCursor(ctx, seq, rs)
}

func lockRows() *fakeJournal {
	f := &fakeJournal{pos: sqlite.RelayPushPosition{PubEpoch: 1, NextRS: 1}}
	for i, id := range []string{"e1", "e2", "e3", "e4", "e5", "e6"} {
		f.rows = append(f.rows, fakeRow(int64(i+1), id))
	}
	return f
}

// TestPublishLock_SecondPublisherWaitsOutAMidBatchPublisher is the
// FR-21 §6.2a item 7 red test: while one publisher is between its cursor
// read and its cursor advance, a second one on the same store must not
// read the cursor, because adoption cannot tell its records from a crash.
func TestPublishLock_SecondPublisherWaitsOutAMidBatchPublisher(t *testing.T) {
	g := &gateJournal{fakeJournal: lockRows(), gate: make(chan struct{}), entered: make(chan struct{})}
	cfg := fakeConfig(t, g)
	cfg.MaxSegmentBytes = 1
	cfgB := cfg
	cfgB.LockWait = 100 * time.Millisecond
	a, err := publisher.New(cfg)
	require.NoError(t, err)
	b, err := publisher.New(cfgB)
	require.NoError(t, err)

	errA := make(chan error, 1)
	go func() { _, e := a.PublishPending(context.Background()); errA <- e }()
	<-g.entered

	_, errB := b.PublishPending(context.Background())
	require.Equal(t, int32(1), g.cursorReads.Load(), "the second publisher read the cursor while the first was mid-batch")
	require.ErrorIs(t, errB, publisher.ErrPublishBusy)

	close(g.gate)
	require.NoError(t, <-errA)
	require.Equal(t, []string{"e1", "e2", "e3", "e4", "e5", "e6"}, mediumEventIDs(t, cfg.SegmentsDir))
	assertUniqueRS(t, cfg.SegmentsDir)
}

func assertUniqueRS(t *testing.T, dir string) {
	t.Helper()
	segs, _, err := segment.ListSegments(dir)
	require.NoError(t, err)
	seen := map[uint64]bool{}
	for i, s := range segs {
		res, readErr := segment.ScanFile(s.Path, segment.ScanOptions{
			Sealed: i < len(segs)-1, Key: testKey, ExpectPeerID: testPeerID,
		})
		require.NoError(t, readErr)
		for _, r := range res.Records {
			require.False(t, seen[r.RS], "duplicate rs %d on the medium", r.RS)
			seen[r.RS] = true
		}
	}
}

// TestPublishLock_ConcurrentPublishersNeverDuplicateAnRS hammers four
// publishers on one store; one record per segment makes every batch
// span files. Run with -race -count=50.
func TestPublishLock_ConcurrentPublishersNeverDuplicateAnRS(t *testing.T) {
	f := lockRows()
	cfg := fakeConfig(t, f)
	cfg.MaxSegmentBytes = 1
	cfg.BatchLimit = 2
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		p, err := publisher.New(cfg)
		require.NoError(t, err)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 6; j++ {
				_, _ = p.PublishPending(context.Background())
			}
		}()
	}
	wg.Wait()
	require.Equal(t, []string{"e1", "e2", "e3", "e4", "e5", "e6"}, mediumEventIDs(t, cfg.SegmentsDir))
	assertUniqueRS(t, cfg.SegmentsDir)
}

// heldJournal fails the test if the cursor is touched without the lock.
type heldJournal struct {
	*fakeJournal
	t    *testing.T
	held *atomic.Bool
	hits atomic.Int32
}

func (h *heldJournal) RelayPushCursor(ctx context.Context) (sqlite.RelayPushPosition, error) {
	h.hits.Add(1)
	require.True(h.t, h.held.Load(), "cursor read without the publish lock")
	return h.fakeJournal.RelayPushCursor(ctx)
}

func (h *heldJournal) AdvanceRelayPushCursor(ctx context.Context, seq int64, rs uint64) error {
	require.True(h.t, h.held.Load(), "cursor advance without the publish lock")
	return h.fakeJournal.AdvanceRelayPushCursor(ctx, seq, rs)
}

func (h *heldJournal) ResetRelayPublisher(ctx context.Context, floor int64, base uint64) error {
	h.hits.Add(1)
	require.True(h.t, h.held.Load(), "cursor reset without the publish lock")
	return h.fakeJournal.ResetRelayPublisher(ctx, floor, base)
}

func (h *heldJournal) RepublishRelayFrom(ctx context.Context, floor int64) error {
	h.hits.Add(1)
	require.True(h.t, h.held.Load(), "cursor rewind without the publish lock")
	return h.fakeJournal.RepublishRelayFrom(ctx, floor)
}

func lockedConfig(t *testing.T, held *atomic.Bool, busy bool) (publisher.Config, *heldJournal) {
	t.Helper()
	hj := &heldJournal{fakeJournal: lockRows(), t: t, held: held}
	cfg := fakeConfig(t, hj)
	cfg.LockWait = 30 * time.Millisecond
	cfg.Lock = func() (func() error, error) {
		if busy {
			return nil, publisher.ErrPublishBusy
		}
		held.Store(true)
		return func() error { held.Store(false); return nil }, nil
	}
	return cfg, hj
}

// TestPublishLock_EveryEntryPointHoldsTheLock drives each publishing
// entry point and checks the cursor is only touched under the lock, and
// not at all when the lock is held elsewhere.
func TestPublishLock_EveryEntryPointHoldsTheLock(t *testing.T) {
	entries := map[string]func(p *publisher.Publisher) error{
		"PublishPending": func(p *publisher.Publisher) error { _, e := p.PublishPending(context.Background()); return e },
		"ResetPeer":      func(p *publisher.Publisher) error { return p.ResetPeer(context.Background(), 0, 1) },
		"RepublishFrom":  func(p *publisher.Publisher) error { _, e := p.RepublishFrom(context.Background(), 1); return e },
		"OnCommit":       func(p *publisher.Publisher) error { p.OnCommit()(); return nil },
	}
	for name, call := range entries {
		t.Run(name+"/lock_free", func(t *testing.T) {
			var held atomic.Bool
			cfg, hj := lockedConfig(t, &held, false)
			p, err := publisher.New(cfg)
			require.NoError(t, err)
			_ = call(p)                  // outcome is irrelevant; the journal asserts the lock
			if name != "RepublishFrom" { // its floor lookup needs a published medium
				require.NotZero(t, hj.hits.Load(), "the entry point never reached the cursor")
			}
			require.False(t, held.Load(), "the lock must be released")
		})
		t.Run(name+"/lock_busy", func(t *testing.T) {
			var held atomic.Bool
			cfg, hj := lockedConfig(t, &held, true)
			p, err := publisher.New(cfg)
			require.NoError(t, err)
			if cerr := call(p); name != "OnCommit" {
				require.ErrorIs(t, cerr, publisher.ErrPublishBusy)
			}
			require.Zero(t, hj.hits.Load(), "a publisher that could not take the lock touched the cursor")
		})
	}
}

// TestPublishLock_EveryExportedEntryPointTakesIt is the source scan: any
// exported *Publisher method that mutates the cursor must call lockStore,
// so a new entry point cannot ship unlocked.
func TestPublishLock_EveryExportedEntryPointTakesIt(t *testing.T) {
	fset := token.NewFileSet()
	names, err := filepath.Glob("*.go")
	require.NoError(t, err)
	entry := map[string]bool{"PublishPending": false, "ResetPeer": false, "RepublishFrom": false, "OnCommit": false}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, perr)
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() {
				continue
			}
			calls := callsLockStore(fn)
			if _, tracked := entry[fn.Name.Name]; tracked {
				entry[fn.Name.Name] = calls
				require.True(t, calls, "%s must take the publish lock", fn.Name.Name)
			} else if !calls {
				require.False(t, touchesCursor(fn), "exported method %s touches the cursor without lockStore", fn.Name.Name)
			}
		}
	}
	for name, seen := range entry {
		require.True(t, seen, "entry point %s not found", name)
	}
}

func callsLockStore(fn *ast.FuncDecl) bool {
	return nodeCalls(fn, func(n string) bool { return n == "lockStore" })
}

func touchesCursor(fn *ast.FuncDecl) bool {
	return nodeCalls(fn, func(n string) bool {
		switch n {
		case "AdvanceRelayPushCursor", "ResetRelayPublisher", "RepublishRelayFrom", "publish", "publishLocked":
			return true
		}
		return false
	})
}

func nodeCalls(root ast.Node, match func(string) bool) bool {
	found := false
	ast.Inspect(root, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && match(sel.Sel.Name) {
			found = true
		}
		return !found
	})
	return found
}
