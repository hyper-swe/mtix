// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Git subprocesses have fixed policy, one caller budget and bounded output storage.
package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

const boardStderrLimit = 16 * 1024

type boardOutput struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	limit    int64
	overflow bool
	cancel   context.CancelFunc
}

func (b *boardOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - int64(b.buf.Len())
	if int64(len(p)) > remaining {
		if remaining > 0 {
			_, _ = b.buf.Write(p[:remaining])
		}
		b.overflow = true
		b.cancel()
		return 0, errors.New("bounded output exceeded")
	}
	return b.buf.Write(p)
}

func (b *boardOutput) result() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Bytes(), b.overflow
}

func boardGitArgs(args []string) []string {
	fixed := []string{"--no-pager", "--no-replace-objects", "--no-lazy-fetch",
		"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false",
		"-c", "core.pager=cat", "-c", "protocol.allow=never", "-c", "credential.helper="}
	return append(fixed, args...)
}

func boardGitEnv() []string {
	return []string{"LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_SYSTEM=" + os.DevNull, "GIT_ALLOW_PROTOCOL=", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1"}
}

func readGit(ctx context.Context, path, dir string, limit int64, args ...string) ([]byte, error) {
	if limit <= 0 {
		return nil, gitBoardError("invalid size cap", nil)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := &boardOutput{limit: limit, cancel: cancel}
	stderr := &boardOutput{limit: boardStderrLimit, cancel: cancel}
	cmd := exec.CommandContext(runCtx, path, boardGitArgs(args)...)
	cmd.Dir, cmd.Env = dir, boardGitEnv()
	cmd.Stdin = bytes.NewReader(nil)
	cmd.Stdout, cmd.Stderr = out, stderr
	cmd.WaitDelay = 200 * time.Millisecond
	if err := containBoardProcess(cmd); err != nil {
		return nil, err
	}
	err := cmd.Run()
	cleanupErr := cleanupBoardProcess(cmd)
	if err == nil {
		err = cleanupErr
	}
	data, over := out.result()
	_, errOver := stderr.result()
	if over || errOver {
		return nil, gitBoardError("output size limit", nil)
	}
	if ctx.Err() != nil {
		return nil, gitBoardError("operation canceled", ctx.Err())
	}
	if err != nil {
		return nil, gitBoardError("Git operation failed", nil)
	}
	return data, nil
}

var _ io.Writer = (*boardOutput)(nil)
