// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

const commentProcessCount = 14

// TestComment_ParallelProcesses_PersistEveryAnnotation runs N separate
// `mtix comment` processes against one node and requires N stored annotations
// (MTIX-148: a lost update reported success while dropping the comment).
func TestComment_ParallelProcesses_PersistEveryAnnotation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".mtix")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("prefix: TEST\n"), 0644))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dbPath := filepath.Join(dir, "data", "mtix.db")
	store, err := sqlite.New(dbPath, logger)
	require.NoError(t, err)
	clock := func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
	nodeSvc := service.NewNodeService(store, nil, &service.StaticConfig{}, logger, clock)
	node, err := nodeSvc.CreateNode(context.Background(), &service.CreateNodeRequest{Project: "TEST", Title: "parallel comments"})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	var wg sync.WaitGroup
	outputs := make([]string, commentProcessCount)
	errs := make([]error, commentProcessCount)
	start := make(chan struct{})
	for i := 0; i < commentProcessCount; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			outputs[i], errs[i] = runCommentChild(t, dir, node.ID, fmt.Sprintf("parallel-%02d", i))
		}(i)
	}
	close(start)
	wg.Wait()
	for i := range errs {
		require.NoError(t, errs[i], outputs[i])
		require.Contains(t, outputs[i], "Added annotation", "a reported success must be a stored comment")
	}

	reopened, err := sqlite.New(dbPath, logger)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	got, err := reopened.GetNode(context.Background(), node.ID)
	require.NoError(t, err)
	require.Len(t, got.Annotations, commentProcessCount)
}

func runCommentChild(t *testing.T, dir, id, text string) (string, error) {
	t.Helper()
	binary, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestComment_CLIProcessHelper$", "--", id, text)
	cmd.Dir = filepath.Dir(dir)
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(key, "MTIX_") && !strings.HasPrefix(key, "PG") && key != "XDG_CONFIG_HOME" {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "XDG_CONFIG_HOME="+t.TempDir())
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	return out.String(), err
}

// TestComment_CLIProcessHelper is the child body for the parallel-process test.
func TestComment_CLIProcessHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-3] != "--" {
		return
	}
	id, text := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	root := newRootCmd()
	root.SetArgs([]string{"comment", id, text})
	require.NoError(t, root.Execute())
	if app.store != nil {
		require.NoError(t, app.store.Close())
	}
	os.Exit(0)
}
