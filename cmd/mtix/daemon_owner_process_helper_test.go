// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

type daemonOwnerEmitter struct{ mu sync.Mutex }

func (e *daemonOwnerEmitter) send(round int, kind, native string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := json.NewEncoder(os.Stdout).Encode(daemonOwnerEvent{Round: round, PID: os.Getpid(), Kind: kind, Native: native}); err != nil {
		panic(err)
	}
}

type daemonOwnerOutput struct {
	emitter *daemonOwnerEmitter
	round   int
	running bool
}

func (w *daemonOwnerOutput) Write(p []byte) (int, error) {
	if strings.Contains(string(p), ": started (PID ") {
		if w.running {
			return 0, fmt.Errorf("duplicate native started output")
		}
		w.running = true
		w.emitter.send(w.round, "running", string(p))
	}
	return len(p), nil
}

type daemonOwnerSession struct {
	emitter  *daemonOwnerEmitter
	round    int
	spelling string
	cancel   context.CancelFunc
	done     chan struct{}
}

func (s *daemonOwnerSession) start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		output := &daemonOwnerOutput{emitter: s.emitter, round: s.round}
		var stderr bytes.Buffer
		err := runDaemonOwnerNative(ctx, output, &stderr, s.spelling)
		if err != nil {
			s.emitter.send(s.round, "error", err.Error()+"; "+stderr.String())
		} else if !output.running {
			kind := "unexpected-return"
			if strings.Contains(stderr.String(), "already running") {
				kind = "contended"
			}
			s.emitter.send(s.round, kind, stderr.String())
		}
		s.emitter.send(s.round, "idle", stderr.String())
	}()
}
func (s *daemonOwnerSession) stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	<-s.done
	s.cancel = nil
}
func runDaemonOwnerNative(ctx context.Context, stdout, stderr io.Writer, spelling string) error {
	if spelling == "daemon" {
		return runDaemon(ctx, stdout, stderr, nil, transport.Options{}, 3600)
	}
	return runSyncDaemon(ctx, stdout, stderr, nil, transport.Options{}, 3600, false)
}
func TestDaemonOwnerProcessHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	dir, err := os.Getwd()
	require.NoError(t, err)
	emitter := &daemonOwnerEmitter{}
	if mode != "session" {
		daemonOwnerMarkerHelper(t, dir, mode, emitter)
		return
	}
	store := newDaemonTestStore(t, dir)
	logger := slog.Default()
	app = appContext{mtixDir: dir, store: store, logger: logger,
		hooksDisp: service.NewHooksDispatcher(store, dir, logger)}
	session := &daemonOwnerSession{emitter: emitter}
	defer session.stop()
	emitter.send(0, "initialized", "")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if !session.command(t, scanner.Text()) {
			return
		}
	}
	require.NoError(t, scanner.Err())
}
func (s *daemonOwnerSession) command(t *testing.T, command string) bool {
	t.Helper()
	switch {
	case strings.HasPrefix(command, "prepare "):
		s.stop()
		_, err := fmt.Sscanf(command, "prepare %d %s", &s.round, &s.spelling)
		require.NoError(t, err)
		require.Contains(t, []string{"daemon", "syncdaemon"}, s.spelling)
		s.emitter.send(s.round, "ready", "")
	case command == "go":
		s.start()
	case command == "release":
		s.stop()
	case command == "quit":
		return false
	default:
		t.Fatalf("unknown owned control command %q", command)
	}
	return true
}
func daemonOwnerMarkerHelper(t *testing.T, dir, mode string, emitter *daemonOwnerEmitter) {
	switch mode {
	case "remove":
		failed, err := acquireDaemonOwnership(dir)
		require.ErrorIs(t, err, errDaemonLockHeld)
		require.Nil(t, failed)
		require.NoError(t, failed.release())
		require.NoError(t, (&daemonOwner{}).release())
		emitter.send(0, "removed", "")
	case "foreign":
		require.NoError(t, writeDaemonPID(dir, os.Getpid()))
		emitter.send(0, "foreign", "")
		_, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			require.ErrorIs(t, err, io.EOF)
		}
	default:
		t.Fatalf("unknown owned helper mode %s", mode)
	}
}
