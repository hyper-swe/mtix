// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func appendDelivery(relPath string, event Event, body []byte) Delivery {
	return Delivery{
		Hook:      Hook{Name: "audit", AppendFile: &AppendFileConfig{Path: relPath}},
		Event:     event,
		EventJSON: body,
	}
}

func TestAppendFile_WritesPrefixedLineAndJSON(t *testing.T) {
	base := t.TempDir()
	a := NewAppendFileAdapter(base)

	body := []byte(`{"event":"status.changed","node":"HP-1.2"}`)
	ev := Event{Name: EventStatusChanged, NodeID: "HP-1.2"}
	require.NoError(t, a.Deliver(context.Background(), appendDelivery("events.log", ev, body)))

	data, err := os.ReadFile(filepath.Join(base, "events.log"))
	require.NoError(t, err)
	line := string(data)
	require.True(t, strings.HasSuffix(line, "\n"), "one complete line")
	fields := strings.Split(strings.TrimRight(line, "\n"), "\t")
	require.Len(t, fields, 4)
	require.Equal(t, EventStatusChanged, fields[1])
	require.Equal(t, "HP-1.2", fields[2])
	require.Equal(t, string(body), fields[3], "JSON appended verbatim")
}

func TestAppendFile_AppendOnlyAcrossTwoDeliveries(t *testing.T) {
	base := t.TempDir()
	a := NewAppendFileAdapter(base)
	ev := Event{Name: EventNodeCreated, NodeID: "HP-9"}

	require.NoError(t, a.Deliver(context.Background(), appendDelivery("events.log", ev, []byte(`{"n":1}`))))
	require.NoError(t, a.Deliver(context.Background(), appendDelivery("events.log", ev, []byte(`{"n":2}`))))

	data, err := os.ReadFile(filepath.Join(base, "events.log"))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	require.Len(t, lines, 2, "second delivery appended, did not truncate")
	require.True(t, strings.HasSuffix(lines[0], `{"n":1}`), "first line preserved")
	require.True(t, strings.HasSuffix(lines[1], `{"n":2}`))
}

func TestAppendFile_RejectsTraversalOutsideBaseDir(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "project")
	require.NoError(t, os.Mkdir(base, 0o755))
	a := NewAppendFileAdapter(base)
	ev := Event{Name: EventNodeCreated, NodeID: "HP-1"}

	err := a.Deliver(context.Background(), appendDelivery("../escape.log", ev, []byte(`{}`)))
	require.Error(t, err, "a `..` path that escapes baseDir is rejected")

	// Nothing was written outside the project.
	_, statErr := os.Stat(filepath.Join(root, "escape.log"))
	require.True(t, os.IsNotExist(statErr), "no file written outside baseDir")
}

func TestAppendFile_RejectsAbsolutePath(t *testing.T) {
	base := t.TempDir()
	a := NewAppendFileAdapter(base)
	outside := filepath.Join(t.TempDir(), "abs.log")

	err := a.Deliver(context.Background(),
		appendDelivery(outside, Event{Name: EventNodeCreated, NodeID: "HP-1"}, []byte(`{}`)))
	require.Error(t, err, "an absolute path is rejected")
	_, statErr := os.Stat(outside)
	require.True(t, os.IsNotExist(statErr))
}
