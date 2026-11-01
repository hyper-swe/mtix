// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// freeLoopbackPort returns a currently unused TCP port on 127.0.0.1.
func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

// startRealServe runs the real runServeWith in-process on an injected free
// port. It writes no lock and no record itself: discovery must come from
// serve. The returned stop function ends serve with SIGTERM and waits for it.
func startRealServe(t *testing.T, mtixDir string) (port int, stop func()) {
	t.Helper()
	port = freeLoopbackPort(t)
	sig := make(chan os.Signal, 8)
	signal.Notify(sig, syscall.SIGTERM) // keep a stray SIGTERM from killing the test binary
	t.Cleanup(func() { signal.Stop(sig) })

	done := make(chan error, 1)
	go func() { done <- runServeWith(serveConfig("127.0.0.1", port)) }()

	require.Eventually(t, func() bool {
		_, ok := readServeRecord(mtixDir)
		return ok
	}, 10*time.Second, 25*time.Millisecond, "serve never became discoverable")

	var stopped bool
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		timeout := time.After(15 * time.Second)
		for {
			select {
			case err := <-done:
				require.NoError(t, err)
				return
			case <-tick.C:
				require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGTERM))
			case <-timeout:
				t.Fatal("serve did not stop on SIGTERM")
			}
		}
	}
	t.Cleanup(stop)
	return port, stop
}

func TestServeRecord_RealServe_URLPrintsLinkAndRecordRemovedOnStop(t *testing.T) {
	initTestApp(t)
	id := newURLTestNode(t)
	mtixDir, err := findMtixDir()
	require.NoError(t, err)

	port, stop := startRealServe(t, mtixDir)
	_, statErr := os.Stat(filepath.Join(mtixDir, pidLockFile))
	require.True(t, os.IsNotExist(statErr), "serve must not write mtix.pid")

	out := captureStdout(t, func() { require.NoError(t, runURL(id)) })
	assert.Equal(t, "http://127.0.0.1:"+strconv.Itoa(port)+"/ticket/"+id+"\n", out)

	app.jsonOutput = true
	out = captureStdout(t, func() { require.NoError(t, runURL(id)) })
	app.jsonOutput = false
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "http://127.0.0.1:"+strconv.Itoa(port)+"/ticket/"+id, got["url"])

	stop()
	_, statErr = os.Stat(filepath.Join(mtixDir, serveRecordFile))
	assert.True(t, os.IsNotExist(statErr), "serve must remove its record on SIGTERM shutdown")
	var runErr error
	out = captureStdout(t, func() { runErr = runURL(id) })
	require.Error(t, runErr)
	assert.Empty(t, out)
}

func TestServeRecord_ProjectDirFlag_URLWorksFromUnrelatedCwd(t *testing.T) {
	initTestApp(t)
	id := newURLTestNode(t)
	mtixDir, err := findMtixDir()
	require.NoError(t, err)
	projectDir := filepath.Dir(mtixDir)
	port, _ := startRealServe(t, mtixDir)

	// The cwd is a different directory with its own, empty .mtix: only -C can
	// reach the project. The app is cold, so the node lookup cannot reuse the
	// store opened by initTestApp.
	elsewhere := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(elsewhere, ".mtix"), 0o755))
	require.NoError(t, os.Chdir(elsewhere))
	app = appContext{}
	resetCloseOnce()
	t.Cleanup(func() {
		if app.store != nil {
			_ = app.store.Close()
		}
	})

	root := newRootCmd()
	root.SetArgs([]string{"-C", projectDir, "url", id})
	var execErr error
	out := captureStdout(t, func() { execErr = root.Execute() })
	require.NoError(t, execErr)
	assert.Equal(t, "http://127.0.0.1:"+strconv.Itoa(port)+"/ticket/"+id+"\n", out)
}

func TestServeRecord_LiveRecordPresent_DoesNotRouteCommands(t *testing.T) {
	initTestApp(t)
	mtixDir, err := findMtixDir()
	require.NoError(t, err)
	port, _ := startRealServe(t, mtixDir)
	require.NotZero(t, port)

	for _, name := range []string{"show", "list", "tree", "stats", "create", "done"} {
		assert.Zero(t, shouldRouteToServer(&cobra.Command{Use: name}), name)
	}
}

func TestPublishServeRecord_WritesAtomicOwnerOnlyRecordWithClockTime(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 11, 1, 1, 2, 3, 0, time.UTC)
	require.NoError(t, publishServeRecord(dir, 4242, "127.0.0.1", 6849, at))

	info, err := os.Stat(filepath.Join(dir, serveRecordFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	raw, err := os.ReadFile(filepath.Join(dir, serveRecordFile))
	require.NoError(t, err)
	var rec map[string]any
	require.NoError(t, json.Unmarshal(raw, &rec))
	assert.EqualValues(t, 4242, rec["pid"])
	assert.EqualValues(t, 6849, rec["port"])
	assert.Equal(t, "127.0.0.1", rec["host"])
	assert.Equal(t, "2026-11-01T01:02:03Z", rec["started_at"])
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp file may be left behind")
}

func TestRemoveServeRecord_OtherPID_LeavesRecord(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, publishServeRecord(dir, 4242, "127.0.0.1", 6849, time.Unix(0, 0)))
	removeServeRecord(dir, 1111)
	_, err := os.Stat(filepath.Join(dir, serveRecordFile))
	require.NoError(t, err, "another serve's record must survive")
	removeServeRecord(dir, 4242)
	_, err = os.Stat(filepath.Join(dir, serveRecordFile))
	assert.True(t, os.IsNotExist(err))
}

func TestServeLinkHost_Table(t *testing.T) {
	tests := []struct{ bound, want string }{
		{"127.0.0.1", "127.0.0.1"}, {"", "127.0.0.1"}, {"0.0.0.0", "127.0.0.1"},
		{"::", "127.0.0.1"}, {"10.1.2.3", "10.1.2.3"}, {"::1", "[::1]"}, {"localhost", "localhost"},
	}
	for _, tt := range tests {
		t.Run(tt.bound, func(t *testing.T) {
			assert.Equal(t, tt.want, serveLinkHost(tt.bound))
		})
	}
}

func TestReadServeRecord_StaleCases_ReportNotRunning(t *testing.T) {
	deadPID := reapedChildPID(t)
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","version":"x"}`))
	}))
	t.Cleanup(healthy.Close)
	foreign := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(foreign.Close)
	healthyPort := healthy.Listener.Addr().(*net.TCPAddr).Port
	foreignPort := foreign.Listener.Addr().(*net.TCPAddr).Port

	tests := []struct {
		name    string
		content string
		alive   bool
		removed bool
	}{
		{"healthy live serve", `{"pid":` + strconv.Itoa(os.Getpid()) + `,"port":` + strconv.Itoa(healthyPort) + `,"host":"127.0.0.1"}`, true, false},
		{"dead pid", `{"pid":` + strconv.Itoa(deadPID) + `,"port":` + strconv.Itoa(healthyPort) + `,"host":"127.0.0.1"}`, false, true},
		{"corrupt json", `not json`, false, false},
		{"empty file", ``, false, false},
		{"zero port", `{"pid":` + strconv.Itoa(os.Getpid()) + `,"port":0,"host":"127.0.0.1"}`, false, false},
		{"live pid closed port", `{"pid":` + strconv.Itoa(os.Getpid()) + `,"port":` + strconv.Itoa(freeLoopbackPort(t)) + `,"host":"127.0.0.1"}`, false, false},
		{"live pid foreign http service", `{"pid":` + strconv.Itoa(os.Getpid()) + `,"port":` + strconv.Itoa(foreignPort) + `,"host":"127.0.0.1"}`, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, serveRecordFile)
			require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o600))
			rec, ok := readServeRecord(dir)
			assert.Equal(t, tt.alive, ok)
			if tt.alive {
				assert.Equal(t, healthyPort, rec.Port)
			}
			_, statErr := os.Stat(path)
			assert.Equal(t, tt.removed, os.IsNotExist(statErr))
		})
	}
}

func TestReadServeRecord_NoFile_ReportsNotRunning(t *testing.T) {
	_, ok := readServeRecord(t.TempDir())
	assert.False(t, ok)
}

func TestURL_StaleServeRecord_FailsNamingServeWithEmptyStdout(t *testing.T) {
	initTestApp(t)
	id := newURLTestNode(t)
	mtixDir, err := findMtixDir()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, serveRecordFile),
		[]byte(`{"pid":`+strconv.Itoa(reapedChildPID(t))+`,"port":6849,"host":"127.0.0.1"}`), 0o600))

	var runErr error
	out := captureStdout(t, func() { runErr = runURL(id) })
	require.Error(t, runErr)
	assert.Contains(t, runErr.Error(), "mtix serve")
	assert.Empty(t, out)
}

// serveRecordMarker is a value found only inside the serve record.
const serveRecordMarker = "2031-04-05T06:07:08Z"

func TestServeRecord_NeverLeaksIntoExportOrBackup(t *testing.T) {
	initTestApp(t)
	newURLTestNode(t)
	mtixDir, err := findMtixDir()
	require.NoError(t, err)
	marker, err := time.Parse(time.RFC3339, serveRecordMarker)
	require.NoError(t, err)
	require.NoError(t, publishServeRecord(mtixDir, os.Getpid(), "127.0.0.1", 6849, marker))

	require.NotNil(t, app.syncSvc)
	require.NoError(t, app.syncSvc.AutoExport(context.Background(), mtixDir))
	backup := filepath.Join(t.TempDir(), "backup.db")
	_, err = app.store.Backup(context.Background(), backup)
	require.NoError(t, err)

	// Every file under .mtix except the record itself, plus the backup copy.
	var checked int
	check := func(path string) {
		raw, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		assert.NotContains(t, string(raw), serveRecordMarker, path)
		checked++
	}
	walkErr := filepath.WalkDir(mtixDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && d.Name() != serveRecordFile {
			check(path)
		}
		return nil
	})
	require.NoError(t, walkErr)
	check(backup)
	assert.Greater(t, checked, 2, "tasks.json and the database must have been scanned")
}

func TestServeRecord_RepoGitignoreListsRecord(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "\n.mtix/serve.json\n")
}

func TestRunInit_IgnoresServeRecord(t *testing.T) {
	saveAndResetApp(t)
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, runInit("TEST"))
	ignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(ignore), ".mtix/serve.json\n")
}

// reapedChildPID returns the PID of a child that has exited and been reaped,
// so no live process owns it.
func reapedChildPID(t *testing.T) int {
	t.Helper()
	c := exec.Command("true")
	require.NoError(t, c.Run())
	return c.Process.Pid
}
