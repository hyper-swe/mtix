// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// The pull identity check counts real transport connections on the existing command path.

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func identityConnectionProxy(t *testing.T, dsn string) (string, *atomic.Int64) {
	t.Helper()
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	upstream := parsed.Host
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var count atomic.Int64
	var workers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			incoming, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			count.Add(1)
			workers.Add(1)
			go func() { defer workers.Done(); identityProxyConnection(incoming, upstream) }()
		}
	}()
	t.Cleanup(func() { require.NoError(t, listener.Close()); <-done; workers.Wait() })
	parsed.Host = listener.Addr().String()
	return parsed.String(), &count
}

func identityProxyConnection(incoming net.Conn, upstream string) {
	defer incoming.Close()
	target, err := net.Dial("tcp", upstream)
	if err != nil {
		return
	}
	defer target.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(target, incoming)
		if tcp, ok := target.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	_, _ = io.Copy(incoming, target)
	_ = incoming.Close()
	<-done
}

func TestHubIdentity_CachedFromPull_NoExtraDial(t *testing.T) {
	hub := openCmdHub(t)
	initTestApp(t)
	state := identityOperatorState(t)
	id := "0c6f03d9-3633-4ab7-bcc9-b8da8d966023"
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "board-id"), []byte(id+"\n"), 0600))
	events := remoteCreates(t, 5)
	pushToHub(t, hub, events...)
	require.NoError(t, registerProjectOnHub(t.Context(), hub, "TEST", "first"))
	dsn, connections := identityConnectionProxy(t, os.Getenv(transport.EnvDSN))
	t.Setenv(transport.EnvDSN, dsn)
	var out, diagnostic bytes.Buffer
	require.NoError(t, runSyncPull(context.Background(), &out, &diagnostic, nil, transport.Options{InsecureTLS: true}, 100), diagnostic.String())
	assert.Equal(t, int64(1), connections.Load(), "identity lookup reuses the existing connection")
	requireAppliedOnce(t, events[0])
	observed, reports, err := state.BoardObservation(filepath.Dir(app.mtixDir))
	require.NoError(t, err)
	require.Empty(t, reports)
	assert.Equal(t, "TEST", observed.Hub.ProjectPrefix)
	assert.Equal(t, "first", observed.Hub.FirstEventHash)
	assert.Equal(t, id, observed.BoardID)
}

func TestHubIdentity_PullCacheRefusal_WarnsAndPreservesPreviousObservation(t *testing.T) {
	hub := openCmdHub(t)
	initTestApp(t)
	state := identityOperatorState(t)
	id := "0c6f03d9-3633-4ab7-bcc9-b8da8d966023"
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "board-id"), []byte(id+"\n"), 0600))
	checkout := filepath.Dir(app.mtixDir)
	previous := oplocal.HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "previous"}
	require.NoError(t, state.ObserveBoard(checkout, id, previous))
	events := remoteCreates(t, 5)
	pushToHub(t, hub, events...)
	require.NoError(t, registerProjectOnHub(t.Context(), hub, "TEST", "first"))
	dsn, connections := identityConnectionProxy(t, os.Getenv(transport.EnvDSN))
	t.Setenv(transport.EnvDSN, dsn)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, diagnostic bytes.Buffer
	err := runSyncPull(t.Context(), &out, &diagnostic, nil, transport.Options{InsecureTLS: true}, 100)
	require.NoError(t, err, "caching is best-effort: data was applied")
	assert.Contains(t, diagnostic.String(), "WARN")
	assert.Equal(t, int64(1), connections.Load())
	requireAppliedOnce(t, events[0])
	observed, _, err := state.BoardObservation(checkout)
	require.NoError(t, err)
	assert.Equal(t, previous, observed.Hub)
	assert.Equal(t, id, observed.BoardID)
}

func TestHubIdentity_ExistingFirstHashReader_UsesSeededPool(t *testing.T) {
	hub := openCmdHub(t)
	require.NoError(t, registerProjectOnHub(t.Context(), hub, "TEST", "first"))
	prefix, hash, err := readHubFirstEventHash(t.Context(), hub, "TEST")
	require.NoError(t, err)
	assert.Equal(t, "TEST", prefix)
	assert.Equal(t, "first", hash)
	prefix, hash, err = readHubFirstEventHash(t.Context(), hub, "OTHER")
	require.NoError(t, err)
	assert.Empty(t, prefix)
	assert.Empty(t, hash)
}
