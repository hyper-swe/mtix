// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package hooks

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func deliverAppend(t *testing.T, base, rel string, ev Event, body string) error {
	t.Helper()
	return NewAppendFileAdapter(base).Deliver(context.Background(), appendDelivery(rel, ev, []byte(body)))
}

var okEvent = Event{Name: EventNodeCreated, NodeID: "HP-1"}

func TestAppendFile_FinalSymlinkIsRefusedAndTargetUntouched(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(t.TempDir(), "rc")
	require.NoError(t, os.WriteFile(outside, []byte("keep\n"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(base, "INBOX.md")))
	require.Error(t, deliverAppend(t, base, "INBOX.md", okEvent, `{}`))
	data, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "keep\n", string(data))
}

func TestAppendFile_SymlinkInsideTheProjectIsAlsoRefused(t *testing.T) {
	base := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(base, "real.log"), nil, 0o600))
	require.NoError(t, os.Symlink("real.log", filepath.Join(base, "link.log")))
	require.Error(t, deliverAppend(t, base, "link.log", okEvent, `{}`))
	data, _ := os.ReadFile(filepath.Join(base, "real.log"))
	require.Empty(t, data)
}

func TestAppendFile_SymlinkedParentDirectoryOutsideTheProjectIsRefused(t *testing.T) {
	base := t.TempDir()
	outsideDir := t.TempDir()
	require.NoError(t, os.Symlink(outsideDir, filepath.Join(base, "logs")))
	require.Error(t, deliverAppend(t, base, "logs/events.log", okEvent, `{}`))
	_, err := os.Stat(filepath.Join(outsideDir, "events.log"))
	require.True(t, os.IsNotExist(err))
}

func TestAppendFile_SymlinkedParentInsideTheProjectIsAllowed(t *testing.T) {
	base := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(base, "real"), 0o700))
	require.NoError(t, os.Symlink("real", filepath.Join(base, "logs")))
	require.NoError(t, deliverAppend(t, base, "logs/events.log", okEvent, `{}`))
	_, err := os.Stat(filepath.Join(base, "real", "events.log"))
	require.NoError(t, err)
}

// within fails the test fast instead of hanging when f blocks.
func within(t *testing.T, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("operation blocked")
	}
}

func TestAppendFile_NonRegularTargetIsRefused(t *testing.T) {
	base := t.TempDir()
	require.NoError(t, syscall.Mkfifo(filepath.Join(base, "pipe"), 0o600))
	within(t, func() { require.Error(t, deliverAppend(t, base, "pipe", okEvent, `{}`)) })
	require.NoError(t, os.Mkdir(filepath.Join(base, "dir"), 0o700))
	within(t, func() { require.Error(t, deliverAppend(t, base, "dir", okEvent, `{}`)) })
}

func openRoot(t *testing.T, base string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(base)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func TestOpenAppendFile_RefusesALinkWithoutAPreCheck(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(t.TempDir(), "rc")
	require.NoError(t, os.WriteFile(outside, []byte("keep\n"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(base, "link")))
	f, err := openAppendFile(openRoot(t, base), "link", nil)
	require.Error(t, err, "the open itself refuses a final link")
	require.Nil(t, f)
	data, _ := os.ReadFile(outside)
	require.Equal(t, "keep\n", string(data))
}

func TestOpenAppendFile_RefusesALinkThatStaysInsideTheProject(t *testing.T) {
	base := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(base, "real.log"), []byte("keep\n"), 0o600))
	require.NoError(t, os.Symlink("real.log", filepath.Join(base, "link.log")))
	f, err := openAppendFile(openRoot(t, base), "link.log", nil)
	require.Error(t, err, "a new file is created exclusively, so an existing link is never followed")
	require.Nil(t, f)
}

func TestOpenAppendFile_ALinkSwappedInAfterTheInspectionIsRefused(t *testing.T) {
	base := t.TempDir()
	root := openRoot(t, base)
	require.NoError(t, os.WriteFile(filepath.Join(base, "a.log"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(base, "b.log"), []byte("keep\n"), 0o600))
	pre, err := os.Lstat(filepath.Join(base, "a.log"))
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(base, "a.log")))
	require.NoError(t, os.Symlink("b.log", filepath.Join(base, "a.log")))
	_, err = openAppendFile(root, "a.log", pre)
	require.ErrorContains(t, err, "changed while it was being opened")
	data, _ := os.ReadFile(filepath.Join(base, "b.log"))
	require.Equal(t, "keep\n", string(data))
}

func TestOpenAppendFile_RefusesAPipeWithoutBlocking(t *testing.T) {
	base := t.TempDir()
	require.NoError(t, syscall.Mkfifo(filepath.Join(base, "pipe"), 0o600))
	pre, err := os.Lstat(filepath.Join(base, "pipe"))
	require.NoError(t, err)
	within(t, func() {
		f, err := openAppendFile(openRoot(t, base), "pipe", pre)
		require.Error(t, err)
		require.Nil(t, f)
	})
}

func TestOpenAppendFile_PostOpenChecksRefuseWhatThePreCheckDidNotSee(t *testing.T) {
	base := t.TempDir()
	root := openRoot(t, base)
	require.NoError(t, os.WriteFile(filepath.Join(base, "a.log"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(base, "b.log"), nil, 0o600))
	other, err := os.Lstat(filepath.Join(base, "b.log"))
	require.NoError(t, err)
	_, err = openAppendFile(root, "a.log", other)
	require.ErrorContains(t, err, "changed while it was being opened")
	same, err := os.Lstat(filepath.Join(base, "a.log"))
	require.NoError(t, err)
	f, err := openAppendFile(root, "a.log", same)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	outsideFile := filepath.Join(t.TempDir(), "rc")
	require.NoError(t, os.WriteFile(outsideFile, []byte("keep\n"), 0o600))
	require.NoError(t, os.Link(outsideFile, filepath.Join(base, "hard.log")))
	hardInfo, err := os.Lstat(filepath.Join(base, "hard.log"))
	require.NoError(t, err)
	_, err = openAppendFile(root, "hard.log", hardInfo)
	require.ErrorContains(t, err, "hard link")
}

func TestVerifyOpened_RefusesANonRegularFile(t *testing.T) {
	dev, err := os.OpenFile("/dev/null", os.O_WRONLY, 0)
	require.NoError(t, err)
	defer dev.Close()
	require.ErrorContains(t, verifyOpened(dev, nil), "not a regular file")
}

func TestAppendFile_HardLinkToAFileOutsideTheProjectIsRefused(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(t.TempDir(), "rc")
	require.NoError(t, os.WriteFile(outside, []byte("keep\n"), 0o600))
	require.NoError(t, os.Link(outside, filepath.Join(base, "INBOX.md")))
	require.Error(t, deliverAppend(t, base, "INBOX.md", okEvent, `{}`))
	data, _ := os.ReadFile(outside)
	require.Equal(t, "keep\n", string(data))
	issues := AppendFileIssues(base, appendHook("INBOX.md"))
	require.Len(t, issues, 1)
	require.True(t, issues[0].Refused)
	require.Contains(t, issues[0].Reason, "hard link")
}

func TestAppendFile_ParentSwappedForALinkAfterTheInspectionStaysInsideTheProject(t *testing.T) {
	base := t.TempDir()
	outsideDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(base, "real"), 0o700))
	root := openRoot(t, base)
	name, pre, issue := inspectAppend(root, base, "real/events.log")
	require.Nil(t, issue)
	require.NoError(t, os.Remove(filepath.Join(base, "real")))
	require.NoError(t, os.Symlink(outsideDir, filepath.Join(base, "real")))
	_, err := openAppendFile(root, name, pre)
	require.Error(t, err, "the root handle refuses the swapped parent at open time")
	_, statErr := os.Stat(filepath.Join(outsideDir, "events.log"))
	require.True(t, os.IsNotExist(statErr))
}

func TestAppendFile_NewFileIsCreatedWithMode0600(t *testing.T) {
	base := t.TempDir()
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	require.NoError(t, deliverAppend(t, base, "events.log", okEvent, `{}`))
	info, err := os.Stat(filepath.Join(base, "events.log"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestAppendFile_ControlCharactersInFieldsAreRefused(t *testing.T) {
	for name, ev := range map[string]Event{
		"newline in node id":  {Name: EventNodeCreated, NodeID: "HP-1\nrm -rf ~"},
		"tab in node id":      {Name: EventNodeCreated, NodeID: "HP-1\tx"},
		"escape in name":      {Name: "node.created\x1b[2J", NodeID: "HP-1"},
		"carriage return":     {Name: EventNodeCreated, NodeID: "HP-1\r"},
		"line separator":      {Name: EventNodeCreated, NodeID: "HP-1\u2028x"},
		"paragraph separator": {Name: EventNodeCreated, NodeID: "HP-1\u2029x"},
	} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			require.Error(t, deliverAppend(t, base, "events.log", ev, `{}`))
			_, err := os.Stat(filepath.Join(base, "events.log"))
			require.True(t, os.IsNotExist(err), "nothing is written for a refused event")
		})
	}
	base := t.TempDir()
	require.Error(t, deliverAppend(t, base, "events.log", okEvent, "{\n}"), "a payload with a line break is refused")
	require.Error(t, deliverAppend(t, base, "events.log", okEvent, "{\r}"), "a payload with a carriage return is refused")
}

func appendHook(rel string) Hook {
	return Hook{Name: "audit", AppendFile: &AppendFileConfig{Path: rel}}
}

func TestAppendFileIssues_GiveTheSpecificFixForEachCause(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(base, "logs")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "x"), filepath.Join(base, "INBOX.md")))
	require.NoError(t, syscall.Mkfifo(filepath.Join(base, "pipe"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(base, "dir"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(base, "ok.log"), nil, 0o600))
	require.Empty(t, AppendFileIssues(base, appendHook("ok.log")))
	require.Empty(t, AppendFileIssues(base, appendHook("new.log")))
	require.Empty(t, AppendFileIssues(base, Hook{Name: "x"}))
	for rel, fix := range map[string]string{
		"logs/e.log": "remove the link", "INBOX.md": "remove the link INBOX.md", "pipe": "remove pipe",
		"dir": "remove dir", "../x": "relative path", "/abs": "relative path",
	} {
		issues := AppendFileIssues(base, appendHook(rel))
		require.Len(t, issues, 1, rel)
		require.True(t, issues[0].Refused, rel)
		require.Equal(t, "audit", issues[0].Hook)
		require.Contains(t, issues[0].Fix, fix, rel)
	}
	require.Contains(t, AppendFileIssues(base, appendHook("INBOX.md"))[0].Reason, "link")
}

func TestAppendFileIssues_WarnAboutAnExistingFileWiderThanOwnerOnly(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "wide.log")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	require.NoError(t, os.Chmod(path, 0o644))
	issues := AppendFileIssues(base, appendHook("wide.log"))
	require.Len(t, issues, 1)
	require.False(t, issues[0].Refused, "delivery still writes; this is a warning")
	require.Equal(t, "chmod 600 wide.log", issues[0].Fix)
	require.NoError(t, deliverAppend(t, base, "wide.log", okEvent, `{}`))
}
