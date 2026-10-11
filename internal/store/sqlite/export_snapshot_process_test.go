// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func snapshotClock() time.Time { return time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC) }
func snapshotProcessStore(t *testing.T, dir string) *Store {
	t.Helper()
	store, err := New(filepath.Join(dir, "mtix.db"), slog.Default())
	require.NoError(t, err)
	store.SetClock(snapshotClock)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}
func TestExportSnapshotProcessHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	require.Equal(t, "writer", os.Args[len(os.Args)-1])
	dir, err := os.Getwd()
	require.NoError(t, err)
	store := snapshotProcessStore(t, dir)
	_, err = fmt.Fprintln(os.Stdout, "ready")
	require.NoError(t, err)
	_, err = bufio.NewReader(os.Stdin).ReadString('\n')
	if errors.Is(err, io.EOF) {
		return
	}
	require.NoError(t, err)
	require.NoError(t, snapshotWriteState(store))
	_, err = fmt.Fprintln(os.Stdout, "committed")
	require.NoError(t, err)
}

// All four exported tables change in ONE native writer transaction.
func snapshotWriteState(store *Store) error {
	return store.WithTx(context.Background(), func(tx *sql.Tx) error {
		statements := []string{
			`INSERT INTO nodes (id,depth,seq,project,title,priority,status,weight,created_at,updated_at,uid)
    VALUES ('SNAP-2',0,2,'SNAP','Atomic writer task',2,'open',1,'2026-10-11T00:00:00Z','2026-10-11T00:00:00Z','aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa')`,
			`INSERT INTO dependencies (from_id,to_id,dep_type,created_at) VALUES ('SNAP-1','SNAP-2','blocks','2026-10-11T00:00:00Z')`,
			`INSERT INTO agents (agent_id,project,state,current_node_id) VALUES ('worker-b','SNAP','idle','SNAP-2')`,
			`INSERT INTO sessions (id,agent_id,project,started_at,status) VALUES ('session-b','worker-b','SNAP','2026-10-11T00:00:00Z','active')`,
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(context.Background(), statement); err != nil {
				return err
			}
		}
		return nil
	})
}

type snapshotWriter struct {
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	lines         chan string
	done, scanned chan struct{}
	err           error
	stderr        bytes.Buffer
}

func startSnapshotWriter(t *testing.T, dir string) *snapshotWriter {
	t.Helper()
	child := &snapshotWriter{lines: make(chan string, 16), done: make(chan struct{}), scanned: make(chan struct{})}
	binary, err := os.Executable()
	require.NoError(t, err)
	child.cmd = exec.Command(binary, "-test.run=^TestExportSnapshotProcessHelper$", "--", "writer") //nolint:gosec,noctx // owned binary; cleanup kills and joins
	child.cmd.Dir = dir                                                                             // Owned project root is selected by child cwd, never argv.
	child.cmd.Stderr = &child.stderr
	child.stdin, err = child.cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := child.cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, child.cmd.Start())
	t.Cleanup(func() { child.close(t) })
	go child.scan(stdout)
	go func() { child.err = child.cmd.Wait(); close(child.done) }()
	require.NoError(t, child.awaitLine("ready"))
	return child
}
func (c *snapshotWriter) scan(stdout io.Reader) {
	defer close(c.scanned)
	defer close(c.lines)
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		c.lines <- scanner.Text()
	}
	if err := scanner.Err(); err != nil {
		c.lines <- "scanner-error: " + err.Error()
	}
}
func (c *snapshotWriter) awaitLine(want string) error {
	guard := time.NewTimer(60 * time.Second)
	defer guard.Stop()
	for {
		select {
		case line, ok := <-c.lines:
			if !ok {
				return fmt.Errorf("owned writer exited before %s", want)
			}
			if line == want {
				return nil
			}
		case <-guard.C:
			return fmt.Errorf("owned writer did not signal %s", want)
		}
	}
}
func (c *snapshotWriter) commitAndJoin() error {
	if _, err := io.WriteString(c.stdin, "commit\n"); err != nil {
		return err
	}
	if err := c.awaitLine("committed"); err != nil {
		return err
	}
	select {
	case <-c.done:
		if c.err != nil {
			return fmt.Errorf("owned writer failed: %w; %s", c.err, c.stderr.String())
		}
		return nil
	case <-time.After(60 * time.Second):
		return errors.New("owned writer commit did not join")
	}
}
func (c *snapshotWriter) close(t *testing.T) {
	t.Helper()
	if err := c.stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Errorf("close owned writer stdin: %v", err)
	}
	select {
	case <-c.done:
	case <-time.After(60 * time.Second):
		if err := c.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("kill owned writer: %v", err)
		}
		select {
		case <-c.done:
		case <-time.After(60 * time.Second):
			t.Error("killed writer did not join")
		}
	}
	select {
	case <-c.scanned:
	case <-time.After(60 * time.Second):
		t.Error("owned writer scanner did not join")
	}
}
