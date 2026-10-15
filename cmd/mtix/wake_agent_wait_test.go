// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

// Marker waits observe the owned wake process, so a startup failure reports
// the phase it reached and the output the process produced.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// wakeMarkerDeadline bounds how long a test waits for a marker file. It stays
// below the 60 second limit on the owned wake process.
const wakeMarkerDeadline = 30 * time.Second

// wakePhaseFiles lists the files the fixture creates in startup order.
var wakePhaseFiles = []string{"token", "stdin-", "calls", "started"}

// syncBuffer lets the test read process output while the process still writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitFile fails the test when the marker is missing at the deadline or when
// the owned wake process ends first.
func (p *wakeScriptProcess) waitFile(t *testing.T, name string) {
	t.Helper()
	if err := p.awaitFile(name, wakeMarkerDeadline); err != nil {
		t.Fatal(err)
	}
}

func (p *wakeScriptProcess) awaitFile(name string, limit time.Duration) error {
	path := filepath.Join(p.root, name)
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		_, err := os.Stat(path)
		if err == nil {
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat owned wake marker %s: %w", name, err)
		}
		select {
		case <-p.done:
			if _, err := os.Stat(path); err == nil {
				return nil
			}
			return fmt.Errorf("owned wake process ended (%v) before creating %s\n%s", p.err, name, p.diagnostics())
		case <-deadline.C:
			return fmt.Errorf("owned wake fixture did not create %s within %s\n%s", name, limit, p.diagnostics())
		case <-tick.C:
		}
	}
}

// diagnostics reads only files the owned fixture created in its own directory.
func (p *wakeScriptProcess) diagnostics() string {
	var b strings.Builder
	state := "running"
	select {
	case <-p.done:
		state = fmt.Sprintf("ended: %v", p.err)
	default:
	}
	fmt.Fprintf(&b, "process: %s\nphase files:", state)
	entries, _ := os.ReadDir(p.root)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, phase := range wakePhaseFiles {
		found := false
		for _, n := range names {
			found = found || strings.HasPrefix(n, phase)
		}
		fmt.Fprintf(&b, " %s=%t", strings.TrimSuffix(phase, "-"), found)
	}
	fmt.Fprintf(&b, "\nowned output:\n%s", p.output.String())
	if log, err := os.ReadFile(filepath.Join(p.root, "logs", "wake.log")); err == nil {
		fmt.Fprintf(&b, "owned log:\n%s", log)
	}
	return b.String()
}
